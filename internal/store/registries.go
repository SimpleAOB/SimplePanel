package store

import "context"

// Registry is a saved login for a private container registry (GHCR,
// Docker Hub, GitLab, or any other). The password is never sent to the
// browser, the API, exports or logs.
type Registry struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Server    string `json:"server"` // registry host, e.g. "ghcr.io" or "docker.io"
	Username  string `json:"username"`
	Password  string `json:"-"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const registryCols = `id,name,server,username,password,created_at,updated_at`

func scanRegistry(r scanner) (*Registry, error) {
	var g Registry
	if err := r.Scan(&g.ID, &g.Name, &g.Server, &g.Username, &g.Password, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &g, nil
}

// Registries lists the saved registry logins by name.
func (s *Store) Registries(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+registryCols+` FROM registries ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registry{}
	for rows.Next() {
		g, err := scanRegistry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *Store) Registry(ctx context.Context, id int64) (*Registry, error) {
	return scanRegistry(s.db.QueryRowContext(ctx, `SELECT `+registryCols+` FROM registries WHERE id=?`, id))
}

func (s *Store) CreateRegistry(ctx context.Context, g *Registry) error {
	g.CreatedAt = now()
	g.UpdatedAt = g.CreatedAt
	res, err := s.db.ExecContext(ctx, `INSERT INTO registries(name,server,username,password,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		g.Name, g.Server, g.Username, g.Password, g.CreatedAt, g.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	g.ID, _ = res.LastInsertId()
	return nil
}

// UpdateRegistry saves a login's fields, including the password (callers
// keep the old one when the user leaves it blank).
func (s *Store) UpdateRegistry(ctx context.Context, g *Registry) error {
	g.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE registries SET name=?,server=?,username=?,password=?,updated_at=? WHERE id=?`,
		g.Name, g.Server, g.Username, g.Password, g.UpdatedAt, g.ID)
	return mapErr(err)
}

// DeleteRegistry removes a login. Apps that used it pull without a login
// from their next deploy.
func (s *Store) DeleteRegistry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE id=?`, id)
	return err
}

// RegistryUsage counts the apps (including previews) that use each login.
func (s *Store) RegistryUsage(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT registry_id, COUNT(*) FROM apps WHERE registry_id IS NOT NULL GROUP BY registry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ClearRegistryFromApps unlinks a login from every app, for when it now
// points at another registry.
func (s *Store) ClearRegistryFromApps(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET registry_id=NULL WHERE registry_id=?`, id)
	return err
}
