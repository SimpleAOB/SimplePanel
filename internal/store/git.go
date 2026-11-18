package store

import (
	"context"
)

// Git provider kinds.
const (
	ProviderGitHub = "github" // a GitHub App created with the manifest flow
	ProviderGitLab = "gitlab" // a GitLab access token
)

// GitProvider is a connection to GitHub (as a GitHub App this panel owns)
// or to a GitLab instance (with an access token). Secrets are never sent to
// the browser.
type GitProvider struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	BaseURL       string `json:"baseUrl"` // GitLab: https://gitlab.com; GitHub: empty
	AppID         int64  `json:"appId"`   // GitHub App id
	Slug          string `json:"slug"`    // GitHub App slug
	ClientID      string `json:"-"`
	ClientSecret  string `json:"-"`
	PrivateKey    string `json:"-"` // GitHub App private key (PEM)
	Token         string `json:"-"` // GitLab access token
	WebhookSecret string `json:"-"`
	Owner         string `json:"owner"`
	HTMLURL       string `json:"htmlUrl"`
	CreatedAt     int64  `json:"createdAt"`
}

const providerCols = `id,kind,name,base_url,app_id,slug,client_id,client_secret,private_key,token,webhook_secret,owner,html_url,created_at`

func scanProvider(r scanner) (*GitProvider, error) {
	var p GitProvider
	err := r.Scan(&p.ID, &p.Kind, &p.Name, &p.BaseURL, &p.AppID, &p.Slug, &p.ClientID, &p.ClientSecret,
		&p.PrivateKey, &p.Token, &p.WebhookSecret, &p.Owner, &p.HTMLURL, &p.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func (s *Store) GitProviders(ctx context.Context) ([]GitProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerCols+` FROM git_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitProvider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) GitProvider(ctx context.Context, id int64) (*GitProvider, error) {
	return scanProvider(s.db.QueryRowContext(ctx, `SELECT `+providerCols+` FROM git_providers WHERE id=?`, id))
}

func (s *Store) CreateGitProvider(ctx context.Context, p *GitProvider) error {
	p.CreatedAt = now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO git_providers(kind,name,base_url,app_id,slug,client_id,client_secret,
		private_key,token,webhook_secret,owner,html_url,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Kind, p.Name, p.BaseURL, p.AppID, p.Slug, p.ClientID, p.ClientSecret, p.PrivateKey, p.Token,
		p.WebhookSecret, p.Owner, p.HTMLURL, p.CreatedAt)
	if err != nil {
		return mapErr(err)
	}
	p.ID, _ = res.LastInsertId()
	return nil
}

// UpdateGitProviderToken replaces a GitLab provider's access token.
func (s *Store) UpdateGitProviderToken(ctx context.Context, id int64, name, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE git_providers SET name=?, token=CASE WHEN ?='' THEN token ELSE ? END WHERE id=?`, name, token, token, id)
	return err
}

// DeleteGitProvider removes a provider. Linked apps keep their repository
// URL and stop getting tokens and push events from it.
func (s *Store) DeleteGitProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM git_providers WHERE id=?`, id)
	return err
}

// ProviderAppCount counts the apps linked to a provider.
func (s *Store) ProviderAppCount(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM apps WHERE git_provider_id=? AND parent_app_id IS NULL`, id).Scan(&n)
	return n, err
}

// SetKnownHosts stores the SSH host keys an app's clones have seen.
func (s *Store) SetKnownHosts(ctx context.Context, appID int64, kh string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET ssh_known_hosts=? WHERE id=?`, kh, appID)
	return err
}

// SetDeployKey stores (or with empty values removes) an app's deploy key.
func (s *Store) SetDeployKey(ctx context.Context, appID int64, private, public string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET deploy_key=?, deploy_key_pub=?, ssh_known_hosts='' WHERE id=?`, private, public, appID)
	return err
}
