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
	Key   string `json:"key"`
	Label string `json:"label"`
	Image string `json:"image"`
	Port  int    `json:"port"`
	// CustomUser is true if the user can choose the username.
	CustomUser bool `json:"customUser"`
	// AIAccess is true if the engine supports AI read-only access (SQL
	// databases only).
	AIAccess bool   `json:"aiAccess"`
	DataPath string `json:"-"`
	Scheme   string `json:"-"`
	// Command is the default command for new databases of this engine,
	// for engines whose image can't be configured with environment
	// variables alone. It must not contain secrets: it is shown in
	// Settings and exported.
	Command string `json:"-"`
	userVar string
	passVar string
	dbVar   string
	rootVar string
	// fixedUser and fixedDB are shown when the engine has no user or
	// database variable (Redis).
	fixedUser string
	fixedDB   string
	urlQuery  string
}

// Engines lists the supported databases. Images are official Docker Hub
// images pinned to a major version; users can change the tag in Settings.
var Engines = []Engine{
	{Key: "postgres", Label: "PostgreSQL", Image: "postgres:17-alpine", Port: 5432, DataPath: "/var/lib/postgresql/data",
		Scheme: "postgres", CustomUser: true, AIAccess: true, userVar: "POSTGRES_USER", passVar: "POSTGRES_PASSWORD", dbVar: "POSTGRES_DB"},
	{Key: "mysql", Label: "MySQL", Image: "mysql:8.4", Port: 3306, DataPath: "/var/lib/mysql",
		Scheme: "mysql", CustomUser: true, AIAccess: true, userVar: "MYSQL_USER", passVar: "MYSQL_PASSWORD", dbVar: "MYSQL_DATABASE", rootVar: "MYSQL_ROOT_PASSWORD"},
	{Key: "mariadb", Label: "MariaDB", Image: "mariadb:11.4", Port: 3306, DataPath: "/var/lib/mysql",
		Scheme: "mysql", CustomUser: true, AIAccess: true, userVar: "MARIADB_USER", passVar: "MARIADB_PASSWORD", dbVar: "MARIADB_DATABASE", rootVar: "MARIADB_ROOT_PASSWORD"},
	// The official image creates this user as MongoDB's administrator, in
	// the admin database; MONGO_INITDB_DATABASE only names the default
	// database in the connection URL.
	{Key: "mongo", Label: "MongoDB", Image: "mongo:8.0", Port: 27017, DataPath: "/data/db",
		Scheme: "mongodb", CustomUser: true, userVar: "MONGO_INITDB_ROOT_USERNAME", passVar: "MONGO_INITDB_ROOT_PASSWORD",
		dbVar: "MONGO_INITDB_DATABASE", urlQuery: "authSource=admin"},
	// The Redis image has no password variable, so the command passes it
	// from REDIS_PASSWORD. Append-only persistence keeps every write
	// (fsync once a second) instead of periodic snapshots only.
	{Key: "redis", Label: "Redis", Image: "redis:8-alpine", Port: 6379, DataPath: "/data",
		Scheme: "redis", passVar: "REDIS_PASSWORD", fixedUser: "default", fixedDB: "0",
		Command: `sh -c 'exec docker-entrypoint.sh redis-server --appendonly yes --requirepass "$REDIS_PASSWORD"'`},
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
		if !e.CustomUser {
			return errors.New("username: " + e.Label + " has no username to choose; it only uses a password")
		}
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
	var env []string
	if e.userVar != "" {
		env = append(env, e.userVar+"="+user)
	}
	env = append(env, e.passVar+"="+pass)
	if e.dbVar != "" {
		env = append(env, e.dbVar+"="+ident)
	}
	if e.rootVar != "" {
		env = append(env, e.rootVar+"="+randomHex(16))
	}
	return &store.App{
		ProjectID: projectID, ProjectName: projectName, Name: name, Kind: e.Key,
		Source: "image", Image: e.Image, Env: strings.Join(env, "\n") + "\n", Replicas: 1, Command: e.Command,
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
		User: e.fixedUser, Password: vars[e.passVar], Database: e.fixedDB,
	}
	if e.userVar != "" {
		info.User = vars[e.userVar]
	}
	if e.dbVar != "" {
		info.Database = vars[e.dbVar]
	}
	if e.rootVar != "" {
		info.RootPassword = vars[e.rootVar]
	}
	u := url.URL{
		Scheme:   e.Scheme,
		User:     url.UserPassword(info.User, info.Password),
		Host:     fmt.Sprintf("%s:%d", info.Host, info.Port),
		Path:     "/" + info.Database,
		RawQuery: e.urlQuery,
	}
	info.URL = u.String()
	return info
}

// ProjectNetwork is the private overlay network shared by a project's apps
// and databases. Databases are attached only to this network, so they are
// unreachable from other projects and from the proxy.
func ProjectNetwork(project string) string { return "sp_" + project }

// URLTemplate is the connection URL with a placeholder instead of the
// password, for host.
func (info *DBInfo) URLTemplate(host string) string {
	scheme, rest, _ := strings.Cut(info.URL, "://")
	_, query, _ := strings.Cut(rest, "?")
	u := fmt.Sprintf("%s://%s:<password>@%s:%d/%s", scheme, info.User, host, info.Port, info.Database)
	if query != "" {
		u += "?" + query
	}
	return u
}

// IsCredentialVar reports whether an environment variable holds a
// credential that SimplePanel generates for this engine.
func (e *Engine) IsCredentialVar(key string) bool {
	if key == "" {
		return false
	}
	switch key {
	case e.userVar, e.passVar, e.dbVar, e.rootVar:
		return true
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
