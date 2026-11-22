// Package publicproxy is the small TCP proxy that makes a one-click database
// reachable from the internet.
//
// SimplePanel never publishes a database's own port. When public access is
// turned on, it starts a separate Swarm service from the panel's own image
// ("simplepanel public-proxy") that listens on the chosen port and forwards
// connections to the database over the project's private network. That
// keeps the database itself untouched (turning access on or off never
// restarts it) and gives one place to enforce, for every engine alike:
//
//   - an IP allowlist (the port is published in host mode, so the proxy
//     sees each client's real address);
//   - encryption: the proxy terminates TLS for PostgreSQL, MongoDB and
//     Redis with the database's certificate, and can refuse unencrypted
//     connections. MySQL and MariaDB encrypt end to end with the server's
//     own certificate; the proxy only checks that the client asked for TLS;
//   - connection limits per address, to slow down password guessing.
//
// Every accepted or refused connection is logged with the client address.
package publicproxy

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Config describes one public listener.
type Config struct {
	Listen     string // ":5432"
	Target     string // "project_db:5432"
	Protocol   string // postgres | mysql | mongo | redis
	Allow      []*net.IPNet
	RequireTLS bool
	Cert       *tls.Certificate // nil: the proxy doesn't terminate TLS

	MaxConns   int     // open connections in total
	MaxPerIP   int     // open connections per client address
	RatePerMin float64 // new connections per minute per address (after Burst)
	Burst      float64

	Log io.Writer
	Now func() time.Time
}

// Protocols lists the supported protocols.
var Protocols = []string{"postgres", "mysql", "mongo", "redis"}

// Environment variables read by ConfigFromEnv.
const (
	EnvListen     = "SP_PUBLIC_LISTEN"
	EnvTarget     = "SP_PUBLIC_TARGET"
	EnvProtocol   = "SP_PUBLIC_PROTOCOL"
	EnvAllow      = "SP_PUBLIC_ALLOW"
	EnvRequireTLS = "SP_PUBLIC_REQUIRE_TLS"
	EnvCert       = "SP_PUBLIC_CERT"
	EnvKey        = "SP_PUBLIC_KEY"
)

// ConfigFromEnv reads the proxy's settings from the environment.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Listen:     os.Getenv(EnvListen),
		Target:     os.Getenv(EnvTarget),
		Protocol:   os.Getenv(EnvProtocol),
		RequireTLS: os.Getenv(EnvRequireTLS) == "1",
	}
	if c.Listen == "" || c.Target == "" {
		return c, fmt.Errorf("%s and %s are required", EnvListen, EnvTarget)
	}
	if !validProtocol(c.Protocol) {
		return c, fmt.Errorf("%s must be one of %s", EnvProtocol, strings.Join(Protocols, ", "))
	}
	nets, err := ParseAllow(os.Getenv(EnvAllow))
	if err != nil {
		return c, err
	}
	c.Allow = nets
	if cert, key := os.Getenv(EnvCert), os.Getenv(EnvKey); cert != "" && key != "" {
		kp, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return c, fmt.Errorf("certificate: %w", err)
		}
		c.Cert = &kp
	}
	if c.RequireTLS && c.Cert == nil && c.Protocol != "mysql" {
		return c, errors.New("TLS is required but no certificate was given")
	}
	return c, nil
}

func validProtocol(p string) bool {
	for _, x := range Protocols {
		if x == p {
			return true
		}
	}
	return false
}

// ParseAllow parses a list of addresses and CIDR ranges separated by commas,
// spaces or new lines. A plain address allows just that address.
func ParseAllow(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		n, err := ParseCIDR(f)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// ParseCIDR parses "203.0.113.7", "203.0.113.0/24" or an IPv6 equivalent.
func ParseCIDR(s string) (*net.IPNet, error) {
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("%q is not an IP address or range", s)
		}
		if v4 := ip.To4(); v4 != nil {
			return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not an IP address or range", s)
	}
	return n, nil
}

// Proxy accepts public connections and forwards them to the database.
type Proxy struct {
	cfg    Config
	mu     sync.Mutex
	open   int
	peers  map[string]*peer
	sweep  time.Time
	logged map[string]time.Time // last "refused" line per address
}

type peer struct {
	tokens float64
	last   time.Time
	open   int
}

// New returns a proxy for cfg, with defaults for unset limits.
func New(cfg Config) *Proxy {
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 256
	}
	if cfg.MaxPerIP == 0 {
		cfg.MaxPerIP = 32
	}
	if cfg.RatePerMin == 0 {
		cfg.RatePerMin = 60
	}
	if cfg.Burst == 0 {
		cfg.Burst = 30
	}
	if cfg.Log == nil {
		cfg.Log = os.Stdout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Proxy{cfg: cfg, peers: map[string]*peer{}, logged: map[string]time.Time{}}
}

// Serve accepts connections on ln until it is closed.
func (p *Proxy) Serve(ln net.Listener) error {
	tls := "off"
	switch {
	case p.cfg.Protocol == "mysql" && p.cfg.RequireTLS:
		tls = "required (end to end with the server's certificate)"
	case p.cfg.Protocol == "mysql":
		tls = "optional (end to end with the server's certificate)"
	case p.cfg.Cert != nil && p.cfg.RequireTLS:
		tls = "required"
	case p.cfg.Cert != nil:
		tls = "optional"
	}
	allow := "anyone"
	if len(p.cfg.Allow) > 0 {
		var s []string
		for _, n := range p.cfg.Allow {
			s = append(s, n.String())
		}
		allow = strings.Join(s, ", ")
	}
	p.logf("listening", "-", "%s for %s on %s; allowed: %s; TLS %s", ln.Addr(), p.cfg.Protocol, p.cfg.Target, allow, tls)
	for {
		c, err := ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		go p.handle(c)
	}
}

// logf writes one line: "<time> <event> <client> <detail>".
func (p *Proxy) logf(event, ip, format string, args ...any) {
	fmt.Fprintf(p.cfg.Log, "%s %s %s %s\n", p.cfg.Now().UTC().Format(time.RFC3339), event, ip, fmt.Sprintf(format, args...))
}

// refused logs a refusal at most once per address every ten minutes, so
// internet-wide scanners can't flood the log.
func (p *Proxy) refused(ip, why string) {
	now := p.cfg.Now()
	p.mu.Lock()
	last, seen := p.logged[ip]
	if seen && now.Sub(last) < 10*time.Minute {
		p.mu.Unlock()
		return
	}
	p.logged[ip] = now
	if len(p.logged) > 10000 {
		for k, t := range p.logged {
			if now.Sub(t) > 10*time.Minute {
				delete(p.logged, k)
			}
		}
	}
	p.mu.Unlock()
	p.logf("refused", ip, "%s", why)
}

// Allowed reports whether ip may connect.
func (p *Proxy) Allowed(ip net.IP) bool {
	if len(p.cfg.Allow) == 0 {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range p.cfg.Allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// admit applies the connection limits. It returns a reason when the
// connection must be refused; otherwise the caller must call release.
func (p *Proxy) admit(ip string) string {
	now := p.cfg.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.sweep) > 5*time.Minute {
		for k, pr := range p.peers {
			if pr.open == 0 && now.Sub(pr.last) > 10*time.Minute {
				delete(p.peers, k)
			}
		}
		p.sweep = now
	}
	pr := p.peers[ip]
	if pr == nil {
		pr = &peer{tokens: p.cfg.Burst, last: now}
		p.peers[ip] = pr
	}
	pr.tokens = min(p.cfg.Burst, pr.tokens+now.Sub(pr.last).Minutes()*p.cfg.RatePerMin)
	pr.last = now
	switch {
	case p.open >= p.cfg.MaxConns:
		return "too many open connections"
	case pr.open >= p.cfg.MaxPerIP:
		return fmt.Sprintf("more than %d open connections from this address", p.cfg.MaxPerIP)
	case pr.tokens < 1:
		return "too many new connections from this address; slow down"
	}
	pr.tokens--
	pr.open++
	p.open++
	return ""
}

func (p *Proxy) release(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open--
	if pr := p.peers[ip]; pr != nil {
		pr.open--
	}
}

// negotiateTimeout bounds the start of a connection, before any data is
// forwarded.
var negotiateTimeout = 15 * time.Second

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	ip := net.ParseIP(host)
	if ip == nil {
		return
	}
	if v4 := ip.To4(); v4 != nil {
		host = v4.String()
	}
	if !p.Allowed(ip) {
		p.refused(host, "not on the allowlist")
		return
	}
	if why := p.admit(host); why != "" {
		p.refused(host, why)
		return
	}
	defer p.release(host)
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(time.Minute)
	}
	_ = c.SetDeadline(time.Now().Add(negotiateTimeout))
	var (
		client  net.Conn
		backend net.Conn
		prefix  []byte
		secure  bool
		err     error
	)
	switch p.cfg.Protocol {
	case "mysql":
		backend, secure, err = p.mysql(c)
		client = c
	case "postgres":
		client, prefix, secure, err = p.postgres(c)
	default:
		client, secure, err = p.direct(c)
	}
	if err != nil {
		if backend != nil {
			backend.Close()
		}
		var r refusal
		if errors.As(err, &r) {
			p.refused(host, string(r))
		} else if strings.HasPrefix(err.Error(), "can't reach") {
			p.logf("error", host, "%v", err)
		}
		return
	}
	if backend == nil {
		backend, err = net.DialTimeout("tcp", p.cfg.Target, 5*time.Second)
		if err != nil {
			p.logf("error", host, "can't reach the database: %v", err)
			return
		}
		if len(prefix) > 0 {
			if _, err := backend.Write(prefix); err != nil {
				backend.Close()
				return
			}
		}
	}
	defer backend.Close()
	_ = c.SetDeadline(time.Time{})
	if client != c {
		_ = client.SetDeadline(time.Time{})
	}
	enc := "unencrypted"
	if secure {
		enc = "TLS"
	}
	p.logf("connected", host, "%s", enc)
	pipe(client, backend)
}

// pipe copies both ways until either side closes.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// refusal is a reason to refuse a client that is worth logging.
type refusal string

func (r refusal) Error() string { return string(r) }

// bufConn reads through a bufio.Reader so peeked bytes aren't lost.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func looksLikeTLS(b []byte) bool {
	// A TLS record: handshake (22), version 3.x.
	return len(b) >= 3 && b[0] == 0x16 && b[1] == 0x03 && b[2] <= 0x04
}

func (p *Proxy) tlsConfig(alpn ...string) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{*p.cfg.Cert}, MinVersion: tls.VersionTLS12, NextProtos: alpn}
}

// direct handles protocols where a client starts TLS straight away
// (MongoDB, Redis): a TLS handshake is terminated here, anything else is
// plain text.
func (p *Proxy) direct(c net.Conn) (net.Conn, bool, error) {
	br := bufio.NewReader(c)
	head, err := br.Peek(3)
	if err != nil {
		return nil, false, err
	}
	bc := &bufConn{Conn: c, r: br}
	if looksLikeTLS(head) && p.cfg.Cert != nil {
		tc := tls.Server(bc, p.tlsConfig())
		if err := tc.Handshake(); err != nil {
			return nil, false, refusal("TLS handshake failed: " + err.Error())
		}
		return tc, true, nil
	}
	if p.cfg.RequireTLS {
		if p.cfg.Protocol == "redis" {
			_, _ = c.Write([]byte("-ERR this Redis only accepts encrypted connections from the internet; connect with TLS (rediss://)\r\n"))
		}
		return nil, false, refusal("unencrypted connection")
	}
	return bc, false, nil
}

// PostgreSQL start-up request codes.
const (
	pgSSLRequest    = 80877103
	pgGSSEncRequest = 80877104
	pgCancelRequest = 80877102
)

// postgres answers PostgreSQL's SSLRequest itself and terminates TLS (the
// database container has no certificate), or accepts a direct TLS
// handshake (sslnegotiation=direct, PostgreSQL 17 clients). It returns the
// client connection and the start-up packet to send to the server.
func (p *Proxy) postgres(c net.Conn) (net.Conn, []byte, bool, error) {
	var cur net.Conn = c
	br := bufio.NewReader(c)
	secure := false
	for range 4 {
		if !secure && p.cfg.Cert != nil {
			if head, err := br.Peek(3); err == nil && looksLikeTLS(head) {
				tc := tls.Server(&bufConn{Conn: cur, r: br}, p.tlsConfig("postgresql"))
				if err := tc.Handshake(); err != nil {
					return nil, nil, false, refusal("TLS handshake failed: " + err.Error())
				}
				cur, br, secure = tc, bufio.NewReader(tc), true
				continue
			}
		}
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil, nil, false, err
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		code := binary.BigEndian.Uint32(hdr[4:])
		if n < 8 || n > 10000 {
			return nil, nil, false, refusal("not a PostgreSQL client")
		}
		switch {
		case n == 8 && code == pgSSLRequest && !secure:
			if p.cfg.Cert == nil {
				if _, err := cur.Write([]byte{'N'}); err != nil {
					return nil, nil, false, err
				}
				continue
			}
			if _, err := cur.Write([]byte{'S'}); err != nil {
				return nil, nil, false, err
			}
			tc := tls.Server(&bufConn{Conn: cur, r: br}, p.tlsConfig("postgresql"))
			if err := tc.Handshake(); err != nil {
				return nil, nil, false, refusal("TLS handshake failed: " + err.Error())
			}
			cur, br, secure = tc, bufio.NewReader(tc), true
			continue
		case n == 8 && (code == pgGSSEncRequest || code == pgSSLRequest):
			if _, err := cur.Write([]byte{'N'}); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, hdr[:])
		if _, err := io.ReadFull(br, pkt[8:]); err != nil {
			return nil, nil, false, err
		}
		// A cancel request only carries a process ID and secret key, and
		// older clients always send it unencrypted.
		if code != pgCancelRequest && p.cfg.RequireTLS && !secure {
			_, _ = cur.Write(pgError("28000", "this database only accepts encrypted connections from the internet; connect with sslmode=require"))
			return nil, nil, false, refusal("unencrypted connection")
		}
		return &bufConn{Conn: cur, r: br}, pkt, secure, nil
	}
	return nil, nil, false, refusal("not a PostgreSQL client")
}

// pgError builds a FATAL ErrorResponse message.
func pgError(code, msg string) []byte {
	var body []byte
	for _, f := range [][2]string{{"S", "FATAL"}, {"V", "FATAL"}, {"C", code}, {"M", msg}} {
		body = append(body, f[0][0])
		body = append(body, f[1]...)
		body = append(body, 0)
	}
	body = append(body, 0)
	out := []byte{'E', 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(len(body)+4))
	return append(out, body...)
}

// MySQL capability flag a client sets to start TLS.
const mysqlClientSSL = 0x0800

// mysql connects to the server first (MySQL's server speaks first), passes
// its greeting on, and when TLS is required checks that the client's
// first packet is an SSL request. TLS itself runs end to end between the
// client and the server, which has its own certificate.
func (p *Proxy) mysql(c net.Conn) (net.Conn, bool, error) {
	backend, err := net.DialTimeout("tcp", p.cfg.Target, 5*time.Second)
	if err != nil {
		return nil, false, fmt.Errorf("can't reach the database: %w", err)
	}
	_ = backend.SetDeadline(time.Now().Add(negotiateTimeout))
	greeting, err := readMySQLPacket(backend)
	if err != nil {
		return backend, false, err
	}
	if _, err := c.Write(greeting); err != nil {
		return backend, false, err
	}
	_ = backend.SetDeadline(time.Time{})
	first, err := readMySQLPacket(c)
	if err != nil {
		return backend, false, err
	}
	secure := len(first) >= 8 && binary.LittleEndian.Uint16(first[4:6])&mysqlClientSSL != 0
	if p.cfg.RequireTLS && !secure {
		_, _ = c.Write(mysqlError(first[3]+1, 3159, "HY000", "This database only accepts encrypted connections from the internet. Connect with TLS (ssl-mode=REQUIRED)."))
		return backend, false, refusal("unencrypted connection")
	}
	if _, err := backend.Write(first); err != nil {
		return backend, false, err
	}
	return backend, secure, nil
}

// readMySQLPacket reads one packet, header included.
func readMySQLPacket(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	if n > 64<<10 {
		return nil, refusal("not a MySQL client")
	}
	pkt := make([]byte, 4+n)
	copy(pkt, hdr[:])
	_, err := io.ReadFull(r, pkt[4:])
	return pkt, err
}

func mysqlError(seq byte, code uint16, state, msg string) []byte {
	body := []byte{0xff, byte(code), byte(code >> 8), '#'}
	body = append(body, state...)
	body = append(body, msg...)
	n := len(body)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, body...)
}
