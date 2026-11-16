package deploy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// Docker reference grammar, simplified: [host[:port]/]path[:tag][@digest]
	imageRe  = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-]{1,2}[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	labelRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidName checks project and app names: lowercase letters, digits and
// single hyphens, 1-30 chars. Underscores are reserved as the separator in
// Swarm service names.
func ValidName(s string) error {
	if !nameRe.MatchString(s) || strings.Contains(s, "--") {
		return errors.New("use 1-30 lowercase letters, numbers or single hyphens")
	}
	return nil
}

// ValidImage checks a Docker image reference.
func ValidImage(s string) error {
	if len(s) > 255 || !imageRe.MatchString(s) {
		return errors.New("not a valid image reference (example: nginx:1.27 or ghcr.io/org/app:latest)")
	}
	return nil
}

// ValidGitURL accepts only https URLs without embedded credentials.
func ValidGitURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" || u.Path == "/" {
		return errors.New("use an https:// Git URL, e.g. https://github.com/user/repo")
	}
	if u.User != nil {
		return errors.New("don't put credentials in the URL; use the access token field")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Git URL must not contain a query or fragment")
	}
	return nil
}

// ValidBranch checks a branch name and rejects option-like values.
func ValidBranch(s string) error {
	if s == "" {
		return nil
	}
	if !branchRe.MatchString(s) || strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return errors.New("invalid branch name")
	}
	return nil
}

// CleanRelPath normalises a path that must stay inside the repository.
func CleanRelPath(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return "", errors.New("path must be relative to the repository root")
	}
	c := path.Clean(s)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", errors.New("path must stay inside the repository")
	}
	if c == "." {
		return "", nil
	}
	return c, nil
}

// ValidHost checks a domain name (no IPs, no wildcards, no ports).
func ValidHost(s string) error {
	if len(s) > 253 || net.ParseIP(s) != nil {
		return errors.New("enter a domain name like app.example.com")
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return errors.New("enter a full domain name like app.example.com")
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return errors.New("enter a domain name like app.example.com (lowercase)")
		}
	}
	return nil
}

// ValidPort checks a TCP port number.
func ValidPort(p int) error {
	if p < 1 || p > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// ParseEnv parses KEY=VALUE lines. Blank lines and # comments are ignored.
func ParseEnv(s string) ([]string, error) {
	var out []string
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("env line %d: expected KEY=value", i+1)
		}
		out = append(out, k+"="+strings.SplitN(line, "=", 2)[1])
	}
	return out, nil
}

// ValidVolumePath checks a container mount path.
func ValidVolumePath(p string) error {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
		return errors.New("mount path must be an absolute path like /data")
	}
	return nil
}
