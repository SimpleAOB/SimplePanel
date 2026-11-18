package api

// Built-in MCP (Model Context Protocol) server, so AI assistants such as
// Claude Code can view and manage SimplePanel.
//
// Transport: Streamable HTTP at POST /mcp. Every request is a single
// JSON-RPC message answered with a single JSON response; the server never
// opens an SSE stream, which the spec allows. Clients authenticate with an
// API token created in Settings, sent as "Authorization: Bearer sp_...".
//
// Each token has a scope (read, manage or full) and only sees the tools it
// may use. Tools reuse the same validation as the web UI, secrets are left
// out of every answer unless a full-scope token explicitly asks for them,
// and every change is written to the audit log.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// mcpProtocolVersions are the MCP revisions this server speaks, newest first.
var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const mcpInstructions = `SimplePanel runs apps and databases as Docker Swarm services on this server.
Projects group services; services in a project share a private network and reach each other by name (e.g. "db").
Refer to a service as "project/name" (or its numeric id) and to a project by name or id.
Typical flow: list_projects -> get_service -> change it (update_service, set_env, add_domain) -> deploy with wait=true -> get_logs if it fails.
Config changes only take effect after deploy.
Secrets are never available through this connection: environment variable values, database passwords, access tokens and webhook URLs. If you need one (for example to put a database URL into an app's environment), ask the user to give it to you or to paste it into SimplePanel themselves.
Databases can be queried read-only with db_query, db_list_tables and db_describe_table, but only where the user turned on "Allow AI read-only access" on the database's page. Call get_permissions to see which ones.`

type jsonrpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcp handles POST /mcp.
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		// No server-initiated SSE stream and no session to delete.
		h.Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	// DNS rebinding protection: a browser page on another origin must not
	// be able to talk to the endpoint.
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			writeErr(w, http.StatusForbidden, "origin not allowed")
			return
		}
	}
	ip := clientIP(r)
	if s.mcpAuthLimit.Blocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	tok, user, err := s.mcpToken(r)
	if err != nil {
		s.mcpAuthLimit.Allow(ip)
		h.Set("WWW-Authenticate", `Bearer realm="SimplePanel"`)
		writeErr(w, http.StatusUnauthorized, "a valid API token is required (Settings > Connect an AI)")
		return
	}

	var msg jsonrpcMsg
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&msg); err != nil {
		writeRPC(w, nil, nil, &jsonrpcErr{-32700, "parse error: send one JSON-RPC message per request"})
		return
	}
	if msg.JSONRPC != "2.0" || msg.Method == "" {
		if msg.Method == "" && len(msg.ID) > 0 {
			w.WriteHeader(http.StatusAccepted) // a response to us; we never ask anything
			return
		}
		writeRPC(w, msg.ID, nil, &jsonrpcErr{-32600, "invalid request"})
		return
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		w.WriteHeader(http.StatusAccepted) // notification, e.g. notifications/initialized
		return
	}

	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		v := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		writeRPC(w, msg.ID, map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "simplepanel", "title": "SimplePanel", "version": s.version},
			"instructions":    mcpInstructions,
		}, nil)
	case "ping":
		writeRPC(w, msg.ID, map[string]any{}, nil)
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range mcpTools {
			if store.ScopeRank(tok.Scope) >= store.ScopeRank(t.scope) {
				tools = append(tools, t.describe())
			}
		}
		writeRPC(w, msg.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			writeRPC(w, msg.ID, nil, &jsonrpcErr{-32602, "invalid params"})
			return
		}
		t := mcpToolByName(p.Name)
		if t == nil {
			writeRPC(w, msg.ID, nil, &jsonrpcErr{-32602, "unknown tool: " + p.Name})
			return
		}
		writeRPC(w, msg.ID, s.callTool(r.Context(), tok, user, t, p.Arguments), nil)
	default:
		writeRPC(w, msg.ID, nil, &jsonrpcErr{-32601, "method not found: " + msg.Method})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *jsonrpcErr) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

// mcpToken authenticates the request and returns the token and the user it
// acts for, with the token's scope applied.
func (s *Server) mcpToken(r *http.Request) (*store.APIToken, *store.User, error) {
	auth := r.Header.Get("Authorization")
	secret, ok := strings.CutPrefix(auth, "Bearer ")
	secret = strings.TrimSpace(secret)
	if !ok || !strings.HasPrefix(secret, store.TokenPrefix) || len(secret) > 200 {
		return nil, nil, errors.New("no token")
	}
	return s.store.APITokenBySecret(r.Context(), secret)
}

// ---- tool plumbing ----

type mcpTool struct {
	name        string
	title       string
	description string
	scope       string
	readOnly    bool
	destructive bool
	audit       bool // record calls in the audit log
	props       map[string]any
	required    []string
	run         func(c *mcpCall) (any, error)
}

func (t *mcpTool) describe() map[string]any {
	schema := map[string]any{"type": "object", "properties": t.props, "additionalProperties": false}
	if t.props == nil {
		schema["properties"] = map[string]any{}
	}
	if len(t.required) > 0 {
		schema["required"] = t.required
	}
	return map[string]any{
		"name":        t.name,
		"title":       t.title,
		"description": t.description,
		"inputSchema": schema,
		"annotations": map[string]any{
			"title":           t.title,
			"readOnlyHint":    t.readOnly,
			"destructiveHint": t.destructive,
			"idempotentHint":  t.readOnly,
			"openWorldHint":   false,
		},
	}
}

func mcpToolByName(name string) *mcpTool {
	for i := range mcpTools {
		if mcpTools[i].name == name {
			return &mcpTools[i]
		}
	}
	return nil
}

// mcpCall is one tool invocation.
type mcpCall struct {
	s      *Server
	ctx    context.Context
	tok    *store.APIToken
	user   *store.User // the token's owner, with the token's scope applied
	tool   *mcpTool
	args   json.RawMessage
	target string // what the call acted on, for the audit log
}

// need is the project access level the current tool requires.
func (c *mcpCall) need() int { return store.ScopeLevel(c.tool.scope) }

// checkProject verifies the user may use the current tool on a project.
// Projects the user can't see are reported as not found.
func (c *mcpCall) checkProject(projectID int64, ref string) error {
	l := c.s.level(c.ctx, c.user, projectID)
	if l == store.LevelNone {
		return fmt.Errorf("%q not found; call list_projects to see them", ref)
	}
	if l < c.need() {
		return fmt.Errorf("your SimplePanel role (%s) can't use %s on %s; ask an admin", c.user.Role, c.tool.name, ref)
	}
	return nil
}

// bind decodes the arguments into v, rejecting unknown fields.
func (c *mcpCall) bind(v any) error {
	if len(c.args) == 0 || string(c.args) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(c.args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func (c *mcpCall) full() bool {
	return store.ScopeRank(c.tok.Scope) >= store.ScopeRank(store.ScopeFull)
}

func (s *Server) callTool(ctx context.Context, tok *store.APIToken, user *store.User, t *mcpTool, args json.RawMessage) map[string]any {
	c := &mcpCall{s: s, ctx: ctx, tok: tok, user: user, tool: t, args: args}
	var (
		res any
		err error
	)
	if store.ScopeRank(tok.Scope) < store.ScopeRank(t.scope) {
		err = fmt.Errorf("this token has %s scope; %s needs %s scope. Create a token with more access in SimplePanel's Settings", tok.Scope, t.name, t.scope)
	} else {
		res, err = t.run(c)
	}
	if t.audit {
		if c.target == "" { // failed before resolving: name what was asked for
			var ref struct{ Service, Project string }
			_ = json.Unmarshal(args, &ref)
			c.target = cmpOr(ref.Service, ref.Project)
		}
		e := &store.AuditEntry{Source: "mcp", TokenID: &tok.ID, TokenName: tok.Name, UserID: &user.ID, Username: user.Username,
			Action: t.name, Target: c.target, Args: redactArgs(args), OK: err == nil}
		if err != nil {
			e.Error = truncate(err.Error(), 500)
		}
		if aerr := s.store.AddAudit(context.WithoutCancel(ctx), e); aerr != nil {
			s.log.Error("audit log", "err", aerr)
		}
	}
	if err != nil {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
	}
	text, ok := res.(string)
	if !ok {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		text = strings.TrimSpace(b.String())
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

// redactArgs keeps the shape of a call's arguments for the audit log but
// drops secret values: environment values and access tokens.
func redactArgs(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for k, v := range m {
		switch k {
		case "env", "set":
			if obj, ok := v.(map[string]any); ok {
				for ek := range obj {
					obj[ek] = "•••"
				}
			} else if v != nil {
				m[k] = "•••"
			}
		case "git_token", "password":
			m[k] = "•••"
		case "document":
			m[k] = "(" + strconv.Itoa(len(fmt.Sprint(v))) + " characters)"
		}
	}
	b, _ := json.Marshal(m)
	return truncate(string(b), 2000)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// tailText keeps at most the last maxLines lines and maxBytes bytes of s.
func tailText(s string, maxLines, maxBytes int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		s = "[… earlier lines omitted]\n" + strings.Join(lines, "\n")
	}
	if len(s) > maxBytes {
		s = "[… earlier output omitted]\n" + s[len(s)-maxBytes:]
	}
	return s
}

// invoke runs one of the web API's handlers in-process, so MCP tools get
// exactly the same validation and side effects as the UI.
func (c *mcpCall) invoke(h func(http.ResponseWriter, *http.Request, *store.User), method, target string, pathValues map[string]string, body any) (json.RawMessage, error) {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequestWithContext(c.ctx, method, target, rd)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req, c.user)
	out := rec.Body.Bytes()
	if rec.Code >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(out, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("request failed (%d)", rec.Code)
	}
	return out, nil
}

// ---- lookups ----

func (c *mcpCall) project(ref string) (*store.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("project is required")
	}
	var p *store.Project
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if p, err = c.s.store.Project(c.ctx, id); err != nil {
			return nil, fmt.Errorf("project %s not found", ref)
		}
	} else {
		ps, err := c.s.store.Projects(c.ctx)
		if err != nil {
			return nil, err
		}
		for i := range ps {
			if ps[i].Name == strings.ToLower(ref) {
				p = &ps[i]
			}
		}
	}
	if p == nil {
		return nil, fmt.Errorf("project %q not found; call list_projects to see them", ref)
	}
	if err := c.checkProject(p.ID, p.Name); err != nil {
		return nil, err
	}
	return p, nil
}

// service resolves "project/name", a numeric id, or a bare name that is
// unique across projects.
func (c *mcpCall) service(ref string) (*store.App, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("service is required")
	}
	var a *store.App
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if a, err = c.s.store.App(c.ctx, id); err != nil {
			return nil, fmt.Errorf("service %s not found", ref)
		}
	} else if pname, name, ok := strings.Cut(ref, "/"); ok {
		ps, err := c.s.store.Projects(c.ctx)
		if err != nil {
			return nil, err
		}
		var p *store.Project
		for i := range ps {
			if ps[i].Name == strings.ToLower(pname) {
				p = &ps[i]
			}
		}
		if p == nil {
			return nil, fmt.Errorf("service %q not found; call list_projects to see them", ref)
		}
		apps, err := c.s.store.Apps(c.ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for i := range apps {
			if apps[i].Name == strings.ToLower(name) {
				a = &apps[i]
			}
		}
	} else {
		ps, err := c.s.store.Projects(c.ctx)
		if err != nil {
			return nil, err
		}
		var found []store.App
		for _, p := range ps {
			apps, err := c.s.store.Apps(c.ctx, p.ID)
			if err != nil {
				return nil, err
			}
			for _, x := range apps {
				if x.Name == strings.ToLower(ref) && c.s.level(c.ctx, c.user, x.ProjectID) > store.LevelNone {
					found = append(found, x)
				}
			}
		}
		if len(found) > 1 {
			return nil, fmt.Errorf("more than one service is called %q; use project/name", ref)
		}
		if len(found) == 1 {
			a = &found[0]
		}
	}
	if a == nil {
		return nil, fmt.Errorf("service %q not found; call list_projects to see them", ref)
	}
	if err := c.checkProject(a.ProjectID, ref); err != nil {
		return nil, err
	}
	c.target = a.ProjectName + "/" + a.Name
	return a, nil
}

// aiDatabase resolves a database that allows AI read-only queries.
func (c *mcpCall) aiDatabase(ref string) (*store.App, string, error) {
	app, err := c.service(ref)
	if err != nil {
		return nil, "", err
	}
	if !app.IsDatabase() {
		return nil, "", fmt.Errorf("%s is an app, not a database", c.target)
	}
	if e := deploy.EngineByKey(app.Kind); e == nil || !e.AIAccess {
		return nil, "", fmt.Errorf("%s is a %s database; AI read-only queries only work with PostgreSQL, MySQL and MariaDB", c.target, app.Kind)
	}
	on, pw, err := c.s.store.AIAccess(c.ctx, app.ID)
	if err != nil {
		return nil, "", err
	}
	if !on || pw == "" {
		return nil, "", fmt.Errorf("AI read-only access is off for %s. Ask the user to open this database in SimplePanel and turn on \"Allow AI read-only access\", then try again", c.target)
	}
	return app, pw, nil
}

// ---- views ----

func envKeys(env string) []string {
	lines, _ := deploy.ParseEnv(env)
	keys := []string{}
	for _, l := range lines {
		k, _, _ := strings.Cut(l, "=")
		keys = append(keys, k)
	}
	return keys
}

func (c *mcpCall) serviceSummary(a *store.App) map[string]any {
	st := c.s.deploy.Status(c.ctx, a)
	out := map[string]any{
		"id": a.ID, "ref": a.ProjectName + "/" + a.Name, "type": a.Kind, "status": st.State,
		"running": fmt.Sprintf("%d/%d", st.Running, st.Desired),
	}
	if a.Source == "git" {
		out["source"] = a.GitURL
	} else {
		out["source"] = a.Image
	}
	if ds, err := c.s.store.Domains(c.ctx, a.ID); err == nil && len(ds) > 0 {
		hosts := []string{}
		for _, d := range ds {
			hosts = append(hosts, d.Host)
		}
		out["domains"] = hosts
	}
	if st.Message != "" {
		out["message"] = st.Message
	}
	if a.IsDatabase() {
		on, _, _ := c.s.store.AIAccess(c.ctx, a.ID)
		out["aiReadAccess"] = on
	}
	return out
}

// dbConnection is a database's connection info without any password.
func (c *mcpCall) dbConnection(a *store.App) map[string]any {
	info := deploy.DatabaseInfo(a)
	if info == nil {
		return nil
	}
	on, _, _ := c.s.store.AIAccess(c.ctx, a.ID)
	return map[string]any{
		"engine": info.Engine, "host": a.Name, "fullHost": info.Host, "port": info.Port,
		"user": info.User, "database": info.Database,
		"urlTemplate":   info.URLTemplate(a.Name),
		"password":      "Not available to AI. Ask the user: it is on the database's page in SimplePanel.",
		"aiReadAccess":  on,
		"reachableFrom": "Only apps in project " + a.ProjectName + ".",
	}
}

// ---- tool definitions ----

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

var (
	pService = prop("string", `Service as "project/name" or numeric id.`)
	pProject = prop("string", "Project name or numeric id.")
)

// appFieldProps are the settings shared by create_app and update_service.
var appFieldProps = map[string]any{
	"source":        enumProp("Where the code comes from.", "image", "git"),
	"image":         prop("string", "Docker image for source=image, e.g. nginx:1.27 or ghcr.io/org/app:tag."),
	"git_url":       prop("string", "HTTPS repository URL for source=git."),
	"git_branch":    prop("string", "Branch to build. Empty means the default branch."),
	"git_token":     prop("string", "Access token for a private repository. Write-only."),
	"build_type":    enumProp("auto uses the Dockerfile if there is one, otherwise builds a static site.", "auto", "dockerfile", "static"),
	"build_path":    prop("string", "Folder in the repository to build from. Empty is the root."),
	"dockerfile":    prop("string", "Dockerfile path relative to build_path."),
	"build_command": prop("string", "Static sites: command that builds the site, e.g. npm run build."),
	"output_dir":    prop("string", "Static sites: folder the build writes to, e.g. dist."),
	"build_image":   prop("string", "Static sites: image the build runs in. Default node:22-alpine."),
	"spa":           prop("boolean", "Static sites: send unknown paths to index.html."),
	"replicas":      prop("integer", "Number of containers, 0-50. Databases always run 1."),
	"cpu_limit":     prop("number", "CPU cores limit, 0 for none."),
	"memory_mb":     prop("integer", "Memory limit in MB, 0 for none."),
	"command":       prop("string", "Override the image's command. Empty uses the default."),
	"auto_domain":   prop("boolean", "Give the app an automatic subdomain (name-project.<wildcard domain>) when a wildcard domain is set. Default true for apps."),
	"http_port":     prop("integer", "Container port the automatic subdomain sends traffic to. 0 uses the first domain's port, or 80."),
	"volumes": map[string]any{
		"type": "array", "description": "Named volumes that keep data across deploys. Replaces the whole list.",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name": prop("string", "Volume name."), "path": prop("string", "Mount path in the container."),
		}, "required": []string{"name", "path"}},
	},
	"ports": map[string]any{
		"type": "array", "description": "Ports published on the server, for non-HTTP services. Replaces the whole list.",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"published": prop("integer", "Port on the server."), "target": prop("integer", "Port in the container."),
			"protocol": enumProp("Protocol.", "tcp", "udp"),
		}, "required": []string{"published", "target"}},
	},
}

// appFields mirrors appFieldProps.
type appFields struct {
	Source       *string        `json:"source"`
	Image        *string        `json:"image"`
	GitURL       *string        `json:"git_url"`
	GitBranch    *string        `json:"git_branch"`
	GitToken     *string        `json:"git_token"`
	BuildType    *string        `json:"build_type"`
	BuildPath    *string        `json:"build_path"`
	Dockerfile   *string        `json:"dockerfile"`
	BuildCommand *string        `json:"build_command"`
	OutputDir    *string        `json:"output_dir"`
	BuildImage   *string        `json:"build_image"`
	SPA          *bool          `json:"spa"`
	Replicas     *int           `json:"replicas"`
	CPULimit     *float64       `json:"cpu_limit"`
	MemoryMB     *int           `json:"memory_mb"`
	Command      *string        `json:"command"`
	Volumes      []store.Volume `json:"volumes"`
	Ports        []store.Port   `json:"ports"`
	AutoDomain   *bool          `json:"auto_domain"`
	HTTPPort     *int           `json:"http_port"`
}

// inputFrom starts an appInput from the app's current settings, then
// overlays the fields that were given.
func inputFrom(a *store.App, f *appFields) appInput {
	rep := a.Replicas
	in := appInput{
		Name: a.Name, Source: a.Source, Image: a.Image, GitURL: a.GitURL, GitBranch: a.GitBranch,
		BuildPath: a.BuildPath, Dockerfile: a.Dockerfile, BuildType: a.BuildType, BuildImage: a.BuildImage,
		BuildCommand: a.BuildCommand, OutputDir: a.OutputDir, SPA: a.SPA, Env: a.Env, Replicas: &rep,
		CPULimit: a.CPULimit, MemoryMB: a.MemoryMB, Command: a.Command, Volumes: a.Volumes, Ports: a.Ports,
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&in.Source, f.Source)
	set(&in.Image, f.Image)
	set(&in.GitURL, f.GitURL)
	set(&in.GitBranch, f.GitBranch)
	set(&in.GitToken, f.GitToken)
	set(&in.BuildType, f.BuildType)
	set(&in.BuildPath, f.BuildPath)
	set(&in.Dockerfile, f.Dockerfile)
	set(&in.BuildCommand, f.BuildCommand)
	set(&in.OutputDir, f.OutputDir)
	set(&in.BuildImage, f.BuildImage)
	set(&in.Command, f.Command)
	if f.SPA != nil {
		in.SPA = *f.SPA
	}
	if f.Replicas != nil {
		in.Replicas = f.Replicas
	}
	if f.CPULimit != nil {
		in.CPULimit = *f.CPULimit
	}
	if f.MemoryMB != nil {
		in.MemoryMB = *f.MemoryMB
	}
	if f.Volumes != nil {
		in.Volumes = f.Volumes
	}
	if f.Ports != nil {
		in.Ports = f.Ports
	}
	in.AutoDomain, in.HTTPPort = f.AutoDomain, f.HTTPPort
	return in
}

// envText turns a map into KEY=VALUE lines, sorted for stable output.
func envText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "\n")
	}
	return b.String()
}

// parseSince accepts a duration ("15m", "2h", "3d") or an RFC 3339 time.
func parseSince(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix(), nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days > 0 {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix(), nil
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, errors.New(`since must be a duration like "15m", "2h" or "1d", or an RFC 3339 time`)
	}
	return time.Now().Add(-d).Unix(), nil
}

// waitDeployment polls a deployment until it finishes or the timeout ends.
func (c *mcpCall) waitDeployment(id int64, timeout time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	for {
		d, err := c.s.store.Deployment(c.ctx, id)
		if err != nil {
			return nil, err
		}
		if d.Status == "succeeded" || d.Status == "failed" {
			out := map[string]any{"deploymentId": d.ID, "status": d.Status, "image": d.Image}
			if d.Status == "failed" {
				out["log"] = tailText(d.Log, 60, 16<<10)
				out["hint"] = "Call get_deployment for the full build log, or get_logs for the app's own output."
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return map[string]any{"deploymentId": d.ID, "status": d.Status,
				"hint": "Still running. Call get_deployment later to check on it."}, nil
		case <-time.After(2 * time.Second):
		}
	}
}

var mcpTools []mcpTool

func init() {
	mcpTools = []mcpTool{
		// ---- read ----
		{
			name: "list_projects", title: "List projects and services", scope: store.ScopeRead, readOnly: true,
			description: "Lists every project with its services (apps and databases), their status and domains. Start here.",
			run: func(c *mcpCall) (any, error) {
				ps, err := c.s.visibleProjects(c.ctx, c.user)
				if err != nil {
					return nil, err
				}
				out := []map[string]any{}
				for _, p := range ps {
					apps, err := c.s.store.Apps(c.ctx, p.ID)
					if err != nil {
						return nil, err
					}
					svcs := []map[string]any{}
					for i := range apps {
						svcs = append(svcs, c.serviceSummary(&apps[i]))
					}
					out = append(out, map[string]any{"id": p.ID, "name": p.Name, "services": svcs})
				}
				return map[string]any{"projects": out}, nil
			},
		},
		{
			name: "get_service", title: "Get a service", scope: store.ScopeRead, readOnly: true,
			description: "Shows a service's configuration, status, domains and environment variable names. Values of environment variables and database passwords are never shown: ask the user for them if you need them.",
			props:       map[string]any{"service": pService},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				domains, err := c.s.store.Domains(c.ctx, app.ID)
				if err != nil {
					return nil, err
				}
				cfg := map[string]any{"source": app.Source, "replicas": app.Replicas, "cpuLimit": app.CPULimit, "memoryMb": app.MemoryMB,
					"command": app.Command, "volumes": app.Volumes, "ports": app.Ports}
				if !app.IsDatabase() {
					cfg["autoDomain"] = app.AutoDomain
					if wd, _ := c.s.store.Setting(c.ctx, "wildcard_domain"); wd != "" {
						if h := deploy.AutoHost(app, wd); h != "" {
							cfg["autoHost"] = h
						}
					}
				}
				if app.Source == "image" {
					cfg["image"] = app.Image
				} else {
					cfg["gitUrl"], cfg["gitBranch"], cfg["privateRepo"] = app.GitURL, app.GitBranch, app.HasGitToken
					cfg["buildType"], cfg["buildPath"], cfg["dockerfile"] = app.BuildType, app.BuildPath, app.Dockerfile
					cfg["buildCommand"], cfg["outputDir"], cfg["buildImage"], cfg["spa"] = app.BuildCommand, app.OutputDir, app.BuildImage, app.SPA
					if app.GitRepo != "" {
						cfg["gitRepo"], cfg["autoDeploy"] = app.GitRepo, app.AutoDeploy
					}
					cfg["pullRequestPreviews"] = app.Previews
					if app.IsPreview() {
						cfg["previewOf"], cfg["pullRequest"] = app.ParentAppID, app.PRNumber
					}
				}
				out := map[string]any{"summary": c.serviceSummary(app), "config": cfg, "domains": domains,
					"swarmService": app.ServiceName(), "envKeys": envKeys(app.Env),
					"envNote": "Only names are shown. Ask the user if you need a value."}
				if app.IsDatabase() {
					out["database"] = c.dbConnection(app)
				}
				return out, nil
			},
		},
		{
			name: "get_logs", title: "Get service logs", scope: store.ScopeRead, readOnly: true,
			description: "Returns a service's recent container output (stdout and stderr), oldest first, with timestamps. " +
				"Pass launch to read one container start's output, including a previous one that crashed or was replaced; " +
				"list_launches lists them.",
			props: map[string]any{
				"service": pService,
				"tail":    prop("integer", "Number of lines from the end, 1-1000. Default 200."),
				"since":   prop("string", `Only lines after this: a duration like "15m", "2h", "1d", or an RFC 3339 time.`),
				"launch":  prop("integer", "Launch number from list_launches. Default: all running containers."),
			},
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Tail    int    `json:"tail"`
					Since   string `json:"since"`
					Launch  int64  `json:"launch"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				if a.Tail <= 0 {
					a.Tail = 200
				}
				a.Tail = min(a.Tail, 1000)
				since, err := parseSince(a.Since)
				if err != nil {
					return nil, err
				}
				ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
				defer cancel()
				var logs string
				if a.Launch > 0 {
					run, err := c.s.store.AppRun(ctx, app.ID, a.Launch)
					if err != nil {
						return nil, fmt.Errorf("launch %d not found: call list_launches", a.Launch)
					}
					logs, err = c.s.deploy.RunLogs(ctx, run, a.Tail, since)
					if err != nil && logs == "" {
						return nil, err
					}
					if strings.TrimSpace(logs) == "" {
						return "No log lines.", nil
					}
					return tailText(logs, a.Tail, 200<<10), nil
				}
				logs, err = c.s.deploy.Logs(ctx, app, a.Tail, since)
				if err != nil && logs == "" {
					if strings.Contains(err.Error(), "not found") {
						return "The service isn't running yet, so there are no logs. Deploy it first.", nil
					}
					return nil, err
				}
				if strings.TrimSpace(logs) == "" {
					return "No log lines.", nil
				}
				return tailText(logs, a.Tail, 200<<10), nil
			},
		},
		{
			name: "list_launches", title: "List launches", scope: store.ScopeRead, readOnly: true,
			description: "Lists a service's recent launches (container starts), newest first: state, exit code, " +
				"start and end time, and the deployment that created it. Read one's output with get_logs and launch.",
			props:    map[string]any{"service": pService},
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
				defer cancel()
				_ = c.s.deploy.SyncRuns(ctx, app)
				return c.s.store.AppRuns(ctx, app.ID, 30)
			},
		},
		{
			name: "list_deployments", title: "List deployments", scope: store.ScopeRead, readOnly: true,
			description: "Lists a service's recent deployments, newest first, with status and trigger.",
			props:       map[string]any{"service": pService, "limit": prop("integer", "How many, 1-30. Default 10.")},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Limit   int    `json:"limit"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				if a.Limit <= 0 || a.Limit > 30 {
					a.Limit = 10
				}
				return c.s.store.Deployments(c.ctx, app.ID, a.Limit)
			},
		},
		{
			name: "get_deployment", title: "Get a deployment and its build log", scope: store.ScopeRead, readOnly: true,
			description: "Shows one deployment's status and the end of its build and deploy log. Use it to find out why a deploy failed.",
			props: map[string]any{
				"deployment_id": prop("integer", "Deployment id from list_deployments or deploy."),
				"tail":          prop("integer", "Log lines from the end, 1-2000. Default 200."),
			},
			required: []string{"deployment_id"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					ID   int64 `json:"deployment_id"`
					Tail int   `json:"tail"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				d, err := c.s.store.Deployment(c.ctx, a.ID)
				if err != nil {
					return nil, fmt.Errorf("deployment %d not found", a.ID)
				}
				if a.Tail <= 0 || a.Tail > 2000 {
					a.Tail = 200
				}
				d.Log = tailText(d.Log, a.Tail, 200<<10)
				return d, nil
			},
		},
		{
			name: "list_database_engines", title: "List database engines", scope: store.ScopeRead, readOnly: true,
			description: "Lists the database engines create_database accepts, with their default images.",
			run:         func(c *mcpCall) (any, error) { return deploy.Engines, nil },
		},
		{
			name: "export", title: "Export as JSON", scope: store.ScopeRead, readOnly: true,
			description: "Exports a project or a single service as SimplePanel JSON, the same format as the UI's Export. Secrets are never included.",
			props:       map[string]any{"project": pProject, "service": pService},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Project string `json:"project"`
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				var out json.RawMessage
				var err error
				switch {
				case a.Service != "":
					var app *store.App
					if app, err = c.service(a.Service); err != nil {
						return nil, err
					}
					out, err = c.invoke(c.s.exportAppHandler, "GET", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, nil)
				case a.Project != "":
					var p *store.Project
					if p, err = c.project(a.Project); err != nil {
						return nil, err
					}
					out, err = c.invoke(c.s.exportProjectHandler, "GET", "/", map[string]string{"id": strconv.FormatInt(p.ID, 10)}, nil)
				default:
					return nil, errors.New("give a project or a service")
				}
				if err != nil {
					return nil, err
				}
				return string(out), nil
			},
		},

		{
			name: "get_permissions", title: "What this connection may do", scope: store.ScopeRead, readOnly: true,
			description: "Shows this token's scope, what it allows, and which databases allow AI read-only queries.",
			run: func(c *mcpCall) (any, error) {
				allows := map[string]string{
					store.ScopeRead:   "View projects, services, logs and deployments.",
					store.ScopeManage: "View, plus create, configure and deploy services.",
					store.ScopeFull:   "Manage, plus delete services and projects.",
				}
				ps, err := c.s.visibleProjects(c.ctx, c.user)
				if err != nil {
					return nil, err
				}
				dbs := []map[string]any{}
				for _, p := range ps {
					apps, err := c.s.store.Apps(c.ctx, p.ID)
					if err != nil {
						return nil, err
					}
					for _, a := range apps {
						if a.IsDatabase() {
							on, _, _ := c.s.store.AIAccess(c.ctx, a.ID)
							dbs = append(dbs, map[string]any{"ref": a.ProjectName + "/" + a.Name, "engine": a.Kind, "aiReadAccess": on})
						}
					}
				}
				projects := []map[string]any{}
				names := map[int]string{store.LevelView: "view", store.LevelDeploy: "deploy", store.LevelManage: "manage"}
				for _, p := range ps {
					projects = append(projects, map[string]any{"project": p.Name, "access": names[c.s.level(c.ctx, c.user, p.ID)]})
				}
				return map[string]any{
					"token": c.tok.Name, "scope": c.tok.Scope, "allows": allows[c.tok.Scope],
					"user": c.user.Username, "role": c.user.Role, "projects": projects,
					"roleNote": "What you can do is the lower of the token's scope and the user's role on each project " +
						"(view: read only; deploy: create, configure, deploy and back up; manage: also delete and restore over data).",
					"secrets":   "Never available: environment values, database passwords, access tokens and webhook URLs. Ask the user for them.",
					"databases": dbs,
					"databaseNote": "db_query, db_list_tables and db_describe_table work only on databases with aiReadAccess true. " +
						"To turn it on, the user opens the database in SimplePanel and switches on \"Allow AI read-only access\".",
				}, nil
			},
		},
		{
			name: "db_list_tables", title: "List database tables", scope: store.ScopeRead, readOnly: true,
			description: "Lists a database's tables and views with approximate row counts. Needs \"Allow AI read-only access\" on that database.",
			props:       map[string]any{"service": prop("string", `Database as "project/name" or numeric id.`)},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, pw, err := c.aiDatabase(a.Service)
				if err != nil {
					return nil, err
				}
				return c.s.deploy.AIListTables(c.ctx, app, pw)
			},
		},
		{
			name: "db_describe_table", title: "Describe a database table", scope: store.ScopeRead, readOnly: true,
			description: "Lists a table's columns and types. Needs \"Allow AI read-only access\" on that database.",
			props: map[string]any{
				"service": prop("string", `Database as "project/name" or numeric id.`),
				"table":   prop("string", `Table name. On PostgreSQL use "schema.table" for schemas other than public.`),
			},
			required: []string{"service", "table"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Table   string `json:"table"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, pw, err := c.aiDatabase(a.Service)
				if err != nil {
					return nil, err
				}
				res, err := c.s.deploy.AIDescribeTable(c.ctx, app, pw, a.Table)
				if err == nil && len(res.Rows) == 0 {
					return nil, fmt.Errorf("table %q not found; call db_list_tables", a.Table)
				}
				return res, err
			},
		},
		{
			name: "db_query", title: "Query a database (read-only)", scope: store.ScopeRead, readOnly: true, audit: true,
			description: fmt.Sprintf("Runs one SQL query as a read-only database user and returns up to %d rows. "+
				"Writes are refused by the database, queries stop after 10 seconds, and every query is recorded in SimplePanel's activity log. "+
				"Needs \"Allow AI read-only access\" on that database. Use LIMIT and select only the columns you need.", deploy.AIMaxRows),
			props: map[string]any{
				"service": prop("string", `Database as "project/name" or numeric id.`),
				"query":   prop("string", "One SQL statement, e.g. SELECT id, email FROM users ORDER BY id DESC LIMIT 20."),
			},
			required: []string{"service", "query"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Query   string `json:"query"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, pw, err := c.aiDatabase(a.Service)
				if err != nil {
					return nil, err
				}
				return c.s.deploy.AIQuery(c.ctx, app, pw, a.Query)
			},
		},

		// ---- manage ----
		{
			name: "create_project", title: "Create a project", scope: store.ScopeManage, audit: true,
			description: "Creates an empty project. Names use lowercase letters, numbers and hyphens.",
			props:       map[string]any{"name": prop("string", "Project name.")},
			required:    []string{"name"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Name string `json:"name"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				c.target = a.Name
				out, err := c.invoke(c.s.createProject, "POST", "/", nil, map[string]string{"name": a.Name})
				return json.RawMessage(out), err
			},
		},
		{
			name: "create_app", title: "Create an app", scope: store.ScopeManage, audit: true,
			description: "Creates an app from a Docker image or a Git repository, optionally adds a domain with automatic HTTPS, and deploys it. A Git repo with a Dockerfile is built from it; without one it is served as a static site.",
			props: func() map[string]any {
				m := map[string]any{
					"project": pProject,
					"name":    prop("string", "App name: lowercase letters, numbers and hyphens."),
					"env":     map[string]any{"type": "object", "description": "Environment variables.", "additionalProperties": map[string]any{"type": "string"}},
					"domain":  prop("string", "Domain to serve the app on, e.g. app.example.com. Its DNS must point at this server."),
					"port":    prop("integer", "Port the app listens on inside the container. Default 80."),
					"deploy":  prop("boolean", "Deploy right away. Default true."),
					"wait":    prop("boolean", "Wait up to 3 minutes for the deploy to finish. Default false."),
				}
				for k, v := range appFieldProps {
					m[k] = v
				}
				return m
			}(),
			required: []string{"project", "name", "source"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					appFields
					Project string            `json:"project"`
					Name    string            `json:"name"`
					Env     map[string]string `json:"env"`
					Domain  string            `json:"domain"`
					Port    int               `json:"port"`
					Deploy  *bool             `json:"deploy"`
					Wait    bool              `json:"wait"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				p, err := c.project(a.Project)
				if err != nil {
					return nil, err
				}
				c.target = p.Name + "/" + a.Name
				in := inputFrom(&store.App{Replicas: 1}, &a.appFields)
				in.Name, in.Env = a.Name, envText(a.Env)
				raw, err := c.invoke(c.s.createApp, "POST", "/", map[string]string{"id": strconv.FormatInt(p.ID, 10)}, in)
				if err != nil {
					return nil, err
				}
				var app store.App
				_ = json.Unmarshal(raw, &app)
				out := map[string]any{"id": app.ID, "ref": c.target}
				if a.Domain != "" {
					port := a.Port
					if port == 0 {
						port = 80
					}
					if _, err := c.invoke(c.s.createDomain, "POST", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)},
						map[string]any{"host": a.Domain, "port": port, "https": true}); err != nil {
						out["domainError"] = err.Error()
					} else {
						out["url"] = "https://" + strings.ToLower(strings.TrimSpace(a.Domain))
					}
				}
				if a.Deploy == nil || *a.Deploy {
					d, err := c.s.deploy.Deploy(c.ctx, app.ID, "mcp")
					if err != nil {
						return nil, err
					}
					out["deploymentId"] = d.ID
					if a.Wait {
						res, err := c.waitDeployment(d.ID, 3*time.Minute)
						if err != nil {
							return nil, err
						}
						out["deployment"] = res
					}
				}
				return out, nil
			},
		},
		{
			name: "create_database", title: "Create a database", scope: store.ScopeManage, audit: true,
			description: "Creates and starts a PostgreSQL, MySQL, MariaDB, MongoDB or Redis database with generated credentials and a persistent volume. Only apps in the same project can reach it.",
			props: map[string]any{
				"project":  pProject,
				"name":     prop("string", "Database service name, e.g. db. Apps connect to it by this name."),
				"engine":   enumProp("Database engine.", "postgres", "mysql", "mariadb", "mongo", "redis"),
				"image":    prop("string", "Override the default image, e.g. postgres:16-alpine."),
				"user":     prop("string", "Username. Leave out to use the database name. Redis has no username: leave it out."),
				"password": prop("string", "Password the user chose. Leave out to generate one; you won't be able to read it, so don't invent one."),
			},
			required: []string{"project", "name", "engine"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Project  string `json:"project"`
					Name     string `json:"name"`
					Engine   string `json:"engine"`
					Image    string `json:"image"`
					User     string `json:"user"`
					Password string `json:"password"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				p, err := c.project(a.Project)
				if err != nil {
					return nil, err
				}
				c.target = p.Name + "/" + a.Name
				raw, err := c.invoke(c.s.createDatabase, "POST", "/", map[string]string{"id": strconv.FormatInt(p.ID, 10)},
					map[string]string{"name": a.Name, "engine": a.Engine, "image": a.Image, "user": a.User, "password": a.Password})
				if err != nil {
					return nil, err
				}
				var res struct {
					App        store.App         `json:"app"`
					Deployment *store.Deployment `json:"deployment"`
				}
				_ = json.Unmarshal(raw, &res)
				return map[string]any{"id": res.App.ID, "ref": c.target, "deploymentId": res.Deployment.ID,
					"next": "The password is not available to you. Apps in this project connect with the URL shown on the database's page in SimplePanel: ask the user to add it to the app's environment, or to give it to you for set_env."}, nil
			},
		},
		{
			name: "update_service", title: "Change a service's settings", scope: store.ScopeManage, audit: true,
			description: "Changes a service's settings. Only the fields you give change. Use set_env for environment variables and add_domain for domains. Changes apply on the next deploy.",
			props: func() map[string]any {
				m := map[string]any{"service": pService, "clear_git_token": prop("boolean", "Remove the saved repository access token.")}
				for k, v := range appFieldProps {
					m[k] = v
				}
				return m
			}(),
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					appFields
					Service       string `json:"service"`
					ClearGitToken bool   `json:"clear_git_token"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				in := inputFrom(app, &a.appFields)
				in.ClearGitToken = a.ClearGitToken
				if _, err := c.invoke(c.s.updateApp, "PUT", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, in); err != nil {
					return nil, err
				}
				return "Saved. Call deploy to apply the changes.", nil
			},
		},
		{
			name: "set_env", title: "Set environment variables", scope: store.ScopeManage, audit: true,
			description: "Adds, changes or removes a service's environment variables without touching the others. Changes apply on the next deploy, or right away with deploy=true.",
			props: map[string]any{
				"service": pService,
				"set":     map[string]any{"type": "object", "description": "Variables to add or change.", "additionalProperties": map[string]any{"type": "string"}},
				"unset":   map[string]any{"type": "array", "description": "Variable names to remove.", "items": map[string]any{"type": "string"}},
				"deploy":  prop("boolean", "Deploy after saving. Default false."),
			},
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string            `json:"service"`
					Set     map[string]string `json:"set"`
					Unset   []string          `json:"unset"`
					Deploy  bool              `json:"deploy"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				if e := deploy.EngineByKey(app.Kind); e != nil {
					for k := range a.Set {
						if e.IsCredentialVar(k) {
							return nil, fmt.Errorf("%s is a generated database credential; changing it here wouldn't change the database's real password", k)
						}
					}
					for _, k := range a.Unset {
						if e.IsCredentialVar(k) {
							return nil, fmt.Errorf("%s is a generated database credential and can't be removed", k)
						}
					}
				}
				lines, _ := deploy.ParseEnv(app.Env)
				var b strings.Builder
				done := map[string]bool{}
				for _, l := range lines {
					k, _, _ := strings.Cut(l, "=")
					if slices.Contains(a.Unset, k) {
						continue
					}
					if v, ok := a.Set[k]; ok {
						l = k + "=" + v
						done[k] = true
					}
					b.WriteString(l + "\n")
				}
				var added []string
				for k := range a.Set {
					if !done[k] {
						added = append(added, k)
					}
				}
				slices.Sort(added)
				for _, k := range added {
					b.WriteString(k + "=" + a.Set[k] + "\n")
				}
				in := inputFrom(app, &appFields{})
				in.Env = b.String()
				if _, err := c.invoke(c.s.updateApp, "PUT", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, in); err != nil {
					return nil, err
				}
				out := map[string]any{"saved": true, "envKeys": envKeys(in.Env)}
				if a.Deploy {
					d, err := c.s.deploy.Deploy(c.ctx, app.ID, "mcp")
					if err != nil {
						return nil, err
					}
					out["deploymentId"] = d.ID
				}
				return out, nil
			},
		},
		{
			name: "add_domain", title: "Add a domain", scope: store.ScopeManage, audit: true,
			description: "Serves an app on a domain with automatic HTTPS (Let's Encrypt). The domain's DNS must point at this server. Takes effect right away.",
			props: map[string]any{
				"service": pService,
				"host":    prop("string", "Domain, e.g. app.example.com."),
				"port":    prop("integer", "Port the app listens on inside the container. Default 80."),
			},
			required: []string{"service", "host"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Host    string `json:"host"`
					Port    int    `json:"port"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				out, err := c.invoke(c.s.createDomain, "POST", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)},
					map[string]any{"host": a.Host, "port": a.Port, "https": true})
				return json.RawMessage(out), err
			},
		},
		{
			name: "remove_domain", title: "Remove a domain", scope: store.ScopeManage, audit: true,
			description: "Stops serving an app on a domain. Takes effect right away.",
			props:       map[string]any{"service": pService, "host": prop("string", "Domain to remove.")},
			required:    []string{"service", "host"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Host    string `json:"host"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				ds, err := c.s.store.Domains(c.ctx, app.ID)
				if err != nil {
					return nil, err
				}
				for _, d := range ds {
					if d.Host == strings.ToLower(strings.TrimSpace(a.Host)) {
						if _, err := c.invoke(c.s.deleteDomain, "DELETE", "/", map[string]string{"id": strconv.FormatInt(d.ID, 10)}, nil); err != nil {
							return nil, err
						}
						return "Removed " + d.Host + ".", nil
					}
				}
				return nil, fmt.Errorf("%s has no domain %q", c.target, a.Host)
			},
		},
		{
			name: "deploy", title: "Deploy a service", scope: store.ScopeManage, audit: true,
			description: "Deploys a service with its current settings: pulls the image or builds the repository, then rolls out with zero downtime. Use wait=true to get the result.",
			props: map[string]any{
				"service": pService,
				"wait":    prop("boolean", "Wait up to 3 minutes for the deploy to finish. Default false."),
			},
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Wait    bool   `json:"wait"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				d, err := c.s.deploy.Deploy(c.ctx, app.ID, "mcp")
				if err != nil {
					return nil, err
				}
				if a.Wait {
					return c.waitDeployment(d.ID, 3*time.Minute)
				}
				return map[string]any{"deploymentId": d.ID, "status": d.Status}, nil
			},
		},
		{
			name: "service_action", title: "Restart, stop or start a service", scope: store.ScopeManage, audit: true,
			description: "restart replaces the containers, stop scales to 0, start scales back to the configured replicas.",
			props:       map[string]any{"service": pService, "action": enumProp("What to do.", "restart", "stop", "start")},
			required:    []string{"service", "action"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Action  string `json:"action"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				h := map[string]func(http.ResponseWriter, *http.Request, *store.User){
					"restart": c.s.restartApp, "stop": c.s.stopApp, "start": c.s.startApp,
				}[a.Action]
				if h == nil {
					return nil, errors.New(`action must be "restart", "stop" or "start"`)
				}
				if _, err := c.invoke(h, "POST", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, nil); err != nil {
					return nil, err
				}
				return "Done: " + a.Action + " " + c.target + ".", nil
			},
		},
		{
			name: "import", title: "Import from JSON", scope: store.ScopeManage, audit: true,
			description: "Creates a project, or a service inside a project, from SimplePanel export JSON. Everything is validated before anything is created.",
			props: map[string]any{
				"document": prop("string", "The export JSON, as text."),
				"project":  prop("string", "For a single-service document: the project to add it to."),
				"name":     prop("string", "Optional new name for the imported project or service."),
				"deploy":   prop("boolean", "Deploy the imported services. Default false."),
			},
			required: []string{"document"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Document string `json:"document"`
					Project  string `json:"project"`
					Name     string `json:"name"`
					Deploy   bool   `json:"deploy"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				req := importRequest{Document: a.Document, Name: a.Name, Deploy: a.Deploy}
				if a.Project != "" {
					p, err := c.project(a.Project)
					if err != nil {
						return nil, err
					}
					req.ProjectID = p.ID
					c.target = p.Name
				}
				out, err := c.invoke(c.s.importHandler, "POST", "/", nil, req)
				return json.RawMessage(out), err
			},
		},

		// ---- full ----
		{
			name: "delete_service", title: "Delete a service", scope: store.ScopeFull, destructive: true, audit: true,
			description: "Permanently removes an app or database and its containers. Volumes (database data) are kept on the server. Pass confirm with the service's name.",
			props:       map[string]any{"service": pService, "confirm": prop("string", "The service's name, to confirm.")},
			required:    []string{"service", "confirm"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Confirm string `json:"confirm"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				if a.Confirm != app.Name {
					return nil, fmt.Errorf("confirm must be %q", app.Name)
				}
				if _, err := c.invoke(c.s.deleteApp, "DELETE", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, nil); err != nil {
					return nil, err
				}
				return "Deleted " + c.target + ".", nil
			},
		},
		{
			name: "delete_project", title: "Delete a project", scope: store.ScopeFull, destructive: true, audit: true,
			description: "Permanently removes a project and every app and database in it. Pass confirm with the project's name.",
			props:       map[string]any{"project": pProject, "confirm": prop("string", "The project's name, to confirm.")},
			required:    []string{"project", "confirm"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Project string `json:"project"`
					Confirm string `json:"confirm"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				p, err := c.project(a.Project)
				if err != nil {
					return nil, err
				}
				c.target = p.Name
				if a.Confirm != p.Name {
					return nil, fmt.Errorf("confirm must be %q", p.Name)
				}
				if _, err := c.invoke(c.s.deleteProject, "DELETE", "/", map[string]string{"id": strconv.FormatInt(p.ID, 10)}, nil); err != nil {
					return nil, err
				}
				return "Deleted project " + p.Name + ".", nil
			},
		},
	}
	mcpTools = append(mcpTools, v02Tools()...)
	mcpTools = append(mcpTools, v03Tools()...)
}
