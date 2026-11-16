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
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

// CreateAPIToken stores a new token and returns its secret value, which is
// only kept as a SHA-256 hash and can't be shown again.
func (s *Store) CreateAPIToken(ctx context.Context, name, scope string) (*APIToken, string, error) {
	secret := TokenPrefix + RandomToken(32)
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(name,token_hash,scope,created_at) VALUES (?,?,?,?)`,
		name, hashToken(secret), scope, t)
	if err != nil {
		return nil, "", mapErr(err)
	}
	id, _ := res.LastInsertId()
	return &APIToken{ID: id, Name: name, Scope: scope, CreatedAt: t}, secret, nil
}

func (s *Store) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,scope,created_at,last_used_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// APITokenBySecret looks a token up by its secret value and records the use.
func (s *Store) APITokenBySecret(ctx context.Context, secret string) (*APIToken, error) {
	var t APIToken
	err := s.db.QueryRowContext(ctx, `SELECT id,name,scope,created_at,last_used_at FROM api_tokens WHERE token_hash=?`,
		hashToken(secret)).Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	n := now()
	if t.LastUsedAt == nil || n-*t.LastUsedAt >= 60 { // limit writes
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, n, t.ID)
		t.LastUsedAt = &n
	}
	return &t, nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuditEntry records one action taken with an API token.
type AuditEntry struct {
	ID        int64  `json:"id"`
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
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(token_id,token_name,action,target,args,ok,error,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, e.TokenID, e.TokenName, e.Action, e.Target, e.Args, e.OK, e.Error, now())
	if err != nil {
		return err
	}
	if id, _ := res.LastInsertId(); id%100 == 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE id <= ?`, id-auditKeep)
	}
	return nil
}

func (s *Store) Audit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,token_id,token_name,action,target,args,ok,error,created_at
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var tid sql.NullInt64
		if err := rows.Scan(&e.ID, &tid, &e.TokenName, &e.Action, &e.Target, &e.Args, &e.OK, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		if tid.Valid {
			e.TokenID = &tid.Int64
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
