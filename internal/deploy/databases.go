package deploy

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Engine describes a one-click database.
type Engine struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Image    string `json:"image"`
	Port     int    `json:"port"`
	DataPath string `json:"-"`
	Scheme   string `json:"-"`
	userVar  string
	passVar  string
	dbVar    string
	rootVar  string
}

// Engines lists the supported databases. Images are official Docker Hub
// images pinned to a major version; users can change the tag in Settings.
var Engines = []Engine{
	{Key: "postgres", Label: "PostgreSQL", Image: "postgres:17-alpine", Port: 5432, DataPath: "/var/lib/postgresql/data",
		Scheme: "postgres", userVar: "POSTGRES_USER", passVar: "POSTGRES_PASSWORD", dbVar: "POSTGRES_DB"},
	{Key: "mysql", Label: "MySQL", Image: "mysql:8.4", Port: 3306, DataPath: "/var/lib/mysql",
		Scheme: "mysql", userVar: "MYSQL_USER", passVar: "MYSQL_PASSWORD", dbVar: "MYSQL_DATABASE", rootVar: "MYSQL_ROOT_PASSWORD"},
	{Key: "mariadb", Label: "MariaDB", Image: "mariadb:11.4", Port: 3306, DataPath: "/var/lib/mysql",
		Scheme: "mysql", userVar: "MARIADB_USER", passVar: "MARIADB_PASSWORD", dbVar: "MARIADB_DATABASE", rootVar: "MARIADB_ROOT_PASSWORD"},
}

// EngineByKey returns the engine or nil.
func EngineByKey(key string) *Engine {
	for i := range Engines {
		if Engines[i].Key == key {
			return &Engines[i]
		}
	}
	return nil
}

// NewDatabaseApp returns a ready-to-save app for engine with generated
// credentials and a persistent data volume.
func NewDatabaseApp(e *Engine, projectID int64, projectName, name string, randomHex func(int) string) *store.App {
	ident := strings.ReplaceAll(name, "-", "_")
	env := []string{
		e.userVar + "=" + ident,
		e.passVar + "=" + randomHex(16),
		e.dbVar + "=" + ident,
	}
	if e.rootVar != "" {
		env = append(env, e.rootVar+"="+randomHex(16))
	}
	return &store.App{
		ProjectID: projectID, ProjectName: projectName, Name: name, Kind: e.Key,
		Source: "image", Image: e.Image, Env: strings.Join(env, "\n") + "\n", Replicas: 1,
		Volumes: []store.Volume{{Name: "data", Path: e.DataPath}},
		Ports:   []store.Port{},
	}
}

// DBInfo is the connection information shown to the user.
type DBInfo struct {
	Engine       string `json:"engine"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	Password     string `json:"password"`
	Database     string `json:"database"`
	RootPassword string `json:"rootPassword,omitempty"`
	URL          string `json:"url"`
}

// DatabaseInfo derives connection details from the app's environment.
func DatabaseInfo(a *store.App) *DBInfo {
	e := EngineByKey(a.Kind)
	if e == nil {
		return nil
	}
	vars := map[string]string{}
	lines, _ := ParseEnv(a.Env)
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		vars[k] = v
	}
	info := &DBInfo{
		Engine: e.Key, Host: a.ServiceName(), Port: e.Port,
		User: vars[e.userVar], Password: vars[e.passVar], Database: vars[e.dbVar],
	}
	if e.rootVar != "" {
		info.RootPassword = vars[e.rootVar]
	}
	u := url.URL{
		Scheme: e.Scheme,
		User:   url.UserPassword(info.User, info.Password),
		Host:   fmt.Sprintf("%s:%d", info.Host, info.Port),
		Path:   "/" + info.Database,
	}
	info.URL = u.String()
	return info
}

// ProjectNetwork is the private overlay network shared by a project's apps
// and databases. Databases are attached only to this network, so they are
// unreachable from other projects and from the proxy.
func ProjectNetwork(project string) string { return "sp_" + project }
