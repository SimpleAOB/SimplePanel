// Command sp is the SimplePanel command-line client, for scripts and CI.
//
//	sp login https://panel.example.com
//	sp deploy shop/web --wait
//
// It talks to the panel's REST API with an API token (Settings → API & CLI).
// SP_URL and SP_TOKEN override the saved login, which suits CI.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

var version = "dev"

const usage = `sp: the SimplePanel command line

Usage: sp <command> [arguments]

  login [URL]                     save the panel URL and an API token
  logout                          forget them
  whoami                          show who the token acts for

  projects                        list projects
  services [PROJECT]              list services (apps and databases)
  status SERVICE                  show a service's status and domains
  deploy SERVICE [--wait] [--tag TAG]
                                  deploy; --wait follows the build log and
                                  exits non-zero if the deploy fails
  deployments SERVICE             list recent deployments
  logs SERVICE [--tail N] [-f] [--since 1h] [--launch N]
                                  show (or follow) container logs; --launch
                                  shows one container start, even a past one
  launches SERVICE                list recent container starts (launches)
  restart | stop | start SERVICE

  env list SERVICE                list environment variables
  env set SERVICE KEY=VALUE... [--deploy]
  env unset SERVICE KEY... [--deploy]

  cron list SERVICE               list cron jobs
  cron run SERVICE JOB [--wait]   run a cron job now (JOB is its name or id)

  previews SERVICE                list pull request previews

  api METHOD PATH [JSON]          call any API endpoint, e.g. sp api GET /projects
  version

SERVICE is project/name, e.g. shop/web. Add --json to most commands for raw
JSON. SP_URL and SP_TOKEN override the saved login (handy in CI).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sp:", err)
		var ee exitErr
		if errors.As(err, &ee) {
			os.Exit(int(ee))
		}
		os.Exit(1)
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// ---- config ----

type config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "simplepanel", "cli.json"), nil
}

func loadConfig() (config, error) {
	var c config
	if p, err := configPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &c)
		}
	}
	if v := os.Getenv("SP_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("SP_TOKEN"); v != "" {
		c.Token = v
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.URL == "" || c.Token == "" {
		return c, errors.New("not logged in: run `sp login https://your-panel`, or set SP_URL and SP_TOKEN")
	}
	return c, nil
}

func saveConfig(c config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// ---- HTTP ----

type client struct {
	cfg  config
	http *http.Client
}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Msg, e.Status) }

func (c *client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			rd = bytes.NewReader(raw)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			rd = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(method, c.cfg.URL+"/api"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", "simplepanel-cli/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return &apiError{res.StatusCode, e.Error}
	}
	if out != nil {
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = b
			return nil
		}
		return json.Unmarshal(b, out)
	}
	return nil
}

func newClient() (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return &client{cfg: cfg, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

// ---- types ----

type status struct {
	State   string `json:"state"`
	Running int    `json:"running"`
	Desired int    `json:"desired"`
	Message string `json:"message"`
}

type app struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ProjectName string `json:"projectName"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Image       string `json:"image"`
	GitURL      string `json:"gitUrl"`
	GitBranch   string `json:"gitBranch"`
	Env         string `json:"env"`
	ParentAppID int64  `json:"parentAppId"`
	PRNumber    int    `json:"prNumber"`
}

type appDetail struct {
	App      app    `json:"app"`
	Status   status `json:"status"`
	AutoHost string `json:"autoHost"`
	Domains  []struct {
		Host  string `json:"host"`
		Path  string `json:"path"`
		HTTPS bool   `json:"https"`
	} `json:"domains"`
}

type deployment struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Trigger   string `json:"trigger"`
	CommitSHA string `json:"commitSha"`
	CommitMsg string `json:"commitMsg"`
	Log       string `json:"log"`
	CreatedAt int64  `json:"createdAt"`
}

func (c *client) service(ref string) (*appDetail, error) {
	p, n, ok := strings.Cut(ref, "/")
	if !ok || p == "" || n == "" {
		return nil, fmt.Errorf("name the service as project/name, e.g. shop/web (got %q)", ref)
	}
	var d appDetail
	if err := c.do("GET", "/services/"+url.PathEscape(p)+"/"+url.PathEscape(n), nil, &d); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, fmt.Errorf("service %s not found", ref)
		}
		return nil, err
	}
	return &d, nil
}

// ---- commands ----

// flags parses args with fs, allowing flags before and after positionals.
func flags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func need(pos []string, n int, what string) error {
	if len(pos) < n {
		return fmt.Errorf("usage: sp %s", what)
	}
	return nil
}

func printJSON(raw []byte) {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Println(string(raw))
}

func ago(unix int64) string {
	if unix == 0 {
		return ""
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		fmt.Println("sp", version, runtime.GOOS+"/"+runtime.GOARCH)
		return nil
	case "login":
		return login(args)
	case "logout":
		p, err := configPath()
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("Logged out.")
		return nil
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	switch cmd {
	case "whoami":
		var me struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		}
		if err := c.do("GET", "/me", nil, &me); err != nil {
			return err
		}
		fmt.Printf("%s (%s) on %s\n", me.Username, me.Role, c.cfg.URL)
		return nil

	case "projects":
		if _, err := flags(fs, args); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.do("GET", "/projects", nil, &raw); err != nil {
			return err
		}
		if *asJSON {
			printJSON(raw)
			return nil
		}
		var ps []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &ps)
		for _, p := range ps {
			fmt.Println(p.Name)
		}
		return nil

	case "services", "ls":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		var ps []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := c.do("GET", "/projects", nil, &ps); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if !*asJSON {
			fmt.Fprintln(tw, "SERVICE\tTYPE\tSTATUS\tSOURCE")
		}
		var all []json.RawMessage
		for _, p := range ps {
			if len(pos) > 0 && p.Name != pos[0] {
				continue
			}
			var rows []json.RawMessage
			if err := c.do("GET", fmt.Sprintf("/projects/%d/apps", p.ID), nil, &rows); err != nil {
				return err
			}
			all = append(all, rows...)
			for _, r := range rows {
				var a struct {
					app
					Status status `json:"status"`
				}
				_ = json.Unmarshal(r, &a)
				src := a.Image
				if a.Source == "git" {
					src = a.GitURL
					if a.GitBranch != "" {
						src += "@" + a.GitBranch
					}
				}
				kind := a.Kind
				if a.ParentAppID != 0 {
					kind = fmt.Sprintf("preview #%d", a.PRNumber)
				}
				fmt.Fprintf(tw, "%s/%s\t%s\t%s %d/%d\t%s\n", a.ProjectName, a.Name, kind, a.Status.State, a.Status.Running, a.Status.Desired, src)
			}
		}
		if *asJSON {
			b, _ := json.Marshal(all)
			printJSON(b)
			return nil
		}
		return tw.Flush()

	case "status":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "status SERVICE"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		if *asJSON {
			b, _ := json.Marshal(d)
			printJSON(b)
			return nil
		}
		fmt.Printf("%s/%s: %s (%d/%d running)\n", d.App.ProjectName, d.App.Name, d.Status.State, d.Status.Running, d.Status.Desired)
		if d.Status.Message != "" {
			fmt.Println("  ", d.Status.Message)
		}
		if d.AutoHost != "" {
			fmt.Println("  https://" + d.AutoHost)
		}
		for _, dm := range d.Domains {
			scheme := "http"
			if dm.HTTPS {
				scheme = "https"
			}
			fmt.Printf("  %s://%s%s\n", scheme, dm.Host, dm.Path)
		}
		return nil

	case "deploy":
		wait := fs.Bool("wait", false, "")
		tag := fs.String("tag", "", "")
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "deploy SERVICE [--wait] [--tag TAG]"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		if *tag != "" {
			if d.App.Source != "image" {
				return errors.New("--tag only applies to services that run a Docker image")
			}
			// Keep the image name, swap the tag.
			img := d.App.Image
			if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
				img = img[:i]
			}
			var full map[string]any
			var raw json.RawMessage
			if err := c.do("GET", fmt.Sprintf("/apps/%d", d.App.ID), nil, &raw); err != nil {
				return err
			}
			var wrap struct {
				App map[string]any `json:"app"`
			}
			_ = json.Unmarshal(raw, &wrap)
			full = wrap.App
			full["image"] = img + ":" + *tag
			if err := c.do("PUT", fmt.Sprintf("/apps/%d", d.App.ID), full, nil); err != nil {
				return err
			}
		}
		var dep deployment
		if err := c.do("POST", fmt.Sprintf("/apps/%d/deploy", d.App.ID), map[string]any{}, &dep); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Deployment #%d started for %s\n", dep.ID, pos[0])
		if !*wait {
			return nil
		}
		return followDeployment(c, dep.ID)

	case "deployments":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "deployments SERVICE"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.do("GET", fmt.Sprintf("/apps/%d/deployments", d.App.ID), nil, &raw); err != nil {
			return err
		}
		if *asJSON {
			printJSON(raw)
			return nil
		}
		var ds []deployment
		_ = json.Unmarshal(raw, &ds)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATUS\tTRIGGER\tCOMMIT\tWHEN")
		for _, x := range ds {
			commit := ""
			if x.CommitSHA != "" {
				commit = x.CommitSHA[:min(7, len(x.CommitSHA))] + " " + x.CommitMsg
			}
			fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\n", x.ID, x.Status, x.Trigger, commit, ago(x.CreatedAt))
		}
		return tw.Flush()

	case "launches":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "launches SERVICE"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		var raw struct {
			Runs json.RawMessage `json:"runs"`
		}
		if err := c.do("GET", fmt.Sprintf("/apps/%d/runs", d.App.ID), nil, &raw); err != nil {
			return err
		}
		if *asJSON {
			printJSON(raw.Runs)
			return nil
		}
		var runs []struct {
			Number       int64  `json:"number"`
			State        string `json:"state"`
			ExitCode     *int   `json:"exitCode"`
			Message      string `json:"message"`
			DeploymentID *int64 `json:"deploymentId"`
			StartedAt    int64  `json:"startedAt"`
			EndedAt      *int64 `json:"endedAt"`
		}
		_ = json.Unmarshal(raw.Runs, &runs)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "LAUNCH\tSTATE\tEXIT\tDEPLOY\tSTARTED\tENDED\tMESSAGE")
		for _, x := range runs {
			exit, dep, ended := "", "", ""
			if x.ExitCode != nil {
				exit = strconv.Itoa(*x.ExitCode)
			}
			if x.DeploymentID != nil {
				dep = fmt.Sprintf("#%d", *x.DeploymentID)
			}
			if x.EndedAt != nil {
				ended = ago(*x.EndedAt)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", x.Number, x.State, exit, dep, ago(x.StartedAt), ended, x.Message)
		}
		return tw.Flush()

	case "logs":
		tail := fs.Int("tail", 200, "")
		follow := fs.Bool("f", false, "")
		fs.BoolVar(follow, "follow", false, "")
		sinceFlag := fs.Duration("since", 0, "")
		launch := fs.Int("launch", 0, "")
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "logs SERVICE [--tail N] [-f] [--since 1h] [--launch N]"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		query := fmt.Sprintf("/apps/%d/logs?tail=%d", d.App.ID, *tail)
		if *sinceFlag > 0 {
			query += fmt.Sprintf("&since=%d", time.Now().Add(-*sinceFlag).Unix())
		}
		if *launch > 0 {
			query += fmt.Sprintf("&run=%d", *launch)
		}
		last := ""
		for {
			var out struct {
				Logs string `json:"logs"`
			}
			if err := c.do("GET", query, nil, &out); err != nil {
				return err
			}
			lines := strings.Split(strings.TrimRight(out.Logs, "\n"), "\n")
			start := 0
			if last != "" {
				start = len(lines)
				for i := len(lines) - 1; i >= 0; i-- {
					if lines[i] == last {
						start = i + 1
						break
					}
				}
				if start == len(lines) && lines[len(lines)-1] != last {
					start = 0 // lost our place; print everything
				}
			}
			for _, l := range lines[start:] {
				if l != "" {
					fmt.Println(l)
				}
			}
			if n := len(lines); n > 0 && lines[n-1] != "" {
				last = lines[n-1]
			}
			if !*follow {
				return nil
			}
			time.Sleep(2 * time.Second)
		}

	case "restart", "stop", "start":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, cmd+" SERVICE"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		if err := c.do("POST", fmt.Sprintf("/apps/%d/%s", d.App.ID, cmd), map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Printf("%s: %s requested\n", pos[0], cmd)
		return nil

	case "env":
		return envCmd(c, args)
	case "cron":
		return cronCmd(c, args)

	case "previews":
		pos, err := flags(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "previews SERVICE"); err != nil {
			return err
		}
		d, err := c.service(pos[0])
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.do("GET", fmt.Sprintf("/apps/%d/previews", d.App.ID), nil, &raw); err != nil {
			return err
		}
		if *asJSON {
			printJSON(raw)
			return nil
		}
		var ps []struct {
			Name     string `json:"name"`
			PRNumber int    `json:"prNumber"`
			PRTitle  string `json:"prTitle"`
			Branch   string `json:"branch"`
			URL      string `json:"url"`
			Status   status `json:"status"`
		}
		_ = json.Unmarshal(raw, &ps)
		if len(ps) == 0 {
			fmt.Println("No previews.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PR\tBRANCH\tSTATUS\tURL\tTITLE")
		for _, p := range ps {
			fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\n", p.PRNumber, p.Branch, p.Status.State, p.URL, p.PRTitle)
		}
		return tw.Flush()

	case "api":
		if len(args) < 2 {
			return errors.New("usage: sp api METHOD PATH [JSON]  (PATH without /api, e.g. /projects)")
		}
		method := strings.ToUpper(args[0])
		path := "/" + strings.TrimPrefix(strings.TrimPrefix(args[1], "/api"), "/")
		var body any
		if len(args) > 2 {
			if !json.Valid([]byte(args[2])) {
				return errors.New("the body isn't valid JSON")
			}
			body = json.RawMessage(args[2])
		} else if method != "GET" && method != "DELETE" {
			body = json.RawMessage("{}")
		}
		var raw json.RawMessage
		if err := c.do(method, path, body, &raw); err != nil {
			return err
		}
		if len(raw) > 0 {
			printJSON(raw)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q; run `sp help`", cmd)
}

func login(args []string) error {
	in := bufio.NewReader(os.Stdin)
	u := ""
	if len(args) > 0 {
		u = args[0]
	} else {
		fmt.Print("Panel URL (e.g. https://panel.example.com): ")
		line, _ := in.ReadString('\n')
		u = strings.TrimSpace(line)
	}
	u = strings.TrimRight(u, "/")
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
		return errors.New("enter the panel's address, like https://panel.example.com")
	}
	if pu.Scheme == "http" && pu.Hostname() != "localhost" && pu.Hostname() != "127.0.0.1" {
		fmt.Fprintln(os.Stderr, "Warning: this address isn't HTTPS, so the token is sent unencrypted. Set a panel domain to get HTTPS.")
	}
	tok := os.Getenv("SP_TOKEN")
	if tok == "" {
		fmt.Println("Create a token with API access in SimplePanel: Settings → API & CLI.")
		fmt.Print("Paste the token: ")
		tok = readSecret(in)
		fmt.Println()
	}
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, "sp_") {
		return errors.New("SimplePanel tokens start with sp_")
	}
	c := &client{cfg: config{URL: u, Token: tok}, http: &http.Client{Timeout: 30 * time.Second}}
	var me struct {
		Username string `json:"username"`
	}
	if err := c.do("GET", "/me", nil, &me); err != nil {
		return fmt.Errorf("the panel didn't accept the token: %w", err)
	}
	if err := saveConfig(c.cfg); err != nil {
		return err
	}
	p, _ := configPath()
	fmt.Printf("Logged in to %s as %s. Saved to %s\n", u, me.Username, p)
	return nil
}

// readSecret reads a line without echoing it where the terminal allows.
func readSecret(in *bufio.Reader) string {
	if runtime.GOOS != "windows" {
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		if off.Run() == nil {
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = os.Stdin
				_ = on.Run()
			}()
		}
	}
	line, _ := in.ReadString('\n')
	return line
}

func followDeployment(c *client, id int64) error {
	printed := 0
	for {
		var d deployment
		if err := c.do("GET", fmt.Sprintf("/deployments/%d", id), nil, &d); err != nil {
			return err
		}
		if len(d.Log) > printed {
			fmt.Print(d.Log[printed:])
			printed = len(d.Log)
		}
		switch d.Status {
		case "succeeded":
			fmt.Fprintf(os.Stderr, "Deployment #%d succeeded\n", id)
			return nil
		case "failed":
			fmt.Fprintf(os.Stderr, "Deployment #%d failed\n", id)
			return exitErr(1)
		}
		time.Sleep(2 * time.Second)
	}
}

func envCmd(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sp env list|set|unset SERVICE ...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	deployAfter := fs.Bool("deploy", false, "")
	pos, err := flags(fs, args[1:])
	if err != nil {
		return err
	}
	if err := need(pos, 1, "env "+sub+" SERVICE ..."); err != nil {
		return err
	}
	d, err := c.service(pos[0])
	if err != nil {
		return err
	}
	q := ""
	if *deployAfter {
		q = "?deploy=1"
	}
	switch sub {
	case "list", "ls", "get":
		fmt.Print(d.App.Env)
		return nil
	case "set":
		set := map[string]string{}
		for _, kv := range pos[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("%q: use KEY=VALUE", kv)
			}
			set[k] = v
		}
		if len(set) == 0 {
			return errors.New("usage: sp env set SERVICE KEY=VALUE...")
		}
		return envPatch(c, d.App.ID, q, map[string]any{"set": set})
	case "unset", "rm":
		if len(pos) < 2 {
			return errors.New("usage: sp env unset SERVICE KEY...")
		}
		return envPatch(c, d.App.ID, q, map[string]any{"unset": pos[1:]})
	}
	return fmt.Errorf("unknown env command %q", sub)
}

func envPatch(c *client, id int64, q string, body map[string]any) error {
	var out struct {
		Keys       []string    `json:"keys"`
		Deployment *deployment `json:"deployment"`
	}
	if err := c.do("PATCH", fmt.Sprintf("/apps/%d/env%s", id, q), body, &out); err != nil {
		return err
	}
	sort.Strings(out.Keys)
	fmt.Println("Saved. Variables:", strings.Join(out.Keys, ", "))
	if out.Deployment != nil {
		fmt.Printf("Deployment #%d started\n", out.Deployment.ID)
	} else {
		fmt.Println("Deploy to apply the change (or add --deploy).")
	}
	return nil
}

type cronJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Command    string `json:"command"`
	Enabled    bool   `json:"enabled"`
	NextRunAt  *int64 `json:"nextRunAt"`
	LastStatus string `json:"lastStatus"`
	LastRunAt  *int64 `json:"lastRunAt"`
}

type cronRun struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode"`
	Output   string `json:"output"`
}

func cronCmd(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sp cron list|run SERVICE ...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("cron", flag.ContinueOnError)
	wait := fs.Bool("wait", false, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := flags(fs, args[1:])
	if err != nil {
		return err
	}
	if err := need(pos, 1, "cron "+sub+" SERVICE"); err != nil {
		return err
	}
	d, err := c.service(pos[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do("GET", fmt.Sprintf("/apps/%d/cron", d.App.ID), nil, &raw); err != nil {
		return err
	}
	var jobs []cronJob
	_ = json.Unmarshal(raw, &jobs)
	switch sub {
	case "list", "ls":
		if *asJSON {
			printJSON(raw)
			return nil
		}
		if len(jobs) == 0 {
			fmt.Println("No cron jobs.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tSCHEDULE\tLAST\tNEXT\tCOMMAND")
		for _, j := range jobs {
			next := "off"
			if j.Enabled && j.NextRunAt != nil {
				next = time.Unix(*j.NextRunAt, 0).Format("Jan 2 15:04")
			}
			last := "-"
			if j.LastRunAt != nil {
				last = j.LastStatus + " " + ago(*j.LastRunAt)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Name, j.Schedule, last, next, j.Command)
		}
		return tw.Flush()
	case "run":
		if len(pos) < 2 {
			return errors.New("usage: sp cron run SERVICE JOB [--wait]")
		}
		var job *cronJob
		for i := range jobs {
			if jobs[i].Name == pos[1] || strconv.FormatInt(jobs[i].ID, 10) == pos[1] {
				job = &jobs[i]
			}
		}
		if job == nil {
			return fmt.Errorf("no cron job %q on %s; see `sp cron list %s`", pos[1], pos[0], pos[0])
		}
		var r cronRun
		if err := c.do("POST", fmt.Sprintf("/cron/%d/run", job.ID), map[string]any{}, &r); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Run #%d of %q started\n", r.ID, job.Name)
		if !*wait {
			return nil
		}
		printed := 0
		for {
			time.Sleep(2 * time.Second)
			if err := c.do("GET", fmt.Sprintf("/cron-runs/%d", r.ID), nil, &r); err != nil {
				return err
			}
			if len(r.Output) > printed {
				fmt.Print(r.Output[printed:])
				printed = len(r.Output)
			}
			if r.Status != "running" {
				if r.Status != "succeeded" {
					fmt.Fprintf(os.Stderr, "\nRun #%d %s\n", r.ID, r.Status)
					if r.ExitCode != nil && *r.ExitCode != 0 {
						return exitErr(*r.ExitCode)
					}
					return exitErr(1)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("unknown cron command %q", sub)
}
