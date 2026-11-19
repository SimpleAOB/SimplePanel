package deploy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
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

// ValidGitURL accepts https URLs without embedded credentials, and SSH
// URLs (git@host:owner/repo.git or ssh://git@host[:port]/path) for
// repositories cloned with a deploy key.
func ValidGitURL(s string) error {
	if IsSSHURL(s) {
		if _, _, err := ParseSSHURL(s); err != nil {
			return err
		}
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" || u.Path == "/" {
		return errors.New("use an https:// Git URL (e.g. https://github.com/user/repo), or git@host:user/repo.git with a deploy key")
	}
	if u.User != nil {
		return errors.New("don't put credentials in the URL; use the access token field")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Git URL must not contain a query or fragment")
	}
	return nil
}

var (
	scpURLRe  = regexp.MustCompile(`^([A-Za-z0-9._-]{1,64})@([A-Za-z0-9.-]{1,253}):([A-Za-z0-9._~/-]{1,300})$`)
	sshPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{1,300}$`)
)

// IsSSHURL reports whether s looks like an SSH Git URL.
func IsSSHURL(s string) bool {
	return strings.HasPrefix(s, "ssh://") || (!strings.Contains(s, "://") && strings.Contains(s, "@") && strings.Contains(s, ":"))
}

// ParseSSHURL returns the host (with :port when not 22) and path of an
// SSH Git URL, rejecting anything ssh or git could read as an option.
func ParseSSHURL(s string) (host, repoPath string, err error) {
	bad := errors.New("use an SSH URL like git@github.com:user/repo.git")
	if rest, ok := strings.CutPrefix(s, "ssh://"); ok {
		u, perr := url.Parse("ssh://" + rest)
		if perr != nil || u.User == nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", bad
		}
		if _, set := u.User.Password(); set || !regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(u.User.Username()) {
			return "", "", bad
		}
		if !sshPathRe.MatchString(u.Path) || strings.Contains(u.Path, "..") {
			return "", "", bad
		}
		host = u.Hostname()
		if p := u.Port(); p != "" && p != "22" {
			if n, perr := strconv.Atoi(p); perr != nil || ValidPort(n) != nil {
				return "", "", bad
			}
			host += ":" + p
		}
		repoPath = strings.TrimPrefix(u.Path, "/")
	} else {
		m := scpURLRe.FindStringSubmatch(s)
		if m == nil || strings.Contains(m[3], "..") || strings.HasPrefix(m[3], "-") || strings.HasPrefix(m[3], "/") {
			return "", "", bad
		}
		host, repoPath = m[2], m[3]
	}
	h, _, _ := strings.Cut(host, ":")
	if strings.HasPrefix(h, "-") || strings.HasPrefix(h, ".") || (ValidHost(strings.ToLower(h)) != nil && net.ParseIP(h) == nil && h != "localhost") {
		return "", "", bad
	}
	return host, repoPath, nil
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
// CleanGitURL adds the https:// people leave out when they paste
// "github.com/user/repo", and turns http:// into https://.
func CleanGitURL(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "", IsSSHURL(s), strings.Contains(s, "://"):
		if strings.HasPrefix(s, "http://") {
			return "https://" + strings.TrimPrefix(s, "http://")
		}
		return s
	}
	if host, rest, ok := strings.Cut(s, "/"); ok && strings.Contains(host, ".") && rest != "" {
		return "https://" + s
	}
	return s
}

// CleanHost turns what people paste into a domain field into a bare host
// name: "https://WWW.Example.com/path" becomes "www.example.com".
func CleanHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if h, p, ok := strings.Cut(s, ":"); ok && p != "" && strings.Trim(p, "0123456789") == "" {
		s = h
	}
	return strings.TrimSuffix(s, ".")
}

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
			return errors.New("enter a domain name like app.example.com: letters, numbers, hyphens and dots only")
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
		// Lines copied from a shell script or .env file may start with
		// "export ".
		if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "export "); ok {
			line = rest
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok {
			return nil, fmt.Errorf("env line %d: expected KEY=value, but there is no =", i+1)
		}
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("env line %d: %q isn't a valid name. Use letters, digits and underscores, with no spaces", i+1, k)
		}
		out = append(out, k+"="+unquoteEnv(v))
	}
	return out, nil
}

// unquoteEnv removes one pair of matching outer quotes, as .env files
// write values with spaces: "a b" or 'a b'. Inside double quotes, \" \\
// and \n are unescaped. Anything else is kept exactly as typed.
func unquoteEnv(v string) string {
	t := strings.TrimSpace(v)
	if len(t) < 2 {
		return v
	}
	switch {
	case t[0] == '\'' && t[len(t)-1] == '\'':
		return t[1 : len(t)-1]
	case t[0] == '"' && t[len(t)-1] == '"':
		inner := t[1 : len(t)-1]
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				switch inner[i+1] {
				case 'n':
					b.WriteByte('\n')
					i++
					continue
				case '"', '\\':
					b.WriteByte(inner[i+1])
					i++
					continue
				}
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}
	return v
}

// ValidVolumePath checks a container mount path.
func ValidVolumePath(p string) error {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
		return errors.New("mount path must be an absolute path like /data")
	}
	return nil
}
