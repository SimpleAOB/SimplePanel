package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Reading what an image declares (ENV and EXPOSE) before it is deployed,
// so the New App form can show the image's settings. The image config is
// read straight from the registry (the same manifest and config blob
// `docker pull` fetches, without the layers), so nothing is downloaded to
// the server's disk.

// EnvVar is one variable an image sets with ENV.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ImageInfo is what an image declares about how it runs.
type ImageInfo struct {
	Env   []EnvVar `json:"env"`
	Ports []int    `json:"ports"`
}

// ErrImageNotFound means the registry has no such image or tag.
var ErrImageNotFound = errors.New("the registry has no image with that name and tag")

// ErrRegistryDenied means the registry wants a login (or a better one).
var ErrRegistryDenied = errors.New("the registry refused access")

const (
	maxManifestBytes = 4 << 20
	maxConfigBytes   = 8 << 20
	maxImageEnv      = 300
	maxEnvValue      = 1000
)

var imageEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ImageConfig reads ENV and EXPOSE from an image already on this server.
func (c *Client) ImageConfig(ctx context.Context, ref string) (*ImageInfo, error) {
	var img struct {
		Config struct {
			Env          []string
			ExposedPorts map[string]struct{}
		}
	}
	if err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, &img); err != nil {
		return nil, err
	}
	return &ImageInfo{Env: parseImageEnv(img.Config.Env), Ports: ParseExposedPorts(img.Config.ExposedPorts)}, nil
}

func parseImageEnv(list []string) []EnvVar {
	out := []EnvVar{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		if !imageEnvKeyRe.MatchString(k) {
			continue
		}
		if len(v) > maxEnvValue {
			v = v[:maxEnvValue]
		}
		out = append(out, EnvVar{Key: k, Value: v})
		if len(out) == maxImageEnv {
			break
		}
	}
	return out
}

// RegistryClient reads image configs from registries over HTTPS.
type RegistryClient struct {
	http *http.Client

	mu    sync.Mutex
	cache map[string]cachedInfo
}

type cachedInfo struct {
	info *ImageInfo
	at   time.Time
}

// cacheFor keeps lookups for a while: Docker Hub counts manifest requests
// against its pull limit, and people retype the same image.
const cacheFor = 15 * time.Minute

// NewRegistryClient returns a client that only connects over HTTPS and
// never to this server itself or to link-local addresses (such as a cloud
// metadata service), whatever an image name or a redirect says.
func NewRegistryClient() *RegistryClient {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: refuseInternal}
	tr := &http.Transport{
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
	}
	return newRegistryClient(&http.Client{Transport: tr})
}

func newRegistryClient(hc *http.Client) *RegistryClient {
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("the registry redirected to a non-HTTPS address")
		}
		return nil
	}
	return &RegistryClient{http: hc, cache: map[string]cachedInfo{}}
}

func refuseInternal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("refusing to connect to %s", host)
	}
	return nil
}

// imageRef is an image reference split into what the registry API needs.
type imageRef struct {
	host      string // as RegistryHost returns it
	apiHost   string // where the API is served
	repo      string // e.g. library/nginx
	reference string // tag or digest
}

func parseImageRef(ref string) (imageRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsAny(ref, " \t\r\n?#\\") {
		return imageRef{}, errors.New("that doesn't look like an image name")
	}
	r := imageRef{host: RegistryHost(ref)}
	name := ref
	if first, rest, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		name = rest
	}
	if n, digest, ok := strings.Cut(name, "@"); ok {
		name, r.reference = n, digest
		if i := strings.LastIndex(name, ":"); i >= 0 {
			name = name[:i] // a tag next to a digest is ignored, as Docker does
		}
	} else if i := strings.LastIndex(name, ":"); i >= 0 {
		name, r.reference = name[:i], name[i+1:]
	}
	if r.reference == "" {
		r.reference = "latest"
	}
	name = strings.ToLower(name)
	if name == "" || strings.Contains(name, "//") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "..") {
		return imageRef{}, errors.New("that doesn't look like an image name")
	}
	r.apiHost = r.host
	if r.host == DockerHub {
		r.apiHost = "registry-1.docker.io"
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
	}
	r.repo = name
	return r, nil
}

var (
	indexTypes = []string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}
	manifestTypes = []string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}
)

// ImageInfo reads an image's ENV and EXPOSE from its registry. auth, when
// set, must be the login for the image's registry; it is sent only there
// and to the token service the registry names, as `docker pull` does.
func (rc *RegistryClient) ImageInfo(ctx context.Context, ref string, auth *RegistryAuth) (*ImageInfo, error) {
	r, err := parseImageRef(ref)
	if err != nil {
		return nil, err
	}
	if auth != nil && auth.Server != r.host {
		return nil, fmt.Errorf("the login is for %s, but the image is on %s", auth.Server, r.host)
	}
	key := r.host + "/" + r.repo + "@" + r.reference
	if auth != nil {
		key += "#" + auth.Username
	}
	rc.mu.Lock()
	c, ok := rc.cache[key]
	rc.mu.Unlock()
	if ok && time.Since(c.at) < cacheFor {
		return c.info, nil
	}

	s := &registrySession{rc: rc, ref: r, auth: auth}
	body, ctype, err := s.get(ctx, "manifests/"+r.reference, append(append([]string{}, indexTypes...), manifestTypes...), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	var m struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, errors.New("the registry sent a manifest SimplePanel can't read")
	}
	if len(m.Manifests) > 0 || isOneOf(ctype, indexTypes) || isOneOf(m.MediaType, indexTypes) {
		pick := ""
		for _, d := range m.Manifests {
			if d.Platform.OS == "linux" && d.Platform.Architecture == runtime.GOARCH {
				pick = d.Digest
				break
			}
		}
		if pick == "" {
			for _, d := range m.Manifests {
				if d.Platform.OS == "linux" && d.Platform.Architecture != "unknown" {
					pick = d.Digest
					break
				}
			}
		}
		if !validDigest(pick) {
			return nil, errors.New("the image has no Linux version")
		}
		body, _, err = s.get(ctx, "manifests/"+pick, manifestTypes, maxManifestBytes)
		if err != nil {
			return nil, err
		}
		m.Config.Digest = ""
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, errors.New("the registry sent a manifest SimplePanel can't read")
		}
	}
	if !validDigest(m.Config.Digest) {
		return nil, errors.New("the image uses an old manifest format SimplePanel can't read; it will still deploy")
	}
	body, _, err = s.get(ctx, "blobs/"+m.Config.Digest, nil, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Config struct {
			Env          []string
			ExposedPorts map[string]struct{}
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, errors.New("the registry sent an image config SimplePanel can't read")
	}
	info := &ImageInfo{Env: parseImageEnv(cfg.Config.Env), Ports: ParseExposedPorts(cfg.Config.ExposedPorts)}
	if info.Ports == nil {
		info.Ports = []int{}
	}
	rc.mu.Lock()
	if len(rc.cache) > 500 {
		rc.cache = map[string]cachedInfo{}
	}
	rc.cache[key] = cachedInfo{info: info, at: time.Now()}
	rc.mu.Unlock()
	return info, nil
}

var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validDigest(d string) bool { return digestRe.MatchString(d) }

func isOneOf(ctype string, list []string) bool {
	ctype, _, _ = strings.Cut(ctype, ";")
	for _, t := range list {
		if strings.TrimSpace(ctype) == t {
			return true
		}
	}
	return false
}

// registrySession is one lookup: it remembers the token the registry gave.
type registrySession struct {
	rc    *RegistryClient
	ref   imageRef
	auth  *RegistryAuth
	authz string // Authorization header to send to the registry
}

func (s *registrySession) get(ctx context.Context, path string, accept []string, limit int64) ([]byte, string, error) {
	u := "https://" + s.ref.apiHost + "/v2/" + s.ref.repo + "/" + path
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		if len(accept) > 0 {
			req.Header.Set("Accept", strings.Join(accept, ", "))
		}
		if s.authz != "" {
			req.Header.Set("Authorization", s.authz)
		}
		res, err := s.rc.http.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("couldn't reach %s: %w", s.ref.host, unwrapURLError(err))
		}
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := res.Header.Get("WWW-Authenticate")
			drain(res)
			if err := s.authorize(ctx, challenge); err != nil {
				return nil, "", err
			}
			continue
		}
		defer drain(res)
		switch {
		case res.StatusCode == http.StatusNotFound:
			return nil, "", ErrImageNotFound
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
			return nil, "", ErrRegistryDenied
		case res.StatusCode == http.StatusTooManyRequests:
			return nil, "", errors.New("the registry's rate limit was reached; try again later")
		case res.StatusCode/100 != 2:
			return nil, "", fmt.Errorf("the registry answered HTTP %d", res.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil {
			return nil, "", fmt.Errorf("reading from %s: %w", s.ref.host, err)
		}
		if int64(len(b)) > limit {
			return nil, "", errors.New("the registry's answer was too large")
		}
		return b, res.Header.Get("Content-Type"), nil
	}
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func drain(res *http.Response) {
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
}

// authorize answers a 401 challenge: Basic with the login, or Bearer with
// a token from the registry's token service (anonymous without a login).
func (s *registrySession) authorize(ctx context.Context, challenge string) error {
	scheme, params := parseChallenge(challenge)
	switch scheme {
	case "basic":
		if s.auth == nil {
			return ErrRegistryDenied
		}
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(s.auth.Username, s.auth.Password)
		s.authz = req.Header.Get("Authorization")
		return nil
	case "bearer":
	default:
		return ErrRegistryDenied
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return errors.New("the registry's sign-in address isn't HTTPS")
	}
	q := realm.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", "repository:"+s.ref.repo+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	if s.auth != nil {
		req.SetBasicAuth(s.auth.Username, s.auth.Password)
	}
	res, err := s.rc.http.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach %s to sign in: %w", realm.Host, unwrapURLError(err))
	}
	defer drain(res)
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrRegistryDenied
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("signing in to %s failed (HTTP %d)", s.ref.host, res.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil {
		return fmt.Errorf("signing in to %s failed: unreadable answer", s.ref.host)
	}
	t := tok.Token
	if t == "" {
		t = tok.AccessToken
	}
	if t == "" || strings.ContainsAny(t, "\r\n") {
		return ErrRegistryDenied
	}
	s.authz = "Bearer " + t
	return nil
}

// parseChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(h string) (string, map[string]string) {
	h = strings.TrimSpace(h)
	scheme, rest, _ := strings.Cut(h, " ")
	params := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		after = strings.TrimLeft(after, " ")
		var v string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				v, rest = after[1:], ""
			} else {
				v, rest = after[1:1+end], after[2+end:]
			}
		} else {
			v, rest, _ = strings.Cut(after, ",")
		}
		params[k] = v
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return strings.ToLower(scheme), params
}
