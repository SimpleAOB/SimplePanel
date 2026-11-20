package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// DockerHub is the registry host of images without one ("nginx",
// "user/app").
const DockerHub = "docker.io"

// RegistryAuth is a login for a private container registry. Server is a
// registry host as RegistryHost returns it (e.g. "ghcr.io", "docker.io").
type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

type authConfig struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	ServerAddress string `json:"serveraddress"`
}

func (a *RegistryAuth) config() authConfig {
	return authConfig{Username: a.Username, Password: a.Password, ServerAddress: ServerAddress(a.Server)}
}

// Encode returns the value of an X-Registry-Auth header.
func (a *RegistryAuth) Encode() string {
	b, _ := json.Marshal(a.config())
	return base64.URLEncoding.EncodeToString(b)
}

// encodeRegistryConfig returns the value of a build's X-Registry-Config
// header: one login per registry, keyed by its address.
func encodeRegistryConfig(auths []RegistryAuth) string {
	m := map[string]authConfig{}
	for i := range auths {
		c := auths[i].config()
		if _, ok := m[c.ServerAddress]; !ok {
			m[c.ServerAddress] = c
		}
	}
	b, _ := json.Marshal(m)
	return base64.URLEncoding.EncodeToString(b)
}

// ServerAddress is the address Docker expects for a registry host. Docker
// Hub logins use the legacy index address.
func ServerAddress(host string) string {
	if host == DockerHub {
		return "https://index.docker.io/v1/"
	}
	return host
}

// RegistryHost returns the registry an image reference pulls from, using
// Docker's rules: the first path component is a registry only if it has a
// dot or a port, or is "localhost"; otherwise the image is on Docker Hub.
func RegistryHost(ref string) string {
	first, _, ok := strings.Cut(ref, "/")
	if !ok || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return DockerHub
	}
	return canonicalHost(strings.ToLower(first))
}

func canonicalHost(h string) string {
	switch h {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "hub.docker.com":
		return DockerHub
	}
	return h
}

// NormalizeRegistry turns what a user typed for a registry ("ghcr.io",
// "https://index.docker.io/v1/", "registry.example.com:5000") into the host
// RegistryHost would return for its images.
func NormalizeRegistry(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	if h == "" {
		return "", errors.New("enter the registry's address, e.g. ghcr.io")
	}
	name, port, hasPort := strings.Cut(h, ":")
	if hasPort {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", errors.New("the registry's port must be a number between 1 and 65535")
		}
	}
	if len(name) > 253 {
		return "", errors.New("the registry address is too long")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("the registry address must be a host name such as ghcr.io or registry.example.com:5000")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", errors.New("the registry address must be a host name such as ghcr.io or registry.example.com:5000")
			}
		}
	}
	return canonicalHost(h), nil
}

// Login checks a registry login through the Docker daemon, the same way
// `docker login` does. Nothing is stored on the server.
func (c *Client) Login(ctx context.Context, auth RegistryAuth) error {
	// The daemon contacts the registry, which can be slow; that isn't
	// Docker hanging. Callers bound it with their own deadline.
	return c.doJSON(Long(ctx), http.MethodPost, "/auth", nil, auth.config(), nil)
}
