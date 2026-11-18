package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Roles. Owner and Admin can do everything on every project; Owner is the
// first account and can't be removed or demoted. Developer and Viewer get
// access to all projects or to selected ones, and a project can override
// the role (for example Viewer everywhere, Developer on one project).
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleViewer    = "viewer"
)

// Access levels on a project, from least to most.
const (
	LevelNone   = 0
	LevelView   = 1 // see services, logs, metrics and backups; no secrets
	LevelDeploy = 2 // also create, configure, deploy, back up, restore into a new database
	LevelManage = 3 // also delete and restore over existing data
)

// ValidRole reports whether r is a role a user can be given.
func ValidRole(r string) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer:
		return true
	}
	return false
}

// RoleLevel is the project access level a role gives.
func RoleLevel(role string) int {
	switch role {
	case RoleOwner, RoleAdmin:
		return LevelManage
	case RoleDeveloper:
		return LevelDeploy
	case RoleViewer:
		return LevelView
	}
	return LevelNone
}

// ScopeLevel is the highest project level an API token scope allows.
func ScopeLevel(scope string) int {
	switch scope {
	case ScopeRead:
		return LevelView
	case ScopeManage:
		return LevelDeploy
	case ScopeFull:
		return LevelManage
	}
	return LevelNone
}

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	AllProjects bool   `json:"allProjects"`
	Pending     bool   `json:"pending"` // invited, no password set yet
	// DBQuery lets a Developer or Viewer use the database Query tab
	// (read-only). Owners and admins can always.
	DBQuery   bool  `json:"dbQuery"`
	CreatedAt int64 `json:"createdAt"`

	// Scope caps what the request may do when it comes from an API token
	// (empty for browser sessions).
	Scope string `json:"-"`
	// Token is the API token behind the request, if any.
	Token *APIToken `json:"-"`
}

// CanQueryDatabases reports whether the user may use the database Query
// tab at all (project access is checked separately).
func (u *User) CanQueryDatabases() bool {
	return u != nil && (u.Role == RoleOwner || u.Role == RoleAdmin || u.DBQuery)
}

// IsAdmin reports whether the user may change server settings and users.
func (u *User) IsAdmin() bool {
	return u != nil && (u.Role == RoleOwner || u.Role == RoleAdmin) && (u.Scope == "" || u.Scope == ScopeFull)
}

const userCols = `u.id,u.username,u.role,u.all_projects,u.password_hash='',u.created_at,u.db_query`

func scanUser(r scanner) (*User, error) {
	var u User
	if err := r.Scan(&u.ID, &u.Username, &u.Role, &u.AllProjects, &u.Pending, &u.CreatedAt, &u.DBQuery); err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	return n, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
}

// CreateFirstUser creates the owner only if no user exists yet (atomic).
func (s *Store) CreateFirstUser(ctx context.Context, username, hash string) (*User, error) {
	t := now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(username,password_hash,created_at,role,all_projects)
		 SELECT ?,?,?,'owner',1 WHERE NOT EXISTS (SELECT 1 FROM users)`, username, hash, t)
	if err != nil {
		return nil, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrConflict
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, Role: RoleOwner, AllProjects: true, CreatedAt: t}, nil
}

// CreateUser adds an invited user without a password.
func (s *Store) CreateUser(ctx context.Context, username, role string, allProjects bool) (*User, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(username,password_hash,created_at,role,all_projects) VALUES (?,'',?,?,?)`,
		username, t, role, allProjects)
	if err != nil {
		return nil, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, Role: role, AllProjects: allProjects, Pending: true, CreatedAt: t}, nil
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users u ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) User(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id=?`, id))
}

func (s *Store) UserByName(ctx context.Context, username string) (*User, string, error) {
	var hash string
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.username=?`, username))
	if err != nil {
		return nil, "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, u.ID).Scan(&hash)
	return u, hash, mapErr(err)
}

func (s *Store) UserPasswordHash(ctx context.Context, id int64) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, id).Scan(&hash)
	return hash, mapErr(err)
}

func (s *Store) SetUserPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, hash, id)
	return err
}

// SetUserDBQuery sets the database query permission.
func (s *Store) SetUserDBQuery(ctx context.Context, id int64, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET db_query=? WHERE id=?`, on, id)
	return err
}

// UpdateUserRole changes a user's role and project access in one step.
// access maps project id to role and replaces the user's overrides.
func (s *Store) UpdateUserRole(ctx context.Context, id int64, role string, allProjects bool, access map[int64]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET role=?, all_projects=? WHERE id=?`, role, allProjects, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_access WHERE user_id=?`, id); err != nil {
		return err
	}
	for pid, r := range access {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_access(user_id,project_id,role) VALUES (?,?,?)`, id, pid, r); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProjectAccess returns a user's per-project role overrides.
func (s *Store) ProjectAccess(ctx context.Context, userID int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, role FROM project_access WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var pid int64
		var r string
		if err := rows.Scan(&pid, &r); err != nil {
			return nil, err
		}
		out[pid] = r
	}
	return out, rows.Err()
}

// ProjectLevel is the access level u has on a project, before any token
// scope is applied.
func (s *Store) ProjectLevel(ctx context.Context, u *User, projectID int64) int {
	if u == nil {
		return LevelNone
	}
	if u.Role == RoleOwner || u.Role == RoleAdmin {
		return LevelManage
	}
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM project_access WHERE user_id=? AND project_id=?`, u.ID, projectID).Scan(&r)
	if err == nil {
		return RoleLevel(r)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return LevelNone
	}
	if u.AllProjects {
		return RoleLevel(u.Role)
	}
	return LevelNone
}

// ---- Invites and password resets ----

const (
	InviteKind = "invite"
	ResetKind  = "reset"
)

// CreateInvite returns a one-time token that lets the user set a password.
// Older tokens for the same user are replaced.
func (s *Store) CreateInvite(ctx context.Context, userID int64, kind string, ttl time.Duration) (string, error) {
	tok := RandomToken(24)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM invites WHERE user_id=?`, userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invites(token_hash,user_id,kind,expires_at) VALUES (?,?,?,?)`,
		hashToken(tok), userID, kind, time.Now().Add(ttl).Unix()); err != nil {
		return "", err
	}
	return tok, tx.Commit()
}

// InviteUser looks up the user an unexpired invite token belongs to.
func (s *Store) InviteUser(ctx context.Context, token string) (*User, string, error) {
	var kind string
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, kind FROM invites WHERE token_hash=? AND expires_at>?`, hashToken(token), now()).Scan(&uid, &kind)
	if err != nil {
		return nil, "", mapErr(err)
	}
	u, err := s.User(ctx, uid)
	return u, kind, err
}

// UseInvite sets the password for the invite's user and burns the token.
func (s *Store) UseInvite(ctx context.Context, token, hash string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM invites WHERE token_hash=? AND expires_at>?`, hashToken(token), now()).Scan(&uid)
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM invites WHERE user_id=?`, uid); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, hash, uid); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, uid); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.User(ctx, uid)
}

// PruneInvites removes expired invite tokens.
func (s *Store) PruneInvites(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE expires_at<=?`, now())
	return err
}
