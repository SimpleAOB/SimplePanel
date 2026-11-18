package gitprovider

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Service makes API calls to the connected Git providers.
type Service struct {
	HTTP *http.Client
	// GitHubAPI and GitHubWeb point at github.com; tests replace them.
	GitHubAPI string
	GitHubWeb string

	mu     sync.Mutex
	tokens map[string]cachedToken // "<provider>/<installation>" -> token
	insts  map[string]int64       // "<provider>/<repo>" -> installation
}

type cachedToken struct {
	token   string
	expires time.Time
}

func New() *Service {
	return &Service{
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		GitHubAPI: "https://api.github.com",
		GitHubWeb: "https://github.com",
		tokens:    map[string]cachedToken{},
		insts:     map[string]int64{},
	}
}

// Repo is a repository the provider can reach.
type Repo struct {
	FullName      string `json:"fullName"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"cloneUrl"`
	WebURL        string `json:"webUrl"`
}

// APIError is an error response from GitHub or GitLab.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

func isStatus(err error, code int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == code
}

// call sends a JSON request and decodes the JSON answer into out.
func (s *Service) call(ctx context.Context, method, u string, hdr map[string]string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SimplePanel")
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if e.Message != nil {
			msg = fmt.Sprint(e.Message)
		}
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ---- GitHub ----

// ManifestConversion exchanges the code GitHub returns at the end of the
// app manifest flow for the new app's credentials.
func (s *Service) ConvertManifest(ctx context.Context, code string) (*store.GitProvider, error) {
	var out struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	err := s.call(ctx, http.MethodPost, s.GitHubAPI+"/app-manifests/"+url.PathEscape(code)+"/conversions",
		map[string]string{"Accept": "application/vnd.github+json"}, nil, &out)
	if err != nil {
		return nil, err
	}
	if out.ID == 0 || out.PEM == "" || out.WebhookSecret == "" {
		return nil, errors.New("GitHub's answer is missing the app's keys")
	}
	return &store.GitProvider{
		Kind: store.ProviderGitHub, Name: out.Name, AppID: out.ID, Slug: out.Slug, ClientID: out.ClientID,
		ClientSecret: out.ClientSecret, WebhookSecret: out.WebhookSecret, PrivateKey: out.PEM,
		Owner: out.Owner.Login, HTMLURL: out.HTMLURL,
	}, nil
}

// appJWT signs a short-lived JWT that authenticates as the GitHub App.
func appJWT(p *store.GitProvider, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(p.PrivateKey))
	if block == nil {
		return "", errors.New("the GitHub App's private key is not valid PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k8.(*rsa.PrivateKey); ok {
			key = rk
		}
	}
	if key == nil {
		return "", errors.New("the GitHub App's private key is not an RSA key")
	}
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(), // allow for clock drift
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(p.AppID, 10),
	})
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (s *Service) ghApp(ctx context.Context, p *store.GitProvider, method, path string, in, out any) error {
	jwt, err := appJWT(p, time.Now())
	if err != nil {
		return err
	}
	return s.call(ctx, method, s.GitHubAPI+path, map[string]string{
		"Authorization": "Bearer " + jwt, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
	}, in, out)
}

// installationToken returns a cached or fresh installation access token.
func (s *Service) installationToken(ctx context.Context, p *store.GitProvider, inst int64) (string, error) {
	key := fmt.Sprintf("%d/%d", p.ID, inst)
	s.mu.Lock()
	t, ok := s.tokens[key]
	s.mu.Unlock()
	if ok && time.Until(t.expires) > 5*time.Minute {
		return t.token, nil
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := s.ghApp(ctx, p, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst), nil, &out); err != nil {
		return "", fmt.Errorf("GitHub installation token: %w", err)
	}
	s.mu.Lock()
	s.tokens[key] = cachedToken{out.Token, out.ExpiresAt}
	s.mu.Unlock()
	return out.Token, nil
}

// repoInstallation finds the installation of the app that covers repo.
func (s *Service) repoInstallation(ctx context.Context, p *store.GitProvider, repo string, fresh bool) (int64, error) {
	key := fmt.Sprintf("%d/%s", p.ID, strings.ToLower(repo))
	s.mu.Lock()
	id, ok := s.insts[key]
	s.mu.Unlock()
	if ok && !fresh {
		return id, nil
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := s.ghApp(ctx, p, http.MethodGet, "/repos/"+repoPath(repo)+"/installation", nil, &out); err != nil {
		if isStatus(err, 404) {
			return 0, fmt.Errorf("the GitHub App %q isn't installed on %s; install it from Settings → Git", p.Name, repo)
		}
		return 0, err
	}
	s.mu.Lock()
	s.insts[key] = out.ID
	s.mu.Unlock()
	return out.ID, nil
}

// repoToken is an installation token that can act on repo.
func (s *Service) repoToken(ctx context.Context, p *store.GitProvider, repo string) (string, error) {
	inst, err := s.repoInstallation(ctx, p, repo, false)
	if err != nil {
		return "", err
	}
	tok, err := s.installationToken(ctx, p, inst)
	if err != nil && isStatus(err, 404) { // reinstalled: look the installation up again
		if inst, err = s.repoInstallation(ctx, p, repo, true); err == nil {
			tok, err = s.installationToken(ctx, p, inst)
		}
	}
	return tok, err
}

func (s *Service) ghRepo(ctx context.Context, p *store.GitProvider, repo, method, path string, in, out any) error {
	tok, err := s.repoToken(ctx, p, repo)
	if err != nil {
		return err
	}
	return s.call(ctx, method, s.GitHubAPI+"/repos/"+repoPath(repo)+path, map[string]string{
		"Authorization": "Bearer " + tok, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
	}, in, out)
}

// Installation is one account (user or organisation) the GitHub App is
// installed on.
type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	URL     string `json:"url"` // where to change which repositories it can see
}

func (s *Service) installations(ctx context.Context, p *store.GitProvider) ([]Installation, error) {
	var out []struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := s.ghApp(ctx, p, http.MethodGet, "/app/installations?per_page=100", nil, &out); err != nil {
		return nil, err
	}
	res := make([]Installation, len(out))
	for i, x := range out {
		res[i] = Installation{x.ID, x.Account.Login, x.HTMLURL}
	}
	return res, nil
}

// Installations lists where a GitHub App is installed.
func (s *Service) Installations(ctx context.Context, p *store.GitProvider) ([]Installation, error) {
	if p.Kind != store.ProviderGitHub {
		return []Installation{}, nil
	}
	return s.installations(ctx, p)
}

// InstallURL is where the user installs the GitHub App on more accounts or
// repositories.
func (s *Service) InstallURL(p *store.GitProvider) string {
	return s.GitHubWeb + "/apps/" + url.PathEscape(p.Slug) + "/installations/new"
}

// ---- GitLab ----

func gitlabAPI(p *store.GitProvider) string { return strings.TrimRight(p.BaseURL, "/") + "/api/v4" }

func (s *Service) gl(ctx context.Context, p *store.GitProvider, method, path string, in, out any) error {
	return s.call(ctx, method, gitlabAPI(p)+path, map[string]string{"PRIVATE-TOKEN": p.Token}, in, out)
}

func glProject(repo string) string { return "/projects/" + url.PathEscape(repo) }

// CheckGitLab verifies a GitLab URL and token, returning the username.
func (s *Service) CheckGitLab(ctx context.Context, p *store.GitProvider) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := s.gl(ctx, p, http.MethodGet, "/user", nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// ---- Common ----

// repoPath escapes "owner/name" for a GitHub API path.
func repoPath(repo string) string {
	o, n, _ := strings.Cut(repo, "/")
	return url.PathEscape(o) + "/" + url.PathEscape(n)
}

// maxRepos caps repository listings.
const maxRepos = 500

// Repos lists the repositories a provider can reach.
func (s *Service) Repos(ctx context.Context, p *store.GitProvider) ([]Repo, error) {
	out := []Repo{}
	switch p.Kind {
	case store.ProviderGitHub:
		insts, err := s.installations(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, inst := range insts {
			tok, err := s.installationToken(ctx, p, inst.ID)
			if err != nil {
				return nil, err
			}
			for page := 1; page <= 5 && len(out) < maxRepos; page++ {
				var res struct {
					TotalCount   int `json:"total_count"`
					Repositories []struct {
						FullName      string `json:"full_name"`
						DefaultBranch string `json:"default_branch"`
						Private       bool   `json:"private"`
						CloneURL      string `json:"clone_url"`
						HTMLURL       string `json:"html_url"`
					} `json:"repositories"`
				}
				err := s.call(ctx, http.MethodGet, fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", s.GitHubAPI, page),
					map[string]string{"Authorization": "Bearer " + tok, "Accept": "application/vnd.github+json"}, nil, &res)
				if err != nil {
					return nil, err
				}
				for _, r := range res.Repositories {
					out = append(out, Repo{r.FullName, r.DefaultBranch, r.Private, r.CloneURL, r.HTMLURL})
					s.mu.Lock()
					s.insts[fmt.Sprintf("%d/%s", p.ID, strings.ToLower(r.FullName))] = inst.ID
					s.mu.Unlock()
				}
				if len(res.Repositories) < 100 {
					break
				}
			}
		}
	case store.ProviderGitLab:
		for page := 1; page <= 5 && len(out) < maxRepos; page++ {
			var res []struct {
				PathWithNamespace string `json:"path_with_namespace"`
				DefaultBranch     string `json:"default_branch"`
				Visibility        string `json:"visibility"`
				HTTPURLToRepo     string `json:"http_url_to_repo"`
				WebURL            string `json:"web_url"`
			}
			err := s.gl(ctx, p, http.MethodGet, fmt.Sprintf("/projects?membership=true&min_access_level=30&simple=true&archived=false&order_by=last_activity_at&per_page=100&page=%d", page), nil, &res)
			if err != nil {
				return nil, err
			}
			for _, r := range res {
				out = append(out, Repo{r.PathWithNamespace, r.DefaultBranch, r.Visibility != "public", r.HTTPURLToRepo, r.WebURL})
			}
			if len(res) < 100 {
				break
			}
		}
	}
	return out, nil
}

// Branches lists a repository's branches (up to 100).
func (s *Service) Branches(ctx context.Context, p *store.GitProvider, repo string) ([]string, error) {
	var res []struct {
		Name string `json:"name"`
	}
	var err error
	switch p.Kind {
	case store.ProviderGitHub:
		err = s.ghRepo(ctx, p, repo, http.MethodGet, "/branches?per_page=100", nil, &res)
	case store.ProviderGitLab:
		err = s.gl(ctx, p, http.MethodGet, glProject(repo)+"/repository/branches?per_page=100", nil, &res)
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, len(res))
	for i, b := range res {
		out[i] = b.Name
	}
	return out, nil
}

// RepoInfo looks one repository up.
func (s *Service) RepoInfo(ctx context.Context, p *store.GitProvider, repo string) (*Repo, error) {
	switch p.Kind {
	case store.ProviderGitHub:
		var r struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
			CloneURL      string `json:"clone_url"`
			HTMLURL       string `json:"html_url"`
		}
		if err := s.ghRepo(ctx, p, repo, http.MethodGet, "", nil, &r); err != nil {
			return nil, err
		}
		return &Repo{r.FullName, r.DefaultBranch, r.Private, r.CloneURL, r.HTMLURL}, nil
	case store.ProviderGitLab:
		var r struct {
			PathWithNamespace string `json:"path_with_namespace"`
			DefaultBranch     string `json:"default_branch"`
			Visibility        string `json:"visibility"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
			WebURL            string `json:"web_url"`
		}
		if err := s.gl(ctx, p, http.MethodGet, glProject(repo), nil, &r); err != nil {
			return nil, err
		}
		return &Repo{r.PathWithNamespace, r.DefaultBranch, r.Visibility != "public", r.HTTPURLToRepo, r.WebURL}, nil
	}
	return nil, errors.New("unknown provider")
}

// CloneToken returns a token that can clone repo over HTTPS.
func (s *Service) CloneToken(ctx context.Context, p *store.GitProvider, repo string) (string, error) {
	switch p.Kind {
	case store.ProviderGitHub:
		return s.repoToken(ctx, p, repo)
	case store.ProviderGitLab:
		return p.Token, nil
	}
	return "", errors.New("unknown provider")
}

// Commit status states.
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
)

// SetStatus reports a deployment on a commit. name tells statuses of
// different services apart.
func (s *Service) SetStatus(ctx context.Context, p *store.GitProvider, repo, sha, state, name, targetURL, desc string) error {
	if len(desc) > 140 {
		desc = desc[:137] + "..."
	}
	switch p.Kind {
	case store.ProviderGitHub:
		body := map[string]string{"state": state, "context": name, "description": desc}
		if targetURL != "" {
			body["target_url"] = targetURL
		}
		return s.ghRepo(ctx, p, repo, http.MethodPost, "/statuses/"+url.PathEscape(sha), body, nil)
	case store.ProviderGitLab:
		gs := map[string]string{StatePending: "running", StateSuccess: "success", StateFailure: "failed"}[state]
		body := map[string]string{"state": gs, "name": name, "description": desc}
		if targetURL != "" {
			body["target_url"] = targetURL
		}
		err := s.gl(ctx, p, http.MethodPost, glProject(repo)+"/statuses/"+url.PathEscape(sha), body, nil)
		if isStatus(err, 400) && state == StatePending {
			return nil // GitLab refuses a repeated "running"
		}
		return err
	}
	return nil
}

// UpsertComment writes a comment on a pull/merge request, editing the
// earlier one when its id is known. It returns the comment's id.
func (s *Service) UpsertComment(ctx context.Context, p *store.GitProvider, repo string, pr int, commentID, body string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	switch p.Kind {
	case store.ProviderGitHub:
		if commentID != "" {
			err := s.ghRepo(ctx, p, repo, http.MethodPatch, "/issues/comments/"+url.PathEscape(commentID), map[string]string{"body": body}, &out)
			if err == nil {
				return commentID, nil
			}
			if !isStatus(err, 404) {
				return commentID, err
			}
		}
		err := s.ghRepo(ctx, p, repo, http.MethodPost, fmt.Sprintf("/issues/%d/comments", pr), map[string]string{"body": body}, &out)
		if err != nil {
			return "", err
		}
	case store.ProviderGitLab:
		base := fmt.Sprintf("%s/merge_requests/%d/notes", glProject(repo), pr)
		if commentID != "" {
			err := s.gl(ctx, p, http.MethodPut, base+"/"+url.PathEscape(commentID), map[string]string{"body": body}, &out)
			if err == nil {
				return commentID, nil
			}
			if !isStatus(err, 404) {
				return commentID, err
			}
		}
		if err := s.gl(ctx, p, http.MethodPost, base, map[string]string{"body": body}, &out); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	return strconv.FormatInt(out.ID, 10), nil
}

// EnsureHook makes sure a GitLab project sends push and merge request
// events to hookURL, authenticated with the provider's webhook secret.
// GitHub Apps get their events without per-repository hooks.
func (s *Service) EnsureHook(ctx context.Context, p *store.GitProvider, repo, hookURL string) error {
	if p.Kind != store.ProviderGitLab {
		return nil
	}
	var hooks []struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := s.gl(ctx, p, http.MethodGet, glProject(repo)+"/hooks?per_page=100", nil, &hooks); err != nil {
		return fmt.Errorf("listing the project's webhooks (the token needs the api scope and Maintainer role): %w", err)
	}
	body := map[string]any{
		"url": hookURL, "token": p.WebhookSecret, "push_events": true, "merge_requests_events": true,
		"enable_ssl_verification": strings.HasPrefix(hookURL, "https://"),
	}
	for _, h := range hooks {
		if h.URL == hookURL {
			// Update it so the secret matches.
			return s.gl(ctx, p, http.MethodPut, fmt.Sprintf("%s/hooks/%d", glProject(repo), h.ID), body, nil)
		}
	}
	return s.gl(ctx, p, http.MethodPost, glProject(repo)+"/hooks", body, nil)
}

// RepoCloneURL is the HTTPS clone URL of a repository.
func (s *Service) RepoCloneURL(p *store.GitProvider, repo string) string {
	switch p.Kind {
	case store.ProviderGitHub:
		return s.GitHubWeb + "/" + repo + ".git"
	case store.ProviderGitLab:
		return strings.TrimRight(p.BaseURL, "/") + "/" + repo + ".git"
	}
	return ""
}
