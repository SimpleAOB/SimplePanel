// Package store persists SimplePanel's desired state in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique constraint violations.
var ErrConflict = errors.New("already exists")

type Store struct{ db *sql.DB }

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialise writers, avoids SQLITE_BUSY
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE projects (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE apps (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		source TEXT NOT NULL,
		image TEXT NOT NULL DEFAULT '',
		git_url TEXT NOT NULL DEFAULT '',
		git_branch TEXT NOT NULL DEFAULT '',
		git_token TEXT NOT NULL DEFAULT '',
		build_path TEXT NOT NULL DEFAULT '',
		dockerfile TEXT NOT NULL DEFAULT '',
		env TEXT NOT NULL DEFAULT '',
		replicas INTEGER NOT NULL DEFAULT 1,
		cpu_limit REAL NOT NULL DEFAULT 0,
		memory_mb INTEGER NOT NULL DEFAULT 0,
		command TEXT NOT NULL DEFAULT '',
		volumes TEXT NOT NULL DEFAULT '[]',
		ports TEXT NOT NULL DEFAULT '[]',
		webhook_token TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(project_id, name)
	);
	CREATE TABLE domains (
		id INTEGER PRIMARY KEY,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		host TEXT NOT NULL UNIQUE,
		port INTEGER NOT NULL,
		https INTEGER NOT NULL DEFAULT 1
	);
	CREATE TABLE deployments (
		id INTEGER PRIMARY KEY,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		status TEXT NOT NULL,
		trigger TEXT NOT NULL,
		image TEXT NOT NULL DEFAULT '',
		log TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		finished_at INTEGER
	);
	CREATE INDEX deployments_app ON deployments(app_id, id DESC);`,
	// v2: databases are apps with a non-"app" kind (postgres, mysql, mariadb, mongo, redis).
	`ALTER TABLE apps ADD COLUMN kind TEXT NOT NULL DEFAULT 'app';`,
	// v3: static site builds for Git apps.
	`ALTER TABLE apps ADD COLUMN build_type TEXT NOT NULL DEFAULT 'auto';
	ALTER TABLE apps ADD COLUMN build_image TEXT NOT NULL DEFAULT '';
	ALTER TABLE apps ADD COLUMN build_command TEXT NOT NULL DEFAULT '';
	ALTER TABLE apps ADD COLUMN output_dir TEXT NOT NULL DEFAULT '';
	ALTER TABLE apps ADD COLUMN spa INTEGER NOT NULL DEFAULT 0;`,
	// v4: API tokens for the MCP endpoint, and an audit log of what they did.
	`CREATE TABLE api_tokens (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		scope TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER
	);
	CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY,
		token_id INTEGER,
		token_name TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		args TEXT NOT NULL DEFAULT '',
		ok INTEGER NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE INDEX audit_log_created ON audit_log(id DESC);
	ALTER TABLE apps ADD COLUMN ai_access INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE apps ADD COLUMN ai_password TEXT NOT NULL DEFAULT '';`,
	// v5 (SimplePanel 0.2): users and roles, auto subdomains, monitoring,
	// notifications and backups.
	`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'owner';
	ALTER TABLE users ADD COLUMN all_projects INTEGER NOT NULL DEFAULT 1;
	CREATE TABLE project_access (
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		PRIMARY KEY (user_id, project_id)
	);
	CREATE TABLE invites (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);
	ALTER TABLE api_tokens ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE;
	UPDATE api_tokens SET user_id = (SELECT MIN(id) FROM users);
	ALTER TABLE audit_log ADD COLUMN user_id INTEGER;
	ALTER TABLE audit_log ADD COLUMN username TEXT NOT NULL DEFAULT '';
	ALTER TABLE audit_log ADD COLUMN source TEXT NOT NULL DEFAULT 'mcp';

	ALTER TABLE apps ADD COLUMN auto_domain INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE apps ADD COLUMN http_port INTEGER NOT NULL DEFAULT 0;

	CREATE TABLE metrics (
		target INTEGER NOT NULL,
		res INTEGER NOT NULL,
		ts INTEGER NOT NULL,
		cpu REAL NOT NULL,
		mem INTEGER NOT NULL,
		mem_limit INTEGER NOT NULL,
		net_rx REAL NOT NULL,
		net_tx REAL NOT NULL,
		disk_used INTEGER NOT NULL DEFAULT 0,
		disk_total INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (target, res, ts)
	) WITHOUT ROWID;
	CREATE INDEX metrics_ts ON metrics(res, ts);

	CREATE TABLE notify_channels (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config TEXT NOT NULL,
		events TEXT NOT NULL,
		projects TEXT NOT NULL DEFAULT '[]',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE notify_log (
		id INTEGER PRIMARY KEY,
		channel_id INTEGER NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
		event TEXT NOT NULL,
		title TEXT NOT NULL,
		ok INTEGER NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE INDEX notify_log_channel ON notify_log(channel_id, id DESC);

	CREATE TABLE backup_destinations (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		endpoint TEXT NOT NULL DEFAULT '',
		region TEXT NOT NULL DEFAULT '',
		bucket TEXT NOT NULL,
		prefix TEXT NOT NULL DEFAULT '',
		access_key TEXT NOT NULL,
		secret_key TEXT NOT NULL,
		path_style INTEGER NOT NULL DEFAULT 0,
		passphrase TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE TABLE backup_jobs (
		id INTEGER PRIMARY KEY,
		app_id INTEGER REFERENCES apps(id) ON DELETE CASCADE,
		destination_id INTEGER NOT NULL REFERENCES backup_destinations(id) ON DELETE CASCADE,
		schedule TEXT NOT NULL,
		keep INTEGER NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		next_run_at INTEGER,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE backups (
		id INTEGER PRIMARY KEY,
		app_id INTEGER REFERENCES apps(id) ON DELETE SET NULL,
		job_id INTEGER REFERENCES backup_jobs(id) ON DELETE SET NULL,
		destination_id INTEGER NOT NULL REFERENCES backup_destinations(id) ON DELETE CASCADE,
		project TEXT NOT NULL DEFAULT '',
		service TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		object_key TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		encrypted INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		trigger TEXT NOT NULL,
		log TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		finished_at INTEGER
	);
	CREATE INDEX backups_app ON backups(app_id, id DESC);
	CREATE TABLE restores (
		id INTEGER PRIMARY KEY,
		app_id INTEGER REFERENCES apps(id) ON DELETE SET NULL,
		target_app_id INTEGER REFERENCES apps(id) ON DELETE SET NULL,
		destination_id INTEGER REFERENCES backup_destinations(id) ON DELETE SET NULL,
		object_key TEXT NOT NULL,
		status TEXT NOT NULL,
		log TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		finished_at INTEGER
	);`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(v),0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(v) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil && containsUnique(err.Error()) {
		return ErrConflict
	}
	return err
}

func containsUnique(s string) bool {
	for i := 0; i+6 <= len(s); i++ {
		if s[i:i+6] == "UNIQUE" {
			return true
		}
	}
	return false
}

// RandomToken returns n random bytes hex-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// ---- Sessions ----
// Users, roles and invites live in users.go.

// CreateSession returns a new raw session token; only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	tok := RandomToken(32)
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES (?,?,?)`,
		hashToken(tok), userID, time.Now().Add(ttl).Unix())
	return tok, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM sessions s JOIN users u ON u.id=s.user_id
		 WHERE s.token_hash=? AND s.expires_at>?`, hashToken(token), now()))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, except string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, userID, hashToken(except))
	return err
}

func (s *Store) PruneSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, now())
	return err
}

// ---- Settings ----

func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- Projects ----

type Project struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(ctx context.Context, id int64) (*Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx, `SELECT id,name,created_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.CreatedAt)
	return &p, mapErr(err)
}

func (s *Store) CreateProject(ctx context.Context, name string) (*Project, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO projects(name,created_at) VALUES (?,?)`, name, t)
	if err != nil {
		return nil, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return &Project{ID: id, Name: name, CreatedAt: t}, nil
}

func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	return err
}

// ---- Apps ----

type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Port struct {
	Published uint32 `json:"published"`
	Target    uint32 `json:"target"`
	Protocol  string `json:"protocol"`
}

type App struct {
	ID           int64    `json:"id"`
	ProjectID    int64    `json:"projectId"`
	ProjectName  string   `json:"projectName"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`   // "app" | "postgres" | "mysql" | "mariadb" | "mongo" | "redis"
	Source       string   `json:"source"` // "image" | "git"
	Image        string   `json:"image"`
	GitURL       string   `json:"gitUrl"`
	GitBranch    string   `json:"gitBranch"`
	GitToken     string   `json:"-"`
	HasGitToken  bool     `json:"hasGitToken"`
	BuildPath    string   `json:"buildPath"`
	Dockerfile   string   `json:"dockerfile"`
	BuildType    string   `json:"buildType"` // "auto" | "dockerfile" | "static"
	BuildImage   string   `json:"buildImage"`
	BuildCommand string   `json:"buildCommand"`
	OutputDir    string   `json:"outputDir"`
	SPA          bool     `json:"spa"`
	Env          string   `json:"env"`
	Replicas     int      `json:"replicas"`
	CPULimit     float64  `json:"cpuLimit"`
	MemoryMB     int      `json:"memoryMb"`
	Command      string   `json:"command"`
	Volumes      []Volume `json:"volumes"`
	Ports        []Port   `json:"ports"`
	WebhookToken string   `json:"-"`
	AutoDomain   bool     `json:"autoDomain"` // gets <name>-<project>.<wildcard domain>
	HTTPPort     int      `json:"httpPort"`   // port the auto domain routes to; 0 = from domains or 80
	CreatedAt    int64    `json:"createdAt"`
	UpdatedAt    int64    `json:"updatedAt"`
}

// ServiceName is the Swarm service name for the app. Names are limited to
// [a-z0-9-], so the underscore separator cannot be ambiguous.
func (a *App) ServiceName() string { return a.ProjectName + "_" + a.Name }

// IsDatabase reports whether the app is a one-click database.
func (a *App) IsDatabase() bool { return a.Kind != "" && a.Kind != "app" }

const appCols = `a.id,a.project_id,p.name,a.name,a.kind,a.source,a.image,a.git_url,a.git_branch,a.git_token,
	a.build_path,a.dockerfile,a.env,a.replicas,a.cpu_limit,a.memory_mb,a.command,a.volumes,a.ports,
	a.webhook_token,a.created_at,a.updated_at,a.build_type,a.build_image,a.build_command,a.output_dir,a.spa,
	a.auto_domain,a.http_port`

type scanner interface{ Scan(...any) error }

func scanApp(r scanner) (*App, error) {
	var a App
	var vols, ports string
	err := r.Scan(&a.ID, &a.ProjectID, &a.ProjectName, &a.Name, &a.Kind, &a.Source, &a.Image, &a.GitURL, &a.GitBranch,
		&a.GitToken, &a.BuildPath, &a.Dockerfile, &a.Env, &a.Replicas, &a.CPULimit, &a.MemoryMB, &a.Command,
		&vols, &ports, &a.WebhookToken, &a.CreatedAt, &a.UpdatedAt,
		&a.BuildType, &a.BuildImage, &a.BuildCommand, &a.OutputDir, &a.SPA, &a.AutoDomain, &a.HTTPPort)
	if err != nil {
		return nil, mapErr(err)
	}
	a.HasGitToken = a.GitToken != ""
	_ = json.Unmarshal([]byte(vols), &a.Volumes)
	_ = json.Unmarshal([]byte(ports), &a.Ports)
	if a.Volumes == nil {
		a.Volumes = []Volume{}
	}
	if a.Ports == nil {
		a.Ports = []Port{}
	}
	return &a, nil
}

func (s *Store) Apps(ctx context.Context, projectID int64) ([]App, error) {
	q := `SELECT ` + appCols + ` FROM apps a JOIN projects p ON p.id=a.project_id`
	args := []any{}
	if projectID > 0 {
		q += ` WHERE a.project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY p.name, a.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) App(ctx context.Context, id int64) (*App, error) {
	return scanApp(s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps a JOIN projects p ON p.id=a.project_id WHERE a.id=?`, id))
}

func (s *Store) AppByWebhook(ctx context.Context, token string) (*App, error) {
	return scanApp(s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps a JOIN projects p ON p.id=a.project_id WHERE a.webhook_token=?`, token))
}

func (s *Store) CreateApp(ctx context.Context, a *App) error {
	vols, _ := json.Marshal(a.Volumes)
	ports, _ := json.Marshal(a.Ports)
	t := now()
	a.WebhookToken = RandomToken(24)
	if a.Kind == "" {
		a.Kind = "app"
	}
	if a.BuildType == "" {
		a.BuildType = "auto"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO apps(project_id,name,kind,source,image,git_url,git_branch,git_token,
		build_path,dockerfile,env,replicas,cpu_limit,memory_mb,command,volumes,ports,webhook_token,created_at,updated_at,
		build_type,build_image,build_command,output_dir,spa,auto_domain,http_port)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ProjectID, a.Name, a.Kind, a.Source, a.Image, a.GitURL, a.GitBranch, a.GitToken, a.BuildPath, a.Dockerfile,
		a.Env, a.Replicas, a.CPULimit, a.MemoryMB, a.Command, string(vols), string(ports), a.WebhookToken, t, t,
		a.BuildType, a.BuildImage, a.BuildCommand, a.OutputDir, a.SPA, a.AutoDomain, a.HTTPPort)
	if err != nil {
		return mapErr(err)
	}
	a.ID, _ = res.LastInsertId()
	a.CreatedAt, a.UpdatedAt = t, t
	return nil
}

// UpdateApp saves all editable fields. The name and project are immutable.
func (s *Store) UpdateApp(ctx context.Context, a *App) error {
	vols, _ := json.Marshal(a.Volumes)
	ports, _ := json.Marshal(a.Ports)
	a.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET source=?,image=?,git_url=?,git_branch=?,git_token=?,build_path=?,
		dockerfile=?,env=?,replicas=?,cpu_limit=?,memory_mb=?,command=?,volumes=?,ports=?,updated_at=?,
		build_type=?,build_image=?,build_command=?,output_dir=?,spa=?,auto_domain=?,http_port=? WHERE id=?`,
		a.Source, a.Image, a.GitURL, a.GitBranch, a.GitToken, a.BuildPath, a.Dockerfile, a.Env, a.Replicas,
		a.CPULimit, a.MemoryMB, a.Command, string(vols), string(ports), a.UpdatedAt,
		orAuto(a.BuildType), a.BuildImage, a.BuildCommand, a.OutputDir, a.SPA, a.AutoDomain, a.HTTPPort, a.ID)
	return mapErr(err)
}

func (s *Store) RotateWebhook(ctx context.Context, id int64) (string, error) {
	tok := RandomToken(24)
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET webhook_token=? WHERE id=?`, tok, id)
	return tok, err
}

func (s *Store) DeleteApp(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id=?`, id)
	return err
}

// ---- Domains ----

type Domain struct {
	ID    int64  `json:"id"`
	AppID int64  `json:"appId"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	HTTPS bool   `json:"https"`
}

// DomainRoute is a domain joined with its app's service name, for the proxy.
type DomainRoute struct {
	Domain
	Service string
}

func (s *Store) Domains(ctx context.Context, appID int64) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,host,port,https FROM domains WHERE app_id=? ORDER BY host`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.AppID, &d.Host, &d.Port, &d.HTTPS); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AllRoutes(ctx context.Context) ([]DomainRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.app_id,d.host,d.port,d.https,p.name||'_'||a.name
		FROM domains d JOIN apps a ON a.id=d.app_id JOIN projects p ON p.id=a.project_id ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainRoute{}
	for rows.Next() {
		var d DomainRoute
		if err := rows.Scan(&d.ID, &d.AppID, &d.Host, &d.Port, &d.HTTPS, &d.Service); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) CreateDomain(ctx context.Context, d *Domain) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO domains(app_id,host,port,https) VALUES (?,?,?,?)`, d.AppID, d.Host, d.Port, d.HTTPS)
	if err != nil {
		return mapErr(err)
	}
	d.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) Domain(ctx context.Context, id int64) (*Domain, error) {
	var d Domain
	err := s.db.QueryRowContext(ctx, `SELECT id,app_id,host,port,https FROM domains WHERE id=?`, id).
		Scan(&d.ID, &d.AppID, &d.Host, &d.Port, &d.HTTPS)
	return &d, mapErr(err)
}

func (s *Store) DeleteDomain(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM domains WHERE id=?`, id)
	return err
}

// ---- Deployments ----

type Deployment struct {
	ID         int64  `json:"id"`
	AppID      int64  `json:"appId"`
	Status     string `json:"status"` // queued|running|succeeded|failed
	Trigger    string `json:"trigger"`
	Image      string `json:"image"`
	Log        string `json:"log,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	FinishedAt *int64 `json:"finishedAt"`
}

func (s *Store) CreateDeployment(ctx context.Context, appID int64, trigger string) (*Deployment, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO deployments(app_id,status,trigger,created_at) VALUES (?,?,?,?)`,
		appID, "queued", trigger, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Deployment{ID: id, AppID: appID, Status: "queued", Trigger: trigger, CreatedAt: t}, nil
}

func (s *Store) UpdateDeployment(ctx context.Context, id int64, status, image, log string, finished bool) error {
	var fin any
	if finished {
		fin = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deployments SET status=?,image=?,log=?,finished_at=COALESCE(?,finished_at) WHERE id=?`,
		status, image, log, fin, id)
	return err
}

func (s *Store) Deployments(ctx context.Context, appID int64, limit int) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,status,trigger,image,created_at,finished_at
		FROM deployments WHERE app_id=? ORDER BY id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Status, &d.Trigger, &d.Image, &d.CreatedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Deployment(ctx context.Context, id int64) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRowContext(ctx, `SELECT id,app_id,status,trigger,image,log,created_at,finished_at FROM deployments WHERE id=?`, id).
		Scan(&d.ID, &d.AppID, &d.Status, &d.Trigger, &d.Image, &d.Log, &d.CreatedAt, &d.FinishedAt)
	return &d, mapErr(err)
}

// FailStaleDeployments marks deployments interrupted by a restart as failed.
func (s *Store) FailStaleDeployments(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployments SET status='failed', log=log||char(10)||'[interrupted: SimplePanel restarted]'||char(10),
		finished_at=? WHERE status IN ('queued','running')`, now())
	return err
}

func orAuto(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}
