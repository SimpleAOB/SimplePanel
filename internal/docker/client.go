// Package docker is a deliberately small client for the Docker Engine API.
// It covers only what SimplePanel needs, which keeps the dependency tree
// tiny compared to the full Docker SDK.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxAPIVersion is the newest API version this client is written against.
// Older daemons are used at their own version via negotiation.
const maxAPIVersion = "1.47"

// Client talks to the Docker Engine over a unix socket.
type Client struct {
	http    *http.Client
	version string
	socket  string

	h     healthState
	slots chan struct{} // limits short calls in flight (see health.go)
}

// ErrNotFound is returned when Docker answers 404.
var ErrNotFound = errors.New("docker: not found")

// APIError carries a non-2xx response from Docker.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

// New returns a client for the socket at path (usually /var/run/docker.sock).
func New(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:    10,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{http: &http.Client{Transport: tr}, version: maxAPIVersion, socket: socketPath, slots: make(chan struct{}, MaxInFlight)}
}

// Negotiate picks the lower of the daemon's API version and ours.
func (c *Client) Negotiate(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: cannot reach daemon: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if v := resp.Header.Get("Api-Version"); v != "" && versionLess(v, maxAPIVersion) {
		c.version = v
	}
	return nil
}

func versionLess(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	if len(pa) != 2 || len(pb) != 2 {
		return false
	}
	ma, _ := strconv.Atoi(pa[0])
	mb, _ := strconv.Atoi(pb[0])
	if ma != mb {
		return ma < mb
	}
	na, _ := strconv.Atoi(pa[1])
	nb, _ := strconv.Atoi(pb[1])
	return na < nb
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, hdr map[string]string) (*http.Response, error) {
	if err := c.availableFor(ctx); err != nil {
		return nil, err
	}
	u := "http://docker/v" + c.version + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if unreachable(err) && ctx.Err() == nil {
			c.markDown(err)
		}
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	return resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, q url.Values, in, out any) error {
	return c.doJSONAuth(ctx, method, path, q, in, out, "")
}

// doJSONAuth is doJSON with an optional X-Registry-Auth header.
func (c *Client) doJSONAuth(ctx context.Context, method, path string, q url.Values, in, out any, registryAuth string) error {
	if isLong(ctx) {
		return c.doJSONRaw(ctx, method, path, q, in, out, registryAuth)
	}
	cctx, done, err := c.shortCall(ctx)
	if err != nil {
		return err
	}
	err = c.doJSONRaw(cctx, method, path, q, in, out, registryAuth)
	done(err)
	return c.wrapUnavailable(ctx, err)
}

func (c *Client) doJSONRaw(ctx context.Context, method, path string, q url.Values, in, out any, registryAuth string) error {
	var body io.Reader
	hdr := map[string]string{}
	if registryAuth != "" {
		hdr["X-Registry-Auth"] = registryAuth
	}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	resp, err := c.do(ctx, method, path, q, body, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- System / Swarm ----

// Info is the subset of /info we use.
type Info struct {
	ServerVersion string
	NCPU          int
	MemTotal      int64
	DockerRootDir string
	Swarm         struct {
		LocalNodeState   string
		ControlAvailable bool
		NodeID           string
	}
}

func (c *Client) Info(ctx context.Context) (*Info, error) {
	var i Info
	return &i, c.doJSON(ctx, http.MethodGet, "/info", nil, nil, &i)
}

// ---- Images ----

// streamMsg is one line of Docker's JSON progress stream.
type streamMsg struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Progress    string `json:"progress"`
	ID          string `json:"id"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

// consumeStream copies human-readable lines to w and returns the first error
// reported in the stream.
func consumeStream(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m streamMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Error != "" {
			return errors.New(strings.TrimSpace(m.Error))
		}
		if w == nil {
			continue
		}
		switch {
		case m.Stream != "":
			io.WriteString(w, m.Stream)
		case m.Status != "" && m.Progress == "":
			if m.ID != "" {
				fmt.Fprintf(w, "%s: %s\n", m.ID, m.Status)
			} else {
				fmt.Fprintln(w, m.Status)
			}
		}
	}
	return sc.Err()
}

// ImagePull pulls ref, writing progress to w. auth may be nil for public
// images.
func (c *Client) ImagePull(ctx context.Context, ref string, auth *RegistryAuth, w io.Writer) error {
	name, tag := SplitTag(ref)
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	var hdr map[string]string
	if auth != nil {
		hdr = map[string]string{"X-Registry-Auth": auth.Encode()}
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return consumeStream(resp.Body, w)
}

// SplitTag splits "repo:tag" (or "repo@digest") into name and tag/digest.
// Registry ports (host:5000/repo) are handled.
func SplitTag(ref string) (string, string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// ImageDigestRef returns "repo@sha256:..." for a pulled image when Docker
// knows its registry digest, so services are pinned to exact content.
func (c *Client) ImageDigestRef(ctx context.Context, ref string) (string, error) {
	var img struct {
		RepoDigests []string
	}
	if err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, &img); err != nil {
		return "", err
	}
	name, _ := SplitTag(ref)
	for _, d := range img.RepoDigests {
		if n, _ := SplitTag(d); n == name || strings.HasSuffix(n, "/"+name) {
			return d, nil
		}
	}
	return ref, nil
}

// BuildOptions configures an image build.
type BuildOptions struct {
	Tag        string
	Dockerfile string
	BuildArgs  map[string]string
	NoPull     bool // use cached base images instead of refreshing them
	// Registries are logins for pulling private base images. Docker sends
	// each one only to its own registry.
	Registries []RegistryAuth
	// MemoryMB caps the memory of the build's containers (0: no cap), so
	// a hungry build can't take the server down with it.
	MemoryMB int
}

// ImageBuild builds an image from a tar build context.
func (c *Client) ImageBuild(ctx context.Context, tarCtx io.Reader, opt BuildOptions, w io.Writer) error {
	q := url.Values{"t": {opt.Tag}, "rm": {"1"}, "forcerm": {"1"}, "pull": {"1"}}
	if opt.NoPull {
		q.Del("pull")
	}
	if opt.Dockerfile != "" {
		q.Set("dockerfile", opt.Dockerfile)
	}
	if opt.MemoryMB > 0 {
		mem := strconv.FormatInt(int64(opt.MemoryMB)<<20, 10)
		q.Set("memory", mem)
		q.Set("memswap", mem) // no swap on top: swapping builds stall the server
	}
	if len(opt.BuildArgs) > 0 {
		b, _ := json.Marshal(opt.BuildArgs)
		q.Set("buildargs", string(b))
	}
	hdr := map[string]string{"Content-Type": "application/x-tar"}
	if len(opt.Registries) > 0 {
		hdr["X-Registry-Config"] = encodeRegistryConfig(opt.Registries)
	}
	resp, err := c.do(ctx, http.MethodPost, "/build", q, tarCtx, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return consumeStream(resp.Body, w)
}

// ---- Networks ----

func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	err := c.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// NetworkEnsure creates a swarm-scoped overlay network if it doesn't exist.
func (c *Client) NetworkEnsure(ctx context.Context, name string, labels map[string]string) error {
	ok, err := c.NetworkExists(ctx, name)
	if err != nil || ok {
		return err
	}
	body := map[string]any{"Name": name, "Driver": "overlay", "Attachable": false, "Labels": labels}
	err = c.doJSON(ctx, http.MethodPost, "/networks/create", nil, body, nil)
	if err != nil {
		// Another caller may have created it concurrently.
		if ok, _ := c.NetworkExists(ctx, name); ok {
			return nil
		}
	}
	return err
}

// NetworkRemove deletes a network; missing networks are not an error.
func (c *Client) NetworkRemove(ctx context.Context, name string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// VolumeRemove deletes a named volume; a missing volume is not an error.
func (c *Client) VolumeRemove(ctx context.Context, name string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ---- Services ----

type Service struct {
	ID           string
	Version      struct{ Index uint64 }
	Spec         ServiceSpec
	UpdateStatus *struct {
		State   string
		Message string
	}
}

type ServiceSpec struct {
	Name           string            `json:"Name"`
	Labels         map[string]string `json:"Labels,omitempty"`
	TaskTemplate   TaskSpec          `json:"TaskTemplate"`
	Mode           ServiceMode       `json:"Mode"`
	UpdateConfig   *UpdateConfig     `json:"UpdateConfig,omitempty"`
	RollbackConfig *UpdateConfig     `json:"RollbackConfig,omitempty"`
	EndpointSpec   *EndpointSpec     `json:"EndpointSpec,omitempty"`
}

type TaskSpec struct {
	ContainerSpec ContainerSpec   `json:"ContainerSpec"`
	Resources     *Resources      `json:"Resources,omitempty"`
	RestartPolicy *RestartPolicy  `json:"RestartPolicy,omitempty"`
	Placement     *Placement      `json:"Placement,omitempty"`
	Networks      []NetworkAttach `json:"Networks,omitempty"`
	ForceUpdate   uint64          `json:"ForceUpdate,omitempty"`
	LogDriver     *LogDriver      `json:"LogDriver,omitempty"`
}

type ContainerSpec struct {
	Image       string            `json:"Image"`
	Labels      map[string]string `json:"Labels,omitempty"`
	Args        []string          `json:"Args,omitempty"`
	Env         []string          `json:"Env,omitempty"`
	Mounts      []Mount           `json:"Mounts,omitempty"`
	Healthcheck *Healthcheck      `json:"Healthcheck,omitempty"`
}

// Healthcheck overrides the image's HEALTHCHECK; Test ["NONE"] turns it off.
type Healthcheck struct {
	Test []string `json:"Test,omitempty"`
}

type Mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

type Resources struct {
	Limits *Limits `json:"Limits,omitempty"`
}

type Limits struct {
	NanoCPUs    int64 `json:"NanoCPUs,omitempty"`
	MemoryBytes int64 `json:"MemoryBytes,omitempty"`
}

type RestartPolicy struct {
	Condition   string `json:"Condition"`
	Delay       int64  `json:"Delay,omitempty"`
	MaxAttempts uint64 `json:"MaxAttempts,omitempty"`
}

type Placement struct {
	Constraints []string `json:"Constraints,omitempty"`
}

type NetworkAttach struct {
	Target  string   `json:"Target"`
	Aliases []string `json:"Aliases,omitempty"`
}

type LogDriver struct {
	Name    string            `json:"Name"`
	Options map[string]string `json:"Options,omitempty"`
}

type ServiceMode struct {
	Replicated *Replicated `json:"Replicated,omitempty"`
}

type Replicated struct {
	Replicas uint64 `json:"Replicas"`
}

type UpdateConfig struct {
	Parallelism   uint64 `json:"Parallelism"`
	Delay         int64  `json:"Delay,omitempty"`
	FailureAction string `json:"FailureAction,omitempty"`
	Monitor       int64  `json:"Monitor,omitempty"`
	Order         string `json:"Order,omitempty"`
}

type EndpointSpec struct {
	Mode  string     `json:"Mode,omitempty"`
	Ports []PortSpec `json:"Ports,omitempty"`
}

type PortSpec struct {
	Protocol      string `json:"Protocol"`
	TargetPort    uint32 `json:"TargetPort"`
	PublishedPort uint32 `json:"PublishedPort"`
	PublishMode   string `json:"PublishMode,omitempty"`
}

func (c *Client) ServiceInspect(ctx context.Context, name string) (*Service, error) {
	var s Service
	if err := c.doJSON(ctx, http.MethodGet, "/services/"+url.PathEscape(name), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ServiceCreate creates a service. auth is the registry login Swarm hands to
// every node that runs a task, so they can pull a private image; nil
// means no login.
func (c *Client) ServiceCreate(ctx context.Context, spec ServiceSpec, auth *RegistryAuth) (string, error) {
	var out struct{ ID string }
	err := c.doJSONAuth(ctx, http.MethodPost, "/services/create", nil, spec, &out, serviceAuth(auth))
	return out.ID, err
}

// ServiceUpdate updates a service. With keepAuth, Swarm keeps the registry
// login the service already has (used for restarts and scaling); otherwise
// auth replaces it, and nil removes it.
func (c *Client) ServiceUpdate(ctx context.Context, id string, version uint64, spec ServiceSpec, auth *RegistryAuth, keepAuth bool) error {
	q := url.Values{"version": {strconv.FormatUint(version, 10)}}
	hdr := ""
	if !keepAuth {
		hdr = serviceAuth(auth)
	}
	return c.doJSONAuth(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/update", q, spec, nil, hdr)
}

// serviceAuth is the X-Registry-Auth header for a service. Without a login
// it is an empty login ("{}"), which clears one the service had before:
// leaving the header out would make Swarm keep the old one.
func serviceAuth(auth *RegistryAuth) string {
	if auth == nil {
		return base64.URLEncoding.EncodeToString([]byte("{}"))
	}
	return auth.Encode()
}

func (c *Client) ServiceRemove(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/services/"+url.PathEscape(id), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Task is the subset of a Swarm task we show in the UI.
type Task struct {
	ID           string
	ServiceID    string
	CreatedAt    time.Time
	Slot         int
	DesiredState string
	Status       struct {
		Timestamp       time.Time
		State           string
		Message         string
		Err             string
		ContainerStatus struct {
			ContainerID string
			ExitCode    int
		}
	}
	Spec struct {
		ContainerSpec struct{ Image string }
	}
}

// Services lists every Swarm service, SimplePanel's or not.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	var out []Service
	err := c.doJSON(ctx, http.MethodGet, "/services", nil, nil, &out)
	return out, err
}

// ServicesByLabel lists services carrying label ("key" or "key=value") in
// one call.
func (c *Client) ServicesByLabel(ctx context.Context, label string) ([]Service, error) {
	f, _ := json.Marshal(map[string][]string{"label": {label}})
	var out []Service
	err := c.doJSON(ctx, http.MethodGet, "/services", url.Values{"filters": {string(f)}}, nil, &out)
	return out, err
}

// TasksOf lists the tasks of several services in one call.
func (c *Client) TasksOf(ctx context.Context, services []string) ([]Task, error) {
	if len(services) == 0 {
		return nil, nil
	}
	f, _ := json.Marshal(map[string][]string{"service": services})
	var tasks []Task
	err := c.doJSON(ctx, http.MethodGet, "/tasks", url.Values{"filters": {string(f)}}, nil, &tasks)
	return tasks, err
}

func (c *Client) ServiceTasks(ctx context.Context, service string) ([]Task, error) {
	f, _ := json.Marshal(map[string][]string{"service": {service}})
	var tasks []Task
	err := c.doJSON(ctx, http.MethodGet, "/tasks", url.Values{"filters": {string(f)}}, nil, &tasks)
	return tasks, err
}

// ServiceLogs returns the last `tail` lines of a service's logs, demultiplexed.
// A non-zero since (Unix seconds) only returns lines logged after it.
func (c *Client) ServiceLogs(ctx context.Context, service string, tail int, since int64) (string, error) {
	return c.logs(ctx, "/services/"+url.PathEscape(service)+"/logs", tail, since, 8<<20)
}

// TaskLogs returns the logs of one Swarm task (one container start). A tail
// of 0 or less returns every line Docker still has, up to limit bytes.
func (c *Client) TaskLogs(ctx context.Context, taskID string, tail int, since int64, limit int64) (string, error) {
	return c.logs(ctx, "/tasks/"+url.PathEscape(taskID)+"/logs", tail, since, limit)
}

func (c *Client) logs(ctx context.Context, path string, tail int, since int64, limit int64) (string, error) {
	t := "all"
	if tail > 0 {
		t = strconv.Itoa(tail)
	}
	q := url.Values{
		"stdout":     {"1"},
		"stderr":     {"1"},
		"timestamps": {"1"},
		"tail":       {t},
	}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	// Reading logs is a short call too: don't wait forever on a slow daemon.
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, path, q, nil, nil)
	if err != nil {
		return "", c.wrapUnavailable(ctx, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	err = demux(io.LimitReader(resp.Body, limit), &buf)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return sortByTimestamp(buf.String()), err
}

// sortByTimestamp orders log lines from several tasks chronologically. Each
// line starts with an RFC 3339 timestamp, which sorts lexically; lines that
// don't (continuations) stay attached to the line before them.
func sortByTimestamp(s string) string {
	lines := strings.SplitAfter(s, "\n")
	type entry struct{ ts, text string }
	var entries []entry
	for _, l := range lines {
		if l == "" {
			continue
		}
		ts, _, _ := strings.Cut(l, " ")
		if len(ts) >= 20 && ts[4] == '-' && ts[10] == 'T' {
			entries = append(entries, entry{ts, l})
		} else if len(entries) > 0 {
			entries[len(entries)-1].text += l
		} else {
			entries = append(entries, entry{"", l})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.text)
	}
	return b.String()
}

// demux decodes Docker's multiplexed stdout/stderr stream format.
// Exec runs cmd in a running container and returns its stdout, stderr and
// exit code. Output beyond limit bytes per stream is dropped.
func (c *Client) Exec(ctx context.Context, containerID string, cmd, env []string, limit int64) (stdout, stderr string, code int, err error) {
	var created struct{ Id string }
	err = c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/exec", nil, map[string]any{
		"AttachStdout": true, "AttachStderr": true, "Cmd": cmd, "Env": env,
	}, &created)
	if err != nil {
		return "", "", -1, err
	}
	b, _ := json.Marshal(map[string]bool{"Detach": false, "Tty": false})
	resp, err := c.do(ctx, http.MethodPost, "/exec/"+created.Id+"/start", nil, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", "", -1, err
	}
	var out, errb limitedBuffer
	out.limit, errb.limit = limit, limit
	err = demuxSplit(resp.Body, &out, &errb)
	resp.Body.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return out.String(), errb.String(), -1, err
	}
	var info struct {
		ExitCode int
		Running  bool
	}
	if err := c.doJSON(ctx, http.MethodGet, "/exec/"+created.Id+"/json", nil, nil, &info); err != nil {
		return out.String(), errb.String(), -1, err
	}
	return out.String(), errb.String(), info.ExitCode, nil
}

// limitedBuffer keeps the first limit bytes written and discards the rest,
// so a huge output can't exhaust memory.
type limitedBuffer struct {
	bytes.Buffer
	limit     int64
	Truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - int64(b.Len()); room < int64(len(p)) {
		b.Truncated = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// demuxSplit is demux with stdout and stderr kept apart.
func demuxSplit(r io.Reader, stdout, stderr io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		w := stdout
		if hdr[0] == 2 {
			w = stderr
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(w, r, n); err != nil {
			return err
		}
	}
}

func demux(r io.Reader, w io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if hdr[0] > 2 || hdr[1] != 0 {
			// Not multiplexed (TTY service): pass through the rest.
			w.Write(hdr)
			_, err := io.Copy(w, r)
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(w, r, n); err != nil {
			return err
		}
	}
}

// ---- Self-update helpers ----
// The panel updates its own service with the raw spec, so fields this
// client doesn't model (health checks, mount options, ...) survive.

// RawService is a service with its spec left as raw JSON.
type RawService struct {
	ID           string
	Version      struct{ Index uint64 }
	Spec         json.RawMessage
	UpdateStatus *struct {
		State   string
		Message string
	}
}

func (c *Client) ServiceInspectRaw(ctx context.Context, name string) (*RawService, error) {
	var s RawService
	if err := c.doJSON(ctx, http.MethodGet, "/services/"+url.PathEscape(name), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ServiceUpdateRaw sends a raw spec. Swarm keeps the previous spec, so a
// failed update can roll back to it.
func (c *Client) ServiceUpdateRaw(ctx context.Context, id string, version uint64, spec json.RawMessage) error {
	q := url.Values{"version": {strconv.FormatUint(version, 10)}}
	return c.doJSON(ctx, http.MethodPost, "/services/"+url.PathEscape(id)+"/update", q, spec, nil)
}

// ImageLabels returns an image's labels (OCI annotations such as
// org.opencontainers.image.version).
func (c *Client) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	var img struct {
		Config struct {
			Labels map[string]string
		}
	}
	if err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, &img); err != nil {
		return nil, err
	}
	return img.Config.Labels, nil
}

// ImageExposedPorts returns the TCP ports an image declares with EXPOSE,
// sorted ascending.
func (c *Client) ImageExposedPorts(ctx context.Context, ref string) ([]int, error) {
	var img struct {
		Config struct {
			ExposedPorts map[string]struct{}
		}
	}
	if err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, &img); err != nil {
		return nil, err
	}
	return ParseExposedPorts(img.Config.ExposedPorts), nil
}

// ParseExposedPorts turns Docker's {"8080/tcp":{}} map into sorted TCP ports.
func ParseExposedPorts(m map[string]struct{}) []int {
	var out []int
	for k := range m {
		p, proto, _ := strings.Cut(k, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
