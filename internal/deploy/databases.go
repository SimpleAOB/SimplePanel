package deploy

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

// DBCredentials optionally sets a new database's user and password. Empty
// fields are generated.
type DBCredentials struct {
	User     string
	Password string
}

var dbUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,31}$`)

// Validate checks user-chosen credentials for engine e.
func (c DBCredentials) Validate(e *Engine) error {
	if c.User != "" {
		if !dbUserRe.MatchString(c.User) {
			return errors.New("username: use up to 32 lowercase letters, numbers and underscores, starting with a letter or underscore")
		}
		if c.User == "root" && e.rootVar != "" {
			return errors.New(`username: "root" is reserved; ` + e.Label + " already has a root user")
		}
		if strings.HasPrefix(c.User, "sp_") {
			return errors.New(`username: names starting with "sp_" are reserved for SimplePanel`)
		}
	}
	if c.Password != "" {
		if len(c.Password) < 8 || len(c.Password) > 128 {
			return errors.New("password: use 8 to 128 characters")
		}
		for _, r := range c.Password {
			if r <= ' ' || r > '~' || strings.ContainsRune("'\"\\`$", r) {
				return errors.New("password: use letters, numbers and symbols, without spaces, quotes, backslashes, backticks or $")
			}
		}
	}
	return nil
}

// NewDatabaseApp returns a ready-to-save app for engine with credentials
// (generated where cred leaves them empty) and a persistent data volume.
// cred must already be validated.
func NewDatabaseApp(e *Engine, projectID int64, projectName, name string, cred DBCredentials, randomHex func(int) string) *store.App {
	ident := strings.ReplaceAll(name, "-", "_")
	user, pass := cred.User, cred.Password
	if user == "" {
		user = ident
	}
	if pass == "" {
		pass = randomHex(16)
	}
	env := []string{
		e.userVar + "=" + user,
		e.passVar + "=" + pass,
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

// IsCredentialVar reports whether an environment variable holds a
// credential that SimplePanel generates for this engine.
func (e *Engine) IsCredentialVar(key string) bool {
	switch key {
	case e.userVar, e.passVar, e.dbVar, e.rootVar:
		return key != ""
	}
	return false
}

// StripCredentials removes generated credential variables from env text.
func (e *Engine) StripCredentials(env string) string {
	var keep []string
	for _, line := range strings.Split(env, "\n") {
		k, _, ok := strings.Cut(line, "=")
		if ok && e.IsCredentialVar(strings.TrimSpace(k)) {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimLeft(strings.Join(keep, "\n"), "\n")
}
