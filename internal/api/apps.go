package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/gitprovider"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// ---- System & settings ----

func (s *Server) system(w http.ResponseWriter, r *http.Request, _ *store.User) {
	out := map[string]any{"version": s.version}
	wd, _ := s.store.Setting(r.Context(), "wildcard_domain")
	out["wildcardDomain"] = wd
	zone, _ := time.Now().Zone()
	out["timezone"] = zone
	if info, err := s.docker.Info(r.Context()); err == nil {
		out["docker"] = info.ServerVersion
		out["swarm"] = info.Swarm.LocalNodeState
	} else {
		out["dockerError"] = err.Error()
	}
	out["dockerHealth"] = s.docker.Health()
	pd, _ := s.store.Setting(r.Context(), "panel_domain")
	out["panelDomain"] = pd
	ip, _ := s.store.Setting(r.Context(), "server_ip")
	out["serverIp"] = ip
	out["httpsOnly"] = s.httpsOnly(r.Context())
	out["viaHttps"] = viaProxyHTTPS(r)
	writeJSON(w, 200, out)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var body struct {
		PanelDomain    *string `json:"panelDomain"`
		WildcardDomain *string `json:"wildcardDomain"`
		HTTPSOnly      *bool   `json:"httpsOnly"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	out := map[string]any{}
	if body.HTTPSOnly != nil && *body.HTTPSOnly {
		// Only from the HTTPS address itself: that proves it works, so
		// turning plain HTTP off can't lock anyone out.
		pd, _ := s.store.Setting(r.Context(), "panel_domain")
		if body.PanelDomain != nil {
			pd = strings.TrimSpace(strings.ToLower(*body.PanelDomain))
		}
		if pd == "" || !viaProxyHTTPS(r) || !strings.EqualFold(hostOnly(r.Host), pd) {
			writeErr(w, 400, "open SimplePanel at its HTTPS address (https://"+orEmpty(pd, "your panel domain")+") and turn this on from there")
			return
		}
	}
	if body.PanelDomain != nil {
		pd := strings.TrimSpace(strings.ToLower(*body.PanelDomain))
		if pd != "" {
			if err := deploy.ValidHost(pd); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		}
		if err := s.store.SetSetting(r.Context(), "panel_domain", pd); err != nil {
			s.storeErr(w, err)
			return
		}
		out["panelDomain"] = pd
		if pd == "" && s.httpsOnly(r.Context()) {
			// No domain, no HTTPS address: plain HTTP must work again.
			if err := s.setHTTPSOnly(r.Context(), false); err != nil {
				s.storeErr(w, err)
				return
			}
			out["httpsOnly"] = false
		}
	}
	if body.WildcardDomain != nil {
		wd := strings.TrimPrefix(strings.TrimSpace(strings.ToLower(*body.WildcardDomain)), "*.")
		if wd != "" {
			if err := deploy.ValidHost(wd); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			if strings.Count(wd, ".") < 1 {
				writeErr(w, 400, "use a domain you control, such as apps.example.com")
				return
			}
		}
		if err := s.store.SetSetting(r.Context(), "wildcard_domain", wd); err != nil {
			s.storeErr(w, err)
			return
		}
		out["wildcardDomain"] = wd
	}
	if body.HTTPSOnly != nil {
		if err := s.setHTTPSOnly(r.Context(), *body.HTTPSOnly); err != nil {
			s.storeErr(w, err)
			return
		}
		out["httpsOnly"] = *body.HTTPSOnly
	}
	if err := s.deploy.SyncProxy(r.Context()); err != nil {
		s.log.Error("proxy sync", "err", err)
		writeErr(w, 500, "saved, but updating the proxy failed: "+err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- Projects ----

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, u *store.User) {
	ps, err := s.visibleProjects(r.Context(), u)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type row struct {
		store.Project
		Level int `json:"level"`
	}
	out := make([]row, len(ps))
	for i, p := range ps {
		out[i] = row{p, s.level(r.Context(), u, p.ID)}
	}
	writeJSON(w, 200, out)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdminScope(w, u, store.ScopeManage) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if err := deploy.ValidName(body.Name); err != nil {
		writeErr(w, 400, "name: "+err.Error())
		return
	}
	p, err := s.store.CreateProject(r.Context(), body.Name)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "a project with that name already exists")
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, id, store.LevelManage) || !requireAdmin(w, u) {
		return
	}
	apps, err := s.store.Apps(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	for i := range apps {
		if err := s.removeApp(r.Context(), &apps[i]); err != nil {
			writeErr(w, 502, fmt.Sprintf("removing app %s failed: %v", apps[i].Name, err))
			return
		}
	}
	p, err := s.store.Project(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.store.DeleteProject(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.deploy.RemoveProjectNetwork(p.Name)
	_ = s.deploy.SyncProxy(r.Context())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Apps ----

// appStatus is the service's state as people should see it: "deploying"
// while a deploy runs on a service that isn't up yet, and "deploy_failed"
// when it never ran because its deploy failed.
func (s *Server) appStatus(ctx context.Context, a *store.App) deploy.Status {
	return s.withDeployState(ctx, a, s.deploy.Status(ctx, a))
}

// withDeployState turns "not deployed" into "deploying" or "deploy
// failed" when the app's first deploy is running or failed.
func (s *Server) withDeployState(ctx context.Context, a *store.App, st deploy.Status) deploy.Status {
	if st.State != "not_deployed" {
		return st
	}
	if ds, err := s.store.Deployments(ctx, a.ID, 1); err == nil && len(ds) > 0 {
		switch ds[0].Status {
		case "failed":
			st.State = "deploy_failed"
		case "queued", "running":
			st.State = "deploying"
		}
	}
	return st
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, id, store.LevelView) {
		return
	}
	apps, err := s.store.Apps(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type row struct {
		store.App
		Status deploy.Status `json:"status"`
	}
	out := make([]row, len(apps))
	secrets := s.level(r.Context(), u, id) >= store.LevelDeploy
	ptrs := make([]*store.App, len(apps))
	for i := range apps {
		ptrs[i] = &apps[i]
	}
	statuses := s.deploy.Statuses(r.Context(), ptrs)
	for i := range apps {
		if !secrets {
			redactApp(&apps[i])
		}
		out[i] = row{App: apps[i], Status: s.withDeployState(r.Context(), &apps[i], statuses[apps[i].ID])}
	}
	writeJSON(w, 200, out)
}

// appInput is the editable part of an app. GitToken is write-only: an empty
// value keeps the stored token, ClearGitToken removes it.
type appInput struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	Image         string         `json:"image"`
	GitURL        string         `json:"gitUrl"`
	GitBranch     string         `json:"gitBranch"`
	GitToken      string         `json:"gitToken"`
	ClearGitToken bool           `json:"clearGitToken"`
	BuildPath     string         `json:"buildPath"`
	Dockerfile    string         `json:"dockerfile"`
	BuildType     string         `json:"buildType"`
	BuildImage    string         `json:"buildImage"`
	BuildCommand  string         `json:"buildCommand"`
	OutputDir     string         `json:"outputDir"`
	SPA           bool           `json:"spa"`
	Env           string         `json:"env"`
	Replicas      *int           `json:"replicas"`
	CPULimit      float64        `json:"cpuLimit"`
	MemoryMB      int            `json:"memoryMb"`
	Command       string         `json:"command"`
	Volumes       []store.Volume `json:"volumes"`
	Ports         []store.Port   `json:"ports"`
	AutoDomain    *bool          `json:"autoDomain"`
	HTTPPort      *int           `json:"httpPort"`

	// Git integration; nil keeps the current value.
	GitProviderID *int64  `json:"gitProviderId"`
	GitRepo       *string `json:"gitRepo"`
	AutoDeploy    *bool   `json:"autoDeploy"`
	Previews      *bool   `json:"previews"`
	PreviewEnv    *string `json:"previewEnv"`

	// RegistryID is the saved registry login for a private image: 0 for
	// none, nil to keep the current one or pick it from the image's
	// registry (see resolveRegistry).
	RegistryID *int64 `json:"registryId"`
}

// apply validates in and copies it onto a.
func (in *appInput) apply(a *store.App) error {
	if a.IsDatabase() && in.Source != "image" {
		return errors.New("databases always run from a Docker image")
	}
	a.Source = in.Source
	switch in.Source {
	case "image":
		in.Image = strings.TrimSpace(in.Image)
		if err := deploy.ValidImage(in.Image); err != nil {
			return err
		}
		a.Image = in.Image
	case "git":
		in.GitURL = deploy.CleanGitURL(in.GitURL)
		if err := deploy.ValidGitURL(in.GitURL); err != nil {
			return err
		}
		if err := deploy.ValidBranch(strings.TrimSpace(in.GitBranch)); err != nil {
			return err
		}
		bp, err := deploy.CleanRelPath(strings.TrimSpace(in.BuildPath))
		if err != nil {
			return fmt.Errorf("build path: %w", err)
		}
		df, err := deploy.CleanRelPath(strings.TrimSpace(in.Dockerfile))
		if err != nil {
			return fmt.Errorf("dockerfile: %w", err)
		}
		a.GitURL, a.GitBranch, a.BuildPath, a.Dockerfile = in.GitURL, strings.TrimSpace(in.GitBranch), bp, df
		bt := strings.TrimSpace(in.BuildType)
		if bt == "" {
			bt = "auto"
		}
		if !slices.Contains(deploy.BuildTypes, bt) {
			return errors.New(`build type must be "auto", "dockerfile" or "static"`)
		}
		img := strings.TrimSpace(in.BuildImage)
		if img != "" {
			if err := deploy.ValidImage(img); err != nil {
				return fmt.Errorf("build image: %w", err)
			}
		}
		cmd := strings.TrimSpace(in.BuildCommand)
		if len(cmd) > 1000 || strings.ContainsAny(cmd, "\x00\r\n") {
			return errors.New("build command must be a single line under 1000 characters")
		}
		out, err := deploy.CleanRelPath(strings.TrimSpace(in.OutputDir))
		if err != nil {
			return fmt.Errorf("output directory: %w", err)
		}
		a.BuildType, a.BuildImage, a.BuildCommand, a.OutputDir, a.SPA = bt, img, cmd, out, in.SPA
		if in.ClearGitToken {
			a.GitToken = ""
		} else if t := strings.TrimSpace(in.GitToken); t != "" {
			if strings.ContainsAny(t, " \r\n") || len(t) > 512 {
				return errors.New("access token looks invalid")
			}
			a.GitToken = t
		}
	default:
		return errors.New(`source must be "image" or "git"`)
	}
	a.HasGitToken = a.GitToken != ""
	if _, err := deploy.ParseEnv(in.Env); err != nil {
		return err
	}
	a.Env = in.Env
	if in.Replicas != nil {
		if *in.Replicas < 0 || *in.Replicas > 50 {
			return errors.New("replicas must be between 0 and 50")
		}
		a.Replicas = *in.Replicas
		if a.IsDatabase() && a.Replicas > 1 {
			return errors.New("databases run as a single replica")
		}
	} else if a.ID == 0 {
		a.Replicas = 1
	}
	if in.CPULimit < 0 || in.CPULimit > 256 {
		return errors.New("CPU limit must be between 0 and 256")
	}
	if in.MemoryMB < deploy.NoMemoryLimit || (in.MemoryMB > 0 && in.MemoryMB < deploy.MinMemoryMB) || in.MemoryMB > 1<<20 {
		return fmt.Errorf("memory limit must be 0 (automatic), -1 (no limit) or at least %d MB", deploy.MinMemoryMB)
	}
	a.CPULimit, a.MemoryMB = in.CPULimit, in.MemoryMB
	if _, err := deploy.SplitCommand(in.Command); err != nil {
		return err
	}
	a.Command = strings.TrimSpace(in.Command)
	seenVol := map[string]bool{}
	a.Volumes = []store.Volume{}
	for _, v := range in.Volumes {
		if err := deploy.ValidName(v.Name); err != nil {
			return fmt.Errorf("volume name %q: %w", v.Name, err)
		}
		if err := deploy.ValidVolumePath(v.Path); err != nil {
			return err
		}
		if seenVol[v.Name] {
			return fmt.Errorf("volume %q is listed twice", v.Name)
		}
		seenVol[v.Name] = true
		a.Volumes = append(a.Volumes, v)
	}
	if in.AutoDomain != nil {
		a.AutoDomain = *in.AutoDomain && !a.IsDatabase()
	} else if a.ID == 0 {
		a.AutoDomain = !a.IsDatabase()
	}
	if in.HTTPPort != nil {
		if *in.HTTPPort != 0 && deploy.ValidPort(*in.HTTPPort) != nil {
			return errors.New("HTTP port must be between 1 and 65535")
		}
		a.HTTPPort = *in.HTTPPort
	}
	if in.AutoDeploy != nil {
		a.AutoDeploy = *in.AutoDeploy
	} else if a.ID == 0 {
		a.AutoDeploy = true
	}
	if in.Previews != nil {
		a.Previews = *in.Previews && a.Source == "git" && !a.IsDatabase()
	}
	if in.PreviewEnv != nil {
		if _, err := deploy.ParseEnv(*in.PreviewEnv); err != nil {
			return fmt.Errorf("preview variables: %w", err)
		}
		a.PreviewEnv = *in.PreviewEnv
	}
	if a.Source != "git" {
		a.GitProviderID, a.GitRepo, a.Previews = 0, "", false
	}
	a.Ports = []store.Port{}
	for _, p := range in.Ports {
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return errors.New("port protocol must be tcp or udp")
		}
		if deploy.ValidPort(int(p.Published)) != nil || deploy.ValidPort(int(p.Target)) != nil {
			return errors.New("ports must be between 1 and 65535")
		}
		// Swarm's routing mesh takes a published port before the server's
		// own programs see it: publishing SSH's port would lock everyone
		// out, and Swarm's own ports would break Docker networking.
		if why := reservedPort(int(p.Published)); why != "" {
			if strings.HasSuffix(why, "SimplePanel") {
				why += "; use a domain instead"
			}
			return errors.New(why)
		}
		a.Ports = append(a.Ports, p)
	}
	return nil
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	pid, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, pid, store.LevelDeploy) {
		return
	}
	p, err := s.store.Project(r.Context(), pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in appInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := deploy.ValidName(in.Name); err != nil {
		writeErr(w, 400, "name: "+err.Error())
		return
	}
	a := &store.App{ProjectID: p.ID, ProjectName: p.Name, Name: in.Name}
	if err := in.apply(a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.GitProviderID != nil && *in.GitProviderID != 0 {
		if err := s.linkRepo(r, a, *in.GitProviderID, deref(in.GitRepo)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if err := s.resolveRegistry(r.Context(), a, in.RegistryID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.CreateApp(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "an app with that name already exists in this project")
			return
		}
		s.storeErr(w, err)
		return
	}
	if a.AutoDomain {
		_ = s.deploy.SyncProxy(r.Context())
	}
	writeJSON(w, 201, a)
}

func (s *Server) listEngines(w http.ResponseWriter, r *http.Request, _ *store.User) {
	writeJSON(w, 200, deploy.Engines)
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request, u *store.User) {
	pid, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, pid, store.LevelDeploy) {
		return
	}
	p, err := s.store.Project(r.Context(), pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name     string `json:"name"`
		Engine   string `json:"engine"`
		Image    string `json:"image"`
		User     string `json:"user"`     // optional, generated if empty
		Password string `json:"password"` // optional, generated if empty
		MemoryMB int    `json:"memoryMb"` // 0 automatic, -1 no limit
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := deploy.ValidName(in.Name); err != nil {
		writeErr(w, 400, "name: "+err.Error())
		return
	}
	e := deploy.EngineByKey(in.Engine)
	if e == nil {
		writeErr(w, 400, "unknown database engine")
		return
	}
	cred := deploy.DBCredentials{User: strings.TrimSpace(in.User), Password: in.Password}
	if err := cred.Validate(e); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.MemoryMB < deploy.NoMemoryLimit || (in.MemoryMB > 0 && in.MemoryMB < deploy.MinMemoryMB) {
		writeErr(w, 400, fmt.Sprintf("memory limit must be 0 (automatic), -1 (no limit) or at least %d MB", deploy.MinMemoryMB))
		return
	}
	a := deploy.NewDatabaseApp(e, p.ID, p.Name, in.Name, cred, store.RandomToken)
	a.MemoryMB = in.MemoryMB
	if in.Image = strings.TrimSpace(in.Image); in.Image != "" {
		if err := deploy.ValidImage(in.Image); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		a.Image = in.Image
	}
	warn, ok := s.memoryGuard(w, r, u, a)
	if !ok {
		return
	}
	if err := s.store.CreateApp(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "an app or database with that name already exists in this project")
			return
		}
		s.storeErr(w, err)
		return
	}
	d, err := s.deploy.Deploy(r.Context(), a.ID, "create")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"app": a, "deployment": d, "warnings": warn})
}

// loadApp loads the app named by the {id} path value and checks that u
// has at least level need on its project.
func (s *Server) loadApp(w http.ResponseWriter, r *http.Request, u *store.User, need int) (*store.App, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	return s.loadAppID(w, r, u, id, need)
}

func (s *Server) loadAppID(w http.ResponseWriter, r *http.Request, u *store.User, id int64, need int) (*store.App, bool) {
	a, err := s.store.App(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if !s.allow(w, r, u, a.ProjectID, need) {
		return nil, false
	}
	return a, true
}

// redactApp removes values only users who can deploy may see: environment
// variable values (names are kept) and Git tokens.
func redactApp(a *store.App) {
	a.Env = keysOnly(a.Env)
	a.PreviewEnv = keysOnly(a.PreviewEnv)
}

func keysOnly(env string) string {
	var keys []string
	for _, k := range envKeys(env) {
		keys = append(keys, k+"=")
	}
	out := strings.Join(keys, "\n")
	if out != "" {
		out += "\n"
	}
	return out
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	lvl := s.level(r.Context(), u, a.ProjectID)
	domains, err := s.store.Domains(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wd, _ := s.store.Setting(r.Context(), "wildcard_domain")
	out := map[string]any{
		"app":         a,
		"domains":     domains,
		"status":      s.appStatus(r.Context(), a),
		"serviceName": a.ServiceName(),
		"level":       lvl,
		"autoHost":    deploy.AutoHost(a, wd),
		"wildcard":    wd != "" && !a.IsDatabase(),
		"autoPort":    s.deploy.AutoPort(r.Context(), a),
		"backupKind":  deploy.BackupKind(a),
	}
	if ds, err := s.store.Deployments(r.Context(), a.ID, 1); err == nil && len(ds) > 0 {
		last := map[string]any{"id": ds[0].ID, "status": ds[0].Status, "createdAt": ds[0].CreatedAt}
		if ds[0].Status == "failed" {
			if d, err := s.store.Deployment(r.Context(), ds[0].ID); err == nil {
				last["reason"] = deploy.FailureReason(d.Log)
				last["tail"] = deploy.LogTail(d.Log, 8)
			}
		}
		out["lastDeployment"] = last
	}
	if lvl >= store.LevelDeploy {
		out["webhookPath"] = "/api/hooks/deploy/" + a.WebhookToken
	}
	if a.RegistryID != 0 {
		if g, err := s.store.Registry(r.Context(), a.RegistryID); err == nil {
			out["registry"] = map[string]any{"id": g.ID, "name": g.Name, "server": g.Server}
		}
	}
	if a.Source == "git" {
		if a.DeployKeyPub != "" {
			out["deployKeyUrl"] = deploy.DeployKeyURL(a.GitURL)
		}
		if p := s.providerFor(a); p != nil {
			out["provider"] = map[string]any{"id": p.ID, "kind": p.Kind, "name": p.Name}
			web := strings.TrimSuffix(a.GitURL, ".git")
			out["repoUrl"] = web
		}
		if a.IsPreview() {
			if parent, err := s.store.App(r.Context(), a.ParentAppID); err == nil {
				out["parent"] = map[string]any{"id": parent.ID, "name": parent.Name}
			}
		} else {
			n, _ := s.store.Previews(r.Context(), a.ID)
			out["previewCount"] = len(n)
		}
	}
	if a.IsDatabase() {
		info := deploy.DatabaseInfo(a)
		if lvl < store.LevelDeploy && info != nil {
			info.URL = info.URLTemplate(info.Host)
			info.Password, info.RootPassword = "", ""
		}
		out["database"] = info
		out["aiAccess"], _, _ = s.store.AIAccess(r.Context(), a.ID)
		out["public"] = s.publicInfo(r, a, lvl)
		out["canQuery"] = deploy.QuerySupported(a.Kind) && u.CanQueryDatabases()
		if e := deploy.EngineByKey(a.Kind); e != nil {
			out["aiAccessSupported"] = e.AIAccess
		}
	}
	if lvl < store.LevelDeploy {
		redactApp(a)
	}
	writeJSON(w, 200, out)
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if a.IsPreview() {
		writeErr(w, 409, "previews copy their settings from the app they preview; change that app instead")
		return
	}
	prevAuto, prevPort := a.AutoDomain, a.HTTPPort
	var in appInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.apply(a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.GitProviderID != nil && a.Source == "git" {
		if err := s.linkRepo(r, a, *in.GitProviderID, deref(in.GitRepo)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if err := s.resolveRegistry(r.Context(), a, in.RegistryID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.UpdateApp(r.Context(), a); err != nil {
		s.storeErr(w, err)
		return
	}
	if a.AutoDomain != prevAuto || a.HTTPPort != prevPort {
		if err := s.deploy.SyncProxy(r.Context()); err != nil {
			s.log.Error("proxy sync", "err", err)
		}
	}
	writeJSON(w, 200, a)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	// Previews are throwaway, so anyone who can deploy may remove one.
	if !a.IsPreview() && !s.allow(w, r, u, a.ProjectID, store.LevelManage) {
		return
	}
	if err := s.removeApp(r.Context(), a); err != nil {
		writeErr(w, 502, "removing the service failed: "+err.Error())
		return
	}
	if err := s.store.DeleteApp(r.Context(), a.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	_ = s.deploy.SyncProxy(r.Context())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deployApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	warn, ok := s.memoryGuard(w, r, u, a)
	if !ok {
		return
	}
	d, err := s.deploy.Deploy(r.Context(), a.ID, "manual")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, struct {
		*store.Deployment
		Warnings []string `json:"warnings,omitempty"`
	}{d, warn})
}

func (s *Server) dockerAction(w http.ResponseWriter, err error, warn ...string) {
	if errors.Is(err, docker.ErrNotFound) {
		writeErr(w, 409, "the app isn't deployed yet; deploy it first")
		return
	}
	if errors.Is(err, docker.ErrUnavailable) {
		writeErr(w, 503, "Docker is not responding on this server, so nothing was changed. SimplePanel reconnects automatically; try again in a minute.")
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	out := map[string]any{"ok": true}
	if len(warn) > 0 {
		out["warnings"] = warn
	}
	writeJSON(w, 200, out)
}

func (s *Server) restartApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		warn, ok := s.memoryGuard(w, r, u, a)
		if !ok {
			return
		}
		s.dockerAction(w, s.deploy.Restart(r.Context(), a), warn...)
	}
}

func (s *Server) stopApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		s.dockerAction(w, s.deploy.Scale(r.Context(), a, 0))
	}
}

func (s *Server) startApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		warn, ok := s.memoryGuard(w, r, u, a)
		if !ok {
			return
		}
		s.dockerAction(w, s.deploy.Scale(r.Context(), a, uint64(max(a.Replicas, 1))), warn...)
	}
}

// appLogs returns a service's logs: every container's, or one launch's with
// ?run=N. ?since= (Unix seconds) and ?tail= narrow them, and ?download=1
// sends them as a .log file.
func (s *Server) appLogs(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	q := r.URL.Query()
	download := q.Get("download") == "1"
	maxTail := 5000
	if download {
		maxTail = 100000
	}
	tail, _ := strconv.Atoi(q.Get("tail"))
	if tail < 0 || tail > maxTail || (tail == 0 && !download) {
		tail = 300
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	if since < 0 {
		since = 0
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var (
		logs string
		err  error
		run  *store.AppRun
	)
	if v := q.Get("run"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n <= 0 {
			writeErr(w, 400, "bad run")
			return
		}
		if run, err = s.store.AppRun(ctx, a.ID, n); err != nil {
			s.storeErr(w, err)
			return
		}
		logs, err = s.deploy.RunLogs(ctx, run, tail, since)
	} else {
		if download && tail == 0 {
			tail = maxTail
		}
		logs, err = s.deploy.Logs(ctx, a, tail, since)
		if errors.Is(err, docker.ErrNotFound) {
			logs, err = "", nil
		}
	}
	if err != nil && logs == "" {
		writeErr(w, 502, err.Error())
		return
	}
	if download {
		name := a.ProjectName + "-" + a.Name
		if run != nil {
			name += fmt.Sprintf("-launch-%d", run.Number)
		}
		name += "-" + time.Now().UTC().Format("20060102-150405") + ".log"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write([]byte(logs))
		return
	}
	writeJSON(w, 200, map[string]string{"logs": logs})
}

// appRuns lists a service's launches (container starts), newest first.
func (s *Server) appRuns(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	syncErr := s.deploy.SyncRuns(ctx, a)
	runs, err := s.store.AppRuns(ctx, a.ID, deploy.RunsKept)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"runs": runs, "kept": deploy.RunsKept}
	if syncErr != nil {
		// Docker unreachable: still show what was recorded.
		out["warning"] = "Couldn't reach Docker, so this list may be out of date."
	}
	writeJSON(w, 200, out)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	ds, err := s.store.Deployments(r.Context(), a.ID, 30)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ds)
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	d, err := s.store.Deployment(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, ok := s.loadAppID(w, r, u, d.AppID, store.LevelView); !ok {
		return
	}
	writeJSON(w, 200, d)
}

// ---- Domains ----

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	var d store.Domain
	if !readJSON(w, r, &d) {
		return
	}
	if a.IsDatabase() {
		writeErr(w, 400, "databases can't have domains; connect to them from apps in the same project")
		return
	}
	d.AppID = a.ID
	// "example.com/api" in the host field is a path domain, as is an
	// explicit path; an explicit path wins.
	if d.Path == "" {
		d.Path = deploy.DomainPath(d.Host)
	} else {
		d.Path = deploy.CleanPath(d.Path)
	}
	d.Host = deploy.CleanHost(d.Host)
	if err := deploy.ValidHost(d.Host); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := deploy.ValidPath(d.Path); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if d.Path == "" {
		d.StripPath = false
	}
	// Port 0 is "automatic": the port the image listens on, else 80.
	if d.Port != 0 {
		if err := deploy.ValidPort(d.Port); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if pd, _ := s.store.Setting(r.Context(), "panel_domain"); pd == d.Host {
		writeErr(w, 409, "that domain is used by the panel itself")
		return
	}
	if wd, _ := s.store.Setting(r.Context(), "wildcard_domain"); wd != "" && d.Host == wd {
		writeErr(w, 409, "that is the wildcard domain for automatic subdomains; use a subdomain of it")
		return
	}
	if err := s.store.CreateDomain(r.Context(), &d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, s.domainOwner(r.Context(), d.Host, d.Path, a.ID))
			return
		}
		s.storeErr(w, err)
		return
	}
	if err := s.deploy.SyncProxy(r.Context()); err != nil {
		s.log.Error("proxy sync", "err", err)
	}
	writeJSON(w, 201, d)
}

// domainOwner explains which app already uses a domain (host and path).
func (s *Server) domainOwner(ctx context.Context, host, path string, appID int64) string {
	label := host + path
	routes, err := s.store.AllRoutes(ctx)
	if err == nil {
		for _, rt := range routes {
			if rt.Host != host || rt.Path != path {
				continue
			}
			if rt.AppID == appID {
				return "this app already has " + label
			}
			if a, err := s.store.App(ctx, rt.AppID); err == nil {
				if path == "" {
					return fmt.Sprintf("the app %s in project %s already uses %s. Remove it there first, or add a path like %s/api to send only part of the site here", a.Name, a.ProjectName, label, host)
				}
				return fmt.Sprintf("the app %s in project %s already uses %s. Remove it there first", a.Name, a.ProjectName, label)
			}
		}
	}
	return "another app already uses " + label + ". Remove it there first"
}

// updateDomain changes the app port a domain sends traffic to, or whether a
// path domain removes its path before forwarding.
func (s *Server) updateDomain(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	d, err := s.store.Domain(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, ok := s.loadAppID(w, r, u, d.AppID, store.LevelDeploy); !ok {
		return
	}
	var in struct {
		Port      *int  `json:"port"`
		StripPath *bool `json:"stripPath"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Port != nil && *in.Port != 0 {
		if err := deploy.ValidPort(*in.Port); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if in.StripPath != nil && *in.StripPath && d.Path == "" {
		writeErr(w, 400, "only a domain with a path, like example.com/api, can remove its path")
		return
	}
	if in.Port != nil {
		if err := s.store.SetDomainPort(r.Context(), id, *in.Port); err != nil {
			s.storeErr(w, err)
			return
		}
		d.Port = *in.Port
	}
	if in.StripPath != nil {
		if err := s.store.SetDomainStripPath(r.Context(), id, *in.StripPath); err != nil {
			s.storeErr(w, err)
			return
		}
		d.StripPath = *in.StripPath
	}
	if err := s.deploy.SyncProxy(r.Context()); err != nil {
		s.log.Error("proxy sync", "err", err)
	}
	writeJSON(w, 200, d)
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	d, err := s.store.Domain(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, ok := s.loadAppID(w, r, u, d.AppID, store.LevelDeploy); !ok {
		return
	}
	if err := s.store.DeleteDomain(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	_ = s.deploy.SyncProxy(r.Context())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Webhooks ----

func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	tok, err := s.store.RotateWebhook(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"webhookPath": "/api/hooks/deploy/" + tok})
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// webhookDeploy redeploys an app. It is authenticated by the secret token in
// the URL, so it works from any CI/CD system with a plain POST:
//
//	curl -X POST https://panel.example.com/api/hooks/deploy/<token>
//	curl -X POST ".../api/hooks/deploy/<token>?tag=1.4.2"   (image apps)
//
// GitHub and GitLab webhooks can point at the same URL: pushes to the app's
// branch deploy, other branches are ignored, and pull request events manage
// previews when the app has them turned on.
func (s *Server) webhookDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.hookLimit.Allow(clientIP(r)) {
		writeErr(w, 429, "rate limited")
		return
	}
	tok := r.PathValue("token")
	if len(tok) != 48 {
		writeErr(w, 404, "not found")
		return
	}
	a, err := s.store.AppByWebhook(r.Context(), tok)
	if err != nil || a.IsPreview() {
		writeErr(w, 404, "not found")
		return
	}

	var ev *gitprovider.Event
	switch {
	case r.Header.Get("X-GitHub-Event") != "":
		body, err := readHookBody(r)
		if err == nil {
			ev, err = gitprovider.ParseGitHub(r.Header.Get("X-GitHub-Event"), githubPayload(r, body))
		}
		if err != nil {
			writeErr(w, 400, "invalid payload")
			return
		}
	case r.Header.Get("X-Gitlab-Event") != "":
		body, err := readHookBody(r)
		if err == nil {
			ev, err = gitprovider.ParseGitLab(r.Header.Get("X-Gitlab-Event"), body)
		}
		if err != nil {
			writeErr(w, 400, "invalid payload")
			return
		}
	}
	if ev != nil {
		if ev.Ignored() {
			writeJSON(w, 200, map[string]string{"skipped": "event " + ev.Name + " ignored; only pushes and pull requests are used"})
			return
		}
		writeJSON(w, 200, map[string]string{"result": s.handleGitEvent(r, a, ev, false)})
		return
	}

	trigger := "webhook"
	if tag := r.URL.Query().Get("tag"); tag != "" {
		if a.Source != "image" {
			writeErr(w, 400, "tag override only applies to image apps")
			return
		}
		if !tagRe.MatchString(tag) {
			writeErr(w, 400, "invalid tag")
			return
		}
		name, _ := docker.SplitTag(a.Image)
		a.Image = name + ":" + tag
		if err := s.store.UpdateApp(r.Context(), a); err != nil {
			s.storeErr(w, err)
			return
		}
		trigger = "webhook:" + tag
	}

	d, err := s.deploy.Deploy(r.Context(), a.ID, trigger)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]int64{"deploymentId": d.ID})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// getAppByName is getApp addressed as /api/services/{project}/{name}, for
// scripts and the CLI.
func (s *Server) getAppByName(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, err := s.store.AppByName(r.Context(), r.PathValue("project"), r.PathValue("name"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	r.SetPathValue("id", strconv.FormatInt(a.ID, 10))
	s.getApp(w, r, u)
}

// patchEnv sets and removes individual environment variables, leaving the
// others as they are. With ?deploy=1 it deploys afterwards.
//
//	{"set": {"KEY": "value"}, "unset": ["OLD"]}
func (s *Server) patchEnv(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if a.IsPreview() {
		writeErr(w, 409, "previews copy their variables from the app they preview")
		return
	}
	var body struct {
		Set   map[string]string `json:"set"`
		Unset []string          `json:"unset"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	lines := []string{}
	for k, v := range body.Set {
		if strings.ContainsAny(v, "\r\n") {
			writeErr(w, 400, k+": values can't contain line breaks")
			return
		}
		lines = append(lines, k+"="+v)
	}
	slices.Sort(lines)
	over := strings.Join(lines, "\n")
	if _, err := deploy.ParseEnv(over); err != nil {
		writeErr(w, 400, "variable names use letters, digits and underscores")
		return
	}
	env := deploy.MergeEnv(a.Env, over)
	if len(body.Unset) > 0 {
		drop := map[string]bool{}
		for _, k := range body.Unset {
			drop[k] = true
		}
		var keep []string
		for _, line := range strings.Split(env, "\n") {
			k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && drop[strings.TrimSpace(k)] {
				continue
			}
			keep = append(keep, line)
		}
		env = strings.Join(keep, "\n")
	}
	if _, err := deploy.ParseEnv(env); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var warn []string
	if r.URL.Query().Get("deploy") == "1" { // check before saving, so a refusal changes nothing
		if warn, ok = s.memoryGuard(w, r, u, a); !ok {
			return
		}
	}
	a.Env = env
	if err := s.store.UpdateApp(r.Context(), a); err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"keys": envKeys(a.Env)}
	if len(warn) > 0 {
		out["warnings"] = warn
	}
	if r.URL.Query().Get("deploy") == "1" {
		d, err := s.deploy.Deploy(r.Context(), a.ID, "env change")
		if err != nil {
			s.storeErr(w, err)
			return
		}
		out["deployment"] = d
	}
	writeJSON(w, 200, out)
}

// publicPort is the port the panel is published on when the installer was
// given another one than 3000 (SP_PORT), or 0.
func publicPort() int {
	n, _ := strconv.Atoi(os.Getenv("SP_PUBLIC_PORT"))
	return n
}
