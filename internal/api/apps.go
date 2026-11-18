package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
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
	pd, _ := s.store.Setting(r.Context(), "panel_domain")
	out["panelDomain"] = pd
	writeJSON(w, 200, out)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var body struct {
		PanelDomain    *string `json:"panelDomain"`
		WildcardDomain *string `json:"wildcardDomain"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	out := map[string]string{}
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
		if err := s.deploy.Remove(r.Context(), &apps[i]); err != nil {
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
	for i := range apps {
		if !secrets {
			redactApp(&apps[i])
		}
		out[i] = row{App: apps[i], Status: s.deploy.Status(r.Context(), &apps[i])}
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
		in.GitURL = strings.TrimSpace(in.GitURL)
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
	if in.MemoryMB < 0 || (in.MemoryMB > 0 && in.MemoryMB < 6) {
		return errors.New("memory limit must be 0 (none) or at least 6 MB")
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
		if p.Published == 80 || p.Published == 443 || p.Published == 3000 {
			return fmt.Errorf("port %d is used by SimplePanel; use a domain instead", p.Published)
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
	a := deploy.NewDatabaseApp(e, p.ID, p.Name, in.Name, cred, store.RandomToken)
	if in.Image = strings.TrimSpace(in.Image); in.Image != "" {
		if err := deploy.ValidImage(in.Image); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		a.Image = in.Image
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
	writeJSON(w, 201, map[string]any{"app": a, "deployment": d})
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
	var keys []string
	for _, k := range envKeys(a.Env) {
		keys = append(keys, k+"=")
	}
	a.Env = strings.Join(keys, "\n")
	if a.Env != "" {
		a.Env += "\n"
	}
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
		"status":      s.deploy.Status(r.Context(), a),
		"serviceName": a.ServiceName(),
		"level":       lvl,
		"autoHost":    deploy.AutoHost(a, wd),
		"wildcard":    wd != "" && !a.IsDatabase(),
		"autoPort":    s.deploy.AutoPort(r.Context(), a),
		"backupKind":  deploy.BackupKind(a),
	}
	if lvl >= store.LevelDeploy {
		out["webhookPath"] = "/api/hooks/deploy/" + a.WebhookToken
	}
	if a.IsDatabase() {
		info := deploy.DatabaseInfo(a)
		if lvl < store.LevelDeploy && info != nil {
			info.URL = info.URLTemplate(info.Host)
			info.Password, info.RootPassword = "", ""
		}
		out["database"] = info
		out["aiAccess"], _, _ = s.store.AIAccess(r.Context(), a.ID)
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
	prevAuto, prevPort := a.AutoDomain, a.HTTPPort
	var in appInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.apply(a); err != nil {
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
	a, ok := s.loadApp(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	if err := s.deploy.Remove(r.Context(), a); err != nil {
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
	d, err := s.deploy.Deploy(r.Context(), a.ID, "manual")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, d)
}

func (s *Server) dockerAction(w http.ResponseWriter, err error) {
	if errors.Is(err, docker.ErrNotFound) {
		writeErr(w, 409, "the app isn't deployed yet; deploy it first")
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) restartApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		s.dockerAction(w, s.deploy.Restart(r.Context(), a))
	}
}

func (s *Server) stopApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		s.dockerAction(w, s.deploy.Scale(r.Context(), a, 0))
	}
}

func (s *Server) startApp(w http.ResponseWriter, r *http.Request, u *store.User) {
	if a, ok := s.loadApp(w, r, u, store.LevelDeploy); ok {
		s.dockerAction(w, s.deploy.Scale(r.Context(), a, uint64(max(a.Replicas, 1))))
	}
}

func (s *Server) appLogs(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 || tail > 5000 {
		tail = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	logs, err := s.deploy.Logs(ctx, a, tail, 0)
	if errors.Is(err, docker.ErrNotFound) {
		writeJSON(w, 200, map[string]string{"logs": ""})
		return
	}
	if err != nil && logs == "" {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"logs": logs})
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
	d.Host = strings.TrimSpace(strings.ToLower(d.Host))
	if err := deploy.ValidHost(d.Host); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if d.Port == 0 {
		d.Port = 80
	}
	if err := deploy.ValidPort(d.Port); err != nil {
		writeErr(w, 400, err.Error())
		return
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
			writeErr(w, 409, "that domain is already in use")
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
// GitHub and GitLab push webhooks can point at the same URL; pushes to
// branches other than the app's branch are ignored.
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
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}

	trigger := "webhook"
	if ev, ref, def, isPush := gitEvent(r); ev != "" {
		if !isPush {
			writeJSON(w, 200, map[string]string{"skipped": "event " + ev + " ignored; only pushes deploy"})
			return
		}
		branch := a.GitBranch
		if branch == "" {
			branch = def
		}
		if branch != "" && ref != "refs/heads/"+branch {
			writeJSON(w, 200, map[string]string{"skipped": "push to " + ref + " ignored; app deploys " + branch})
			return
		}
		trigger = "push"
	}

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

// gitEvent recognises GitHub/GitLab webhook deliveries. ev is empty for a
// plain CI/CD call.
func gitEvent(r *http.Request) (ev, ref, defaultBranch string, isPush bool) {
	var payload struct {
		Ref        string `json:"ref"`
		Repository struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"repository"`
		Project struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"project"`
	}
	switch {
	case r.Header.Get("X-GitHub-Event") != "":
		ev = r.Header.Get("X-GitHub-Event")
		isPush = ev == "push"
	case r.Header.Get("X-Gitlab-Event") != "":
		ev = r.Header.Get("X-Gitlab-Event")
		isPush = ev == "Push Hook"
	default:
		return "", "", "", false
	}
	if isPush {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			_ = json.Unmarshal([]byte(r.PostFormValue("payload")), &payload) // GitHub "form" content type
		} else {
			_ = json.NewDecoder(r.Body).Decode(&payload)
		}
		defaultBranch = payload.Repository.DefaultBranch
		if defaultBranch == "" {
			defaultBranch = payload.Project.DefaultBranch
		}
	}
	return ev, payload.Ref, defaultBranch, isPush
}
