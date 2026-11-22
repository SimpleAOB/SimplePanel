package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Export/import format. Secrets are never exported: Git tokens and webhook
// tokens are left out, and database credentials are regenerated on import.
// Environment variables of apps are included only when asked for, because
// they often hold API keys.

const exportVersion = 1

const maxImportServices = 50

type exportDoc struct {
	SimplePanel int            `json:"simplepanel"`
	Kind        string         `json:"kind"` // "project" | "service"
	ExportedAt  string         `json:"exportedAt,omitempty"`
	Project     *exportProject `json:"project,omitempty"`
	Service     *exportService `json:"service,omitempty"`
}

type exportProject struct {
	Name     string          `json:"name"`
	Services []exportService `json:"services"`
}

type exportService struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"` // "app" | "postgres" | "mysql" | "mariadb" | "mongo" | "redis"
	Source     string         `json:"source,omitempty"`
	Image      string         `json:"image,omitempty"`
	Git        *exportGit     `json:"git,omitempty"`
	Env        *string        `json:"env,omitempty"`
	Replicas   *int           `json:"replicas,omitempty"`
	CPULimit   float64        `json:"cpuLimit,omitempty"`
	MemoryMB   int            `json:"memoryMb,omitempty"`
	Command    string         `json:"command,omitempty"`
	Volumes    []store.Volume `json:"volumes,omitempty"`
	Ports      []store.Port   `json:"ports,omitempty"`
	Domains    []exportDomain `json:"domains,omitempty"`
	GitPrivate bool           `json:"gitPrivate,omitempty"` // a token was set; add it again after import
	// Registry is the host of a private image's registry ("ghcr.io"). The
	// login itself is never exported; on import the app uses a saved login
	// for that registry if there is exactly one.
	Registry   string `json:"registry,omitempty"`
	AutoDomain *bool  `json:"autoDomain,omitempty"` // only exported when turned off
	HTTPPort   int    `json:"httpPort,omitempty"`
}

type exportGit struct {
	URL        string `json:"url"`
	Branch     string `json:"branch,omitempty"`
	BuildPath  string `json:"buildPath,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	BuildType  string `json:"buildType,omitempty"`
	BuildImage string `json:"buildImage,omitempty"`
	BuildCmd   string `json:"buildCommand,omitempty"`
	OutputDir  string `json:"outputDir,omitempty"`
	SPA        bool   `json:"spa,omitempty"`
}

type exportDomain struct {
	Host      string `json:"host"`
	Path      string `json:"path,omitempty"`
	StripPath bool   `json:"stripPath,omitempty"`
	Port      int    `json:"port"`
	HTTPS     bool   `json:"https"`
}

func (s *Server) exportService(r *http.Request, a *store.App, withEnv bool) (exportService, error) {
	es := exportService{Name: a.Name, Type: orApp(a.Kind), Image: a.Image,
		CPULimit: a.CPULimit, MemoryMB: a.MemoryMB, Command: a.Command, Ports: a.Ports, HTTPPort: a.HTTPPort}
	if !a.AutoDomain && !a.IsDatabase() {
		off := false
		es.AutoDomain = &off
	}
	if a.IsDatabase() {
		e := deploy.EngineByKey(a.Kind)
		if e != nil {
			if extra := strings.TrimSpace(e.StripCredentials(a.Env)); extra != "" {
				es.Env = &extra
			}
		}
		return es, nil
	}
	es.Source = a.Source
	if a.Source == "image" && a.RegistryID != 0 {
		es.Registry = docker.RegistryHost(a.Image)
	}
	replicas := a.Replicas
	es.Replicas = &replicas
	es.Volumes = a.Volumes
	if a.Source == "git" {
		es.Image = ""
		es.Git = &exportGit{URL: a.GitURL, Branch: a.GitBranch, BuildPath: a.BuildPath, Dockerfile: a.Dockerfile,
			BuildImage: a.BuildImage, BuildCmd: a.BuildCommand, OutputDir: a.OutputDir, SPA: a.SPA}
		if a.BuildType != "auto" {
			es.Git.BuildType = a.BuildType
		}
		es.GitPrivate = a.GitToken != ""
	}
	if withEnv && a.Env != "" {
		env := a.Env
		es.Env = &env
	}
	domains, err := s.store.Domains(r.Context(), a.ID)
	if err != nil {
		return es, err
	}
	for _, d := range domains {
		es.Domains = append(es.Domains, exportDomain{Host: d.Host, Path: d.Path, StripPath: d.StripPath, Port: d.Port, HTTPS: d.HTTPS})
	}
	return es, nil
}

func orApp(kind string) string {
	if kind == "" {
		return "app"
	}
	return kind
}

func writeExport(w http.ResponseWriter, doc exportDoc, filename string) {
	doc.SimplePanel = exportVersion
	doc.ExportedAt = time.Now().UTC().Format(time.RFC3339)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.simplepanel.json"`, filename))
	writeJSON(w, 200, doc)
}

func (s *Server) exportProjectHandler(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, id, store.LevelView) {
		return
	}
	p, err := s.store.Project(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	apps, err := s.store.Apps(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	withEnv := r.URL.Query().Get("env") == "1"
	if withEnv && !s.allow(w, r, u, id, store.LevelDeploy) {
		return
	}
	ep := &exportProject{Name: p.Name, Services: []exportService{}}
	for i := range apps {
		es, err := s.exportService(r, &apps[i], withEnv)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		ep.Services = append(ep.Services, es)
	}
	writeExport(w, exportDoc{Kind: "project", Project: ep}, p.Name)
}

func (s *Server) exportAppHandler(w http.ResponseWriter, r *http.Request, u *store.User) {
	withEnv := r.URL.Query().Get("env") == "1"
	need := store.LevelView
	if withEnv {
		need = store.LevelDeploy
	}
	a, ok := s.loadApp(w, r, u, need)
	if !ok {
		return
	}
	es, err := s.exportService(r, a, withEnv)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeExport(w, exportDoc{Kind: "service", Service: &es}, a.Name)
}

// ---- Import ----

type importRequest struct {
	// Document is the pasted export, as JSON text.
	Document string `json:"document"`
	// ProjectID is required when importing a single service.
	ProjectID int64 `json:"projectId"`
	// Name optionally renames the imported project or service.
	Name string `json:"name"`
	// Deploy starts a deployment for each imported service.
	Deploy bool `json:"deploy"`
}

type importResult struct {
	ProjectID int64    `json:"projectId"`
	AppIDs    []int64  `json:"appIds"`
	Warnings  []string `json:"warnings"`
}

// plannedService is a fully validated service waiting to be created.
type plannedService struct {
	app     *store.App
	domains []store.Domain
}

func parseImportDoc(text string) (*exportDoc, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("paste an exported SimplePanel JSON document")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var doc exportDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a valid SimplePanel export: %v", err)
	}
	if dec.More() {
		return nil, errors.New("not a valid SimplePanel export: trailing data after the JSON document")
	}
	if doc.SimplePanel != exportVersion {
		return nil, fmt.Errorf("unsupported export version %d (expected %d)", doc.SimplePanel, exportVersion)
	}
	switch doc.Kind {
	case "project":
		if doc.Project == nil || doc.Service != nil {
			return nil, errors.New(`a "project" export needs a "project" object`)
		}
		if len(doc.Project.Services) > maxImportServices {
			return nil, fmt.Errorf("too many services (max %d)", maxImportServices)
		}
	case "service":
		if doc.Service == nil || doc.Project != nil {
			return nil, errors.New(`a "service" export needs a "service" object`)
		}
	default:
		return nil, errors.New(`"kind" must be "project" or "service"`)
	}
	return &doc, nil
}

// planService validates one exported service and builds the app to create.
func planService(es exportService, projectID int64, projectName string) (*plannedService, error) {
	name := strings.TrimSpace(es.Name)
	if err := deploy.ValidName(name); err != nil {
		return nil, fmt.Errorf("service %q: name: %v", es.Name, err)
	}
	fail := func(err error) (*plannedService, error) { return nil, fmt.Errorf("service %q: %v", name, err) }

	var a *store.App
	switch es.Type {
	case "postgres", "mysql", "mariadb", "mongo", "redis":
		e := deploy.EngineByKey(es.Type)
		if es.Git != nil || es.Source == "git" || len(es.Domains) > 0 || len(es.Volumes) > 0 {
			return fail(errors.New("databases only support image, env, command, cpuLimit, memoryMb and ports"))
		}
		a = deploy.NewDatabaseApp(e, projectID, projectName, name, deploy.DBCredentials{}, store.RandomToken)
		in := appInput{Source: "image", Image: a.Image, Env: a.Env, CPULimit: es.CPULimit, MemoryMB: es.MemoryMB,
			Volumes: a.Volumes, Ports: es.Ports, Command: a.Command}
		if es.Command != "" {
			in.Command = es.Command
		}
		if es.Image != "" {
			in.Image = es.Image
		}
		if es.Env != nil {
			extra := e.StripCredentials(*es.Env) // never accept imported credentials
			if strings.TrimSpace(extra) != "" {
				in.Env = a.Env + strings.TrimSpace(extra) + "\n"
			}
		}
		one := 1
		in.Replicas = &one
		if err := in.apply(a); err != nil {
			return fail(err)
		}
		return &plannedService{app: a}, nil
	case "app", "":
	default:
		return fail(fmt.Errorf("unknown type %q", es.Type))
	}

	in := appInput{Source: es.Source, Image: es.Image, Replicas: es.Replicas, CPULimit: es.CPULimit,
		MemoryMB: es.MemoryMB, Command: es.Command, Volumes: es.Volumes, Ports: es.Ports, AutoDomain: es.AutoDomain}
	if es.HTTPPort != 0 {
		in.HTTPPort = &es.HTTPPort
	}
	if es.Env != nil {
		in.Env = *es.Env
	}
	if es.Git != nil {
		if es.Source != "git" {
			return fail(errors.New(`"git" is only allowed when source is "git"`))
		}
		in.GitURL, in.GitBranch, in.BuildPath, in.Dockerfile = es.Git.URL, es.Git.Branch, es.Git.BuildPath, es.Git.Dockerfile
		in.BuildType, in.BuildImage, in.BuildCommand, in.OutputDir, in.SPA = es.Git.BuildType, es.Git.BuildImage, es.Git.BuildCmd, es.Git.OutputDir, es.Git.SPA
	}
	a = &store.App{ProjectID: projectID, ProjectName: projectName, Name: name, Kind: "app"}
	if err := in.apply(a); err != nil {
		return fail(err)
	}
	ps := &plannedService{app: a}
	seen := map[string]bool{}
	for _, d := range es.Domains {
		host := strings.TrimSpace(strings.ToLower(d.Host))
		if err := deploy.ValidHost(host); err != nil {
			return fail(fmt.Errorf("domain %q: %v", d.Host, err))
		}
		path := deploy.CleanPath(d.Path)
		if err := deploy.ValidPath(path); err != nil {
			return fail(fmt.Errorf("domain %q: %v", d.Host+d.Path, err))
		}
		if d.Port == 0 {
			d.Port = 80
		}
		if err := deploy.ValidPort(d.Port); err != nil {
			return fail(fmt.Errorf("domain %q: %v", d.Host, err))
		}
		if seen[host+path] {
			return fail(fmt.Errorf("domain %q is listed twice", host+path))
		}
		seen[host+path] = true
		ps.domains = append(ps.domains, store.Domain{Host: host, Path: path, StripPath: d.StripPath && path != "", Port: d.Port, HTTPS: d.HTTPS})
	}
	return ps, nil
}

func (s *Server) importHandler(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req importRequest
	if !readJSON(w, r, &req) {
		return
	}
	doc, err := parseImportDoc(req.Document)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	req.Name = strings.TrimSpace(strings.ToLower(req.Name))

	// 1. Decide the target project and validate every service before
	//    creating anything, so a bad document changes nothing.
	var (
		project  *store.Project
		services []exportService
		newName  string
	)
	if doc.Kind == "project" {
		if !requireAdminScope(w, u, store.ScopeManage) {
			return
		}
		newName = doc.Project.Name
		if req.Name != "" {
			newName = req.Name
		}
		if err := deploy.ValidName(newName); err != nil {
			writeErr(w, 400, "project name: "+err.Error())
			return
		}
		services = doc.Project.Services
	} else {
		if req.ProjectID <= 0 {
			writeErr(w, 400, "choose a project to import the service into")
			return
		}
		if !s.allow(w, r, u, req.ProjectID, store.LevelDeploy) {
			return
		}
		if project, err = s.store.Project(ctx, req.ProjectID); err != nil {
			s.storeErr(w, err)
			return
		}
		svc := *doc.Service
		if req.Name != "" {
			svc.Name = req.Name
		}
		services = []exportService{svc}
		newName = project.Name
	}

	plans := make([]*plannedService, 0, len(services))
	names := map[string]bool{}
	for _, es := range services {
		ps, err := planService(es, 0, newName)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if names[ps.app.Name] {
			writeErr(w, 400, fmt.Sprintf("service %q is listed twice", ps.app.Name))
			return
		}
		names[ps.app.Name] = true
		plans = append(plans, ps)
	}
	if project != nil {
		existing, err := s.store.Apps(ctx, project.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		for _, a := range existing {
			if names[a.Name] {
				writeErr(w, 409, fmt.Sprintf("%q already exists in %s; import it under another name", a.Name, project.Name))
				return
			}
		}
	}

	// 2. Create.
	if project == nil {
		project, err = s.store.CreateProject(ctx, newName)
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, fmt.Sprintf("a project named %q already exists; import it under another name", newName))
			return
		}
		if err != nil {
			s.storeErr(w, err)
			return
		}
	}
	res := importResult{ProjectID: project.ID, AppIDs: []int64{}, Warnings: []string{}}
	panelDomain, _ := s.store.Setting(ctx, "panel_domain")
	needsLogin := map[int]bool{}
	for i, ps := range plans {
		if err := s.resolveRegistry(ctx, ps.app, nil); err != nil {
			s.storeErr(w, err)
			return
		}
		if services[i].Registry != "" && ps.app.RegistryID == 0 && ps.app.Source == "image" {
			needsLogin[i] = true
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: private image on %s; add a login under Settings > Registries and choose it before deploying", ps.app.Name, docker.RegistryHost(ps.app.Image)))
		}
	}
	for _, ps := range plans {
		ps.app.ProjectID = project.ID
		if err := s.store.CreateApp(ctx, ps.app); err != nil {
			s.storeErr(w, err)
			return
		}
		res.AppIDs = append(res.AppIDs, ps.app.ID)
		for _, d := range ps.domains {
			if d.Host == panelDomain {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: domain %s is the panel's own domain and was skipped", ps.app.Name, d.Host))
				continue
			}
			d.AppID = ps.app.ID
			if err := s.store.CreateDomain(ctx, &d); err != nil {
				if errors.Is(err, store.ErrConflict) {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s: domain %s is already used by another app and was skipped", ps.app.Name, d.Label()))
					continue
				}
				s.storeErr(w, err)
				return
			}
		}
	}
	for _, es := range services {
		if es.GitPrivate {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: private repository; add an access token in Settings before deploying", es.Name))
		}
	}
	if err := s.deploy.SyncProxy(ctx); err != nil {
		s.log.Error("proxy sync", "err", err)
	}
	if req.Deploy {
		for i, ps := range plans {
			if services[i].GitPrivate || needsLogin[i] {
				continue
			}
			if _, err := s.deploy.Deploy(ctx, ps.app.ID, "import"); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: deploy failed to start: %v", ps.app.Name, err))
			}
		}
	}
	writeJSON(w, 201, res)
}
