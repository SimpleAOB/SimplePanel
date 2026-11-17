package store

import (
	"context"
	"database/sql"
)

// Token scopes, from least to most powerful. Each scope includes the ones
// before it.
const (
	ScopeRead   = "read"   // view projects, services, logs and deployments
	ScopeManage = "manage" // also create, configure and deploy
	ScopeFull   = "full"   // also delete, and read secrets when asked
)

// ScopeRank orders scopes; unknown scopes rank 0 and allow nothing.
func ScopeRank(scope string) int {
	switch scope {
	case ScopeRead:
		return 1
	case ScopeManage:
		return 2
	case ScopeFull:
		return 3
	}
	return 0
}

// TokenPrefix marks SimplePanel API tokens so they are easy to recognise
// (and to find with secret scanners) if they leak.
const TokenPrefix = "sp_"

type APIToken struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	UserID     int64  `json:"userId"`
	Username   string `json:"username"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

const tokenCols = `t.id,t.name,t.scope,COALESCE(t.user_id,0),COALESCE(u.username,''),t.created_at,t.last_used_at`

func scanToken(r scanner) (*APIToken, error) {
	var t APIToken
	if err := r.Scan(&t.ID, &t.Name, &t.Scope, &t.UserID, &t.Username, &t.CreatedAt, &t.LastUsedAt); err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

// CreateAPIToken stores a new token and returns its secret value, which is
// only kept as a SHA-256 hash and can't be shown again.
func (s *Store) CreateAPIToken(ctx context.Context, name, scope string, user *User) (*APIToken, string, error) {
	secret := TokenPrefix + RandomToken(32)
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(name,token_hash,scope,created_at,user_id) VALUES (?,?,?,?,?)`,
		name, hashToken(secret), scope, t, user.ID)
	if err != nil {
		return nil, "", mapErr(err)
	}
	id, _ := res.LastInsertId()
	return &APIToken{ID: id, Name: name, Scope: scope, UserID: user.ID, Username: user.Username, CreatedAt: t}, secret, nil
}

// APITokens lists tokens; userID 0 lists everyone's.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens t LEFT JOIN users u ON u.id=t.user_id
		WHERE ?=0 OR t.user_id=? ORDER BY t.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// APITokenBySecret looks a token up by its secret value, records the use,
// and returns the user it acts for with the token's scope applied.
func (s *Store) APITokenBySecret(ctx context.Context, secret string) (*APIToken, *User, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens t LEFT JOIN users u ON u.id=t.user_id
		WHERE t.token_hash=?`, hashToken(secret)))
	if err != nil {
		return nil, nil, err
	}
	u, err := s.User(ctx, t.UserID)
	if err != nil {
		return nil, nil, ErrNotFound // a token without a user does nothing
	}
	u.Scope = t.Scope
	n := now()
	if t.LastUsedAt == nil || n-*t.LastUsedAt >= 60 { // limit writes
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, n, t.ID)
		t.LastUsedAt = &n
	}
	return t, u, nil
}

// DeleteAPIToken removes a token; a non-zero userID only removes that
// user's own token.
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=? AND (?=0 OR user_id=?)`, id, userID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuditEntry records one change, made in the UI ("ui"), with an API token
// ("mcp") or by SimplePanel itself ("system").
type AuditEntry struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	UserID    *int64 `json:"userId"`
	Username  string `json:"username"`
	TokenID   *int64 `json:"tokenId"`
	TokenName string `json:"tokenName"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Args      string `json:"args"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	CreatedAt int64  `json:"createdAt"`
}

// auditKeep is how many audit entries are kept; older ones are pruned.
const auditKeep = 5000

func (s *Store) AddAudit(ctx context.Context, e *AuditEntry) error {
	if e.Source == "" {
		e.Source = "mcp"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(token_id,token_name,action,target,args,ok,error,created_at,user_id,username,source)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, e.TokenID, e.TokenName, e.Action, e.Target, e.Args, e.OK, e.Error, now(), e.UserID, e.Username, e.Source)
	if err != nil {
		return err
	}
	if id, _ := res.LastInsertId(); id%100 == 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE id <= ?`, id-auditKeep)
	}
	return nil
}

// Audit lists recent entries. userID limits it to one user's actions and
// source to "ui" or "mcp"; zero values list everything.
func (s *Store) Audit(ctx context.Context, limit int, userID int64, source string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,token_id,token_name,action,target,args,ok,error,created_at,user_id,username,source
		FROM audit_log WHERE (?=0 OR user_id=?) AND (?='' OR source=?) ORDER BY id DESC LIMIT ?`, userID, userID, source, source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var tid, uid sql.NullInt64
		if err := rows.Scan(&e.ID, &tid, &e.TokenName, &e.Action, &e.Target, &e.Args, &e.OK, &e.Error, &e.CreatedAt, &uid, &e.Username, &e.Source); err != nil {
			return nil, err
		}
		if tid.Valid {
			e.TokenID = &tid.Int64
		}
		if uid.Valid {
			e.UserID = &uid.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AIAccess reports whether AI read-only access is on for a database, and
// the read-only user's password. The password never leaves the server.
func (s *Store) AIAccess(ctx context.Context, appID int64) (bool, string, error) {
	var on bool
	var pw string
	err := s.db.QueryRowContext(ctx, `SELECT ai_access, ai_password FROM apps WHERE id=?`, appID).Scan(&on, &pw)
	return on, pw, mapErr(err)
}

func (s *Store) SetAIAccess(ctx context.Context, appID int64, on bool, password string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET ai_access=?, ai_password=? WHERE id=?`, on, password, appID)
	return err
}
