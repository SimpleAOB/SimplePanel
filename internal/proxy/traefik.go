// Package proxy renders Traefik dynamic configuration for app domains.
//
// Traefik never gets the Docker socket: SimplePanel writes YAML into a folder
// that Traefik's file provider watches. Only files prefixed "sp-" are managed
// here, so users can add their own Traefik config alongside.
package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Route is one domain pointing at a Swarm service port.
type Route struct {
	Key     string // unique, stable identifier (e.g. "d12")
	Host    string
	Service string // Swarm service name, resolvable on the overlay network
	Port    int
	HTTPS   bool
	// Path, when set instead of Host, routes that path prefix on every
	// host (plain HTTP only), ahead of all host routes. With Host, it routes
	// only that path on that host, ahead of the host's shorter paths.
	Path string
	// StripPath removes Path from the request before it reaches the app.
	StripPath bool
}

// pathPriority puts host+path routes ahead of the plain host route and
// longer paths ahead of shorter ones, below the global check path.
const pathPriority = 10000

const (
	certResolver = "letsencrypt"
	redirectMW   = "sp-redirect-https"
	securityMW   = "sp-security-headers"
	managedFile  = "sp-apps.yml"
	entryHTTP    = "web"
	entryHTTPS   = "websecure"
)

type router struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Service     string   `yaml:"service"`
	Middlewares []string `yaml:"middlewares,omitempty"`
	TLS         *tlsCfg  `yaml:"tls,omitempty"`
	Priority    int      `yaml:"priority,omitempty"`
}

type tlsCfg struct {
	CertResolver string `yaml:"certResolver"`
}

type server struct {
	URL string `yaml:"url"`
}

type service struct {
	LoadBalancer struct {
		Servers []server `yaml:"servers"`
	} `yaml:"loadBalancer"`
}

// Render produces the YAML document for the given routes.
func Render(routes []Route) ([]byte, error) {
	routers := map[string]router{}
	services := map[string]service{}
	middlewares := map[string]any{
		redirectMW: map[string]any{"redirectScheme": map[string]any{"scheme": "https", "permanent": true}},
		securityMW: map[string]any{"headers": map[string]any{
			"stsSeconds":         31536000,
			"contentTypeNosniff": true,
			"referrerPolicy":     "strict-origin-when-cross-origin",
		}},
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Key < routes[j].Key })
	for _, r := range routes {
		svcName := "sp-" + r.Key
		var s service
		s.LoadBalancer.Servers = []server{{URL: fmt.Sprintf("http://%s:%d", r.Service, r.Port)}}
		services[svcName] = s
		if r.Host == "" && r.Path != "" {
			routers["sp-"+r.Key+"-http"] = router{
				Rule: fmt.Sprintf("PathPrefix(`%s`)", r.Path), EntryPoints: []string{entryHTTP}, Service: svcName, Priority: 1 << 20,
			}
			continue
		}
		rule := fmt.Sprintf("Host(`%s`)", r.Host)
		prio := 0
		var extra []string
		if r.Path != "" {
			// Path(/api) || PathPrefix(/api/) matches /api and /api/x but
			// not /apix, which a bare PathPrefix(/api) would.
			rule += fmt.Sprintf(" && (Path(`%s`) || PathPrefix(`%s/`))", r.Path, r.Path)
			prio = pathPriority + len(r.Path)
			if r.StripPath {
				mw := "sp-" + r.Key + "-strip"
				middlewares[mw] = map[string]any{"stripPrefix": map[string]any{"prefixes": []string{r.Path}}}
				extra = append(extra, mw)
			}
		}
		if r.HTTPS {
			routers["sp-"+r.Key+"-https"] = router{
				Rule: rule, EntryPoints: []string{entryHTTPS}, Service: svcName,
				Middlewares: append([]string{securityMW}, extra...), TLS: &tlsCfg{CertResolver: certResolver},
				Priority: prio,
			}
			routers["sp-"+r.Key+"-http"] = router{
				Rule: rule, EntryPoints: []string{entryHTTP}, Service: svcName,
				Middlewares: []string{redirectMW}, Priority: prio,
			}
		} else {
			routers["sp-"+r.Key+"-http"] = router{
				Rule: rule, EntryPoints: []string{entryHTTP}, Service: svcName,
				Middlewares: extra, Priority: prio,
			}
		}
	}
	doc := map[string]any{
		"http": map[string]any{
			"middlewares": middlewares,
			"routers":     routers,
			"services":    services,
		},
	}
	if len(routers) == 0 {
		// Traefik rejects empty router/service maps; keep only middlewares.
		delete(doc["http"].(map[string]any), "routers")
		delete(doc["http"].(map[string]any), "services")
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Managed by SimplePanel. Edits will be overwritten.\n# Add your own Traefik config in a separate file without the sp- prefix.\n"), b...), nil
}

// Write atomically writes the rendered routes into dir.
func Write(dir string, routes []Route) error {
	b, err := Render(routes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sp-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, managedFile))
}
