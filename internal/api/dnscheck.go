package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// A DNS check proves a domain reaches this server: the panel makes up a
// token, asks http://<domain>/.well-known/simplepanel-check/<token>, and
// Traefik sends that path (on any host) back to the panel. It works behind
// NAT and needs no outside service to learn the server's IP address.

var checkTokens = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func newCheckToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	checkTokens.Lock()
	defer checkTokens.Unlock()
	now := time.Now()
	for k, exp := range checkTokens.m {
		if now.After(exp) {
			delete(checkTokens.m, k)
		}
	}
	checkTokens.m[t] = now.Add(time.Minute)
	return t
}

// serveCheck answers a DNS check request (public, no sign-in).
func (s *Server) serveCheck(w http.ResponseWriter, r *http.Request) {
	t := r.PathValue("token")
	checkTokens.Lock()
	exp, ok := checkTokens.m[t]
	checkTokens.Unlock()
	if !ok || time.Now().After(exp) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, t)
}

// cloudflareNets are Cloudflare's proxy ranges (IPv4), to explain the
// orange-cloud case.
var cloudflareNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
		"198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

func isCloudflare(ip net.IP) bool {
	for _, n := range cloudflareNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// DomainCheck is the result of checking a domain.
type DomainCheck struct {
	Host      string   `json:"host"`
	Addresses []string `json:"addresses"`
	// DNS: "ok" (reaches this server), "missing" (no A/AAAA record),
	// "elsewhere" (answers, but not from this server), "unreachable"
	// (nothing answered on port 80), "proxied" (Cloudflare's proxy).
	DNS        string `json:"dns"`
	DNSMessage string `json:"dnsMessage"`
	// HTTPS: "ok", "pending" (no trusted certificate yet), "error",
	// or "" when not checked.
	HTTPS        string `json:"https"`
	HTTPSMessage string `json:"httpsMessage"`
	ServerIP     string `json:"serverIp,omitempty"`
}

func checkDomain(ctx context.Context, host string, wantHTTPS bool) DomainCheck {
	c := DomainCheck{Host: host, Addresses: []string{}}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil || len(addrs) == 0 {
		c.DNS = "missing"
		c.DNSMessage = "There is no DNS record for " + host + " yet. Add an A record pointing at this server. New records can take a few minutes to show up."
		return c
	}
	cf := false
	for _, a := range addrs {
		c.Addresses = append(c.Addresses, a.IP.String())
		if isCloudflare(a.IP) {
			cf = true
		}
	}

	token := newCheckToken()
	client := &http.Client{
		Timeout:       6 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+deploy.CheckPath+token, nil)
	resp, err := client.Do(req)
	reached := false
	if err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		reached = strings.TrimSpace(string(b)) == token
	}
	switch {
	case reached && cf:
		c.DNS = "proxied"
		c.DNSMessage = host + " goes through Cloudflare's proxy (orange cloud). It works, but set Cloudflare's SSL mode to Full, or switch the record to DNS only."
	case reached:
		c.DNS = "ok"
		c.DNSMessage = host + " points at this server."
		if len(c.Addresses) > 0 {
			c.ServerIP = c.Addresses[0]
		}
	case cf:
		c.DNS = "proxied"
		c.DNSMessage = host + " goes through Cloudflare's proxy (orange cloud) and doesn't reach this server. Set the record to DNS only while the certificate is issued."
	case err != nil:
		c.DNS = "unreachable"
		c.DNSMessage = fmt.Sprintf("%s points at %s, but nothing answered there on port 80. If that is this server's IP, open ports 80 and 443 in its firewall.", host, strings.Join(c.Addresses, ", "))
	default:
		c.DNS = "elsewhere"
		c.DNSMessage = fmt.Sprintf("%s points at %s, which is a different server. Change its A record to this server's IP address.", host, strings.Join(c.Addresses, ", "))
	}
	if !wantHTTPS || (c.DNS != "ok" && c.DNS != "proxied") {
		return c
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, "443"), &tls.Config{ServerName: host})
	if err == nil {
		exp := conn.ConnectionState().PeerCertificates[0].NotAfter
		conn.Close()
		c.HTTPS = "ok"
		c.HTTPSMessage = "HTTPS works. The certificate renews automatically (current one valid until " + exp.Format("Jan 2, 2006") + ")."
		return c
	}
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	if errors.As(err, &ua) || errors.As(err, &hn) {
		c.HTTPS = "pending"
		c.HTTPSMessage = "The certificate isn't issued yet. Let's Encrypt usually takes under a minute once DNS points here. If this stays, check that port 80 is open."
		return c
	}
	c.HTTPS = "error"
	c.HTTPSMessage = "HTTPS isn't answering: " + err.Error()
	return c
}

// dnsCheck checks that a host name reaches this server, and its HTTPS.
func (s *Server) dnsCheck(w http.ResponseWriter, r *http.Request, _ *store.User) {
	host := deploy.CleanHost(r.URL.Query().Get("host"))
	if err := deploy.ValidHost(host); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c := checkDomain(r.Context(), host, r.URL.Query().Get("https") != "0")
	if c.ServerIP != "" {
		_ = s.store.SetSetting(r.Context(), "server_ip", c.ServerIP)
	}
	writeJSON(w, 200, c)
}
