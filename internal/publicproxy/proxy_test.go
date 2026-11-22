package publicproxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoBackend records what it receives and answers each read with "ok:" +
// the data. For MySQL it first sends a greeting packet.
type echoBackend struct {
	ln   net.Listener
	mu   sync.Mutex
	got  bytes.Buffer
	gree []byte
}

func newBackend(t *testing.T, greeting []byte) *echoBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &echoBackend{ln: ln, gree: greeting}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if b.gree != nil {
					c.Write(b.gree)
				}
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						b.mu.Lock()
						b.got.Write(buf[:n])
						b.mu.Unlock()
						c.Write(append([]byte("ok:"), buf[:n]...))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return b
}

func (b *echoBackend) received() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got.String()
}

func testCert(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	cp, kp, err := NewCert("test", []string{"127.0.0.1", "db.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cp), []byte(kp))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(cp))
	return &pair, pool
}

func startProxy(t *testing.T, cfg Config) (string, *syncBuf) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	log := &syncBuf{}
	cfg.Log = log
	go New(cfg).Serve(ln)
	return ln.Addr().String(), log
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func readSome(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, n)
	got, err := io.ReadFull(c, buf)
	if err != nil {
		t.Fatalf("read: %v (got %q)", err, buf[:got])
	}
	return string(buf)
}

func pgStartup() []byte {
	body := []byte{0, 3, 0, 0}
	body = append(body, "user\x00app\x00\x00"...)
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(body)+4))
	return append(out, body...)
}

func pgSSLRequestPkt() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, 8)
	binary.BigEndian.PutUint32(b[4:], pgSSLRequest)
	return b
}

func TestPostgresSSLRequest(t *testing.T) {
	be := newBackend(t, nil)
	cert, pool := testCert(t)
	addr, log := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "postgres", Cert: cert, RequireTLS: true})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write(pgSSLRequestPkt())
	if got := readSome(t, c, 1); got != "S" {
		t.Fatalf("SSLRequest answer = %q", got)
	}
	tc := tls.Client(c, &tls.Config{RootCAs: pool, ServerName: "db.example.com"})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake (verified against the certificate): %v", err)
	}
	st := pgStartup()
	tc.Write(st)
	tc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, len(st)+3)
	if _, err := io.ReadFull(tc, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[3:], st) || !strings.HasPrefix(be.received(), string(st)) {
		t.Fatalf("backend got %q", be.received())
	}
	if !strings.Contains(log.String(), "connected 127.0.0.1 TLS") {
		t.Errorf("log: %s", log)
	}
}

func TestPostgresDirectTLS(t *testing.T) {
	be := newBackend(t, nil)
	cert, pool := testCert(t)
	addr, _ := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "postgres", Cert: cert, RequireTLS: true})
	tc, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", NextProtos: []string{"postgresql"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	if tc.ConnectionState().NegotiatedProtocol != "postgresql" {
		t.Errorf("ALPN = %q", tc.ConnectionState().NegotiatedProtocol)
	}
	tc.Write(pgStartup())
	tc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(tc, make([]byte, 3)); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresPlainRefusedWhenTLSRequired(t *testing.T) {
	be := newBackend(t, nil)
	cert, _ := testCert(t)
	addr, log := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "postgres", Cert: cert, RequireTLS: true})
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	c.Write(pgStartup())
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(c)
	if len(b) == 0 || b[0] != 'E' || !bytes.Contains(b, []byte("sslmode=require")) {
		t.Fatalf("answer = %q", b)
	}
	if be.received() != "" {
		t.Errorf("backend got data: %q", be.received())
	}
	if !strings.Contains(log.String(), "refused 127.0.0.1 unencrypted connection") {
		t.Errorf("log: %s", log)
	}
}

func TestPostgresPlainAllowedWhenOptional(t *testing.T) {
	be := newBackend(t, nil)
	cert, _ := testCert(t)
	addr, _ := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "postgres", Cert: cert})
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	st := pgStartup()
	c.Write(st)
	readSome(t, c, 3)
	if !strings.HasPrefix(be.received(), string(st)) {
		t.Fatalf("backend got %q", be.received())
	}
}

func TestAllowlist(t *testing.T) {
	be := newBackend(t, nil)
	nets, err := ParseAllow("203.0.113.0/24, 2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	addr, log := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "redis", Allow: nets})
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	c.Write([]byte("PING\r\n"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if b, _ := io.ReadAll(c); len(b) != 0 {
		t.Fatalf("got %q from a refused connection", b)
	}
	if !strings.Contains(log.String(), "refused 127.0.0.1 not on the allowlist") {
		t.Errorf("log: %s", log)
	}
	p := New(Config{Allow: nets})
	for ip, want := range map[string]bool{"203.0.113.9": true, "198.51.100.1": false, "2001:db8::1": true, "::ffff:203.0.113.5": true} {
		if got := p.Allowed(net.ParseIP(ip)); got != want {
			t.Errorf("Allowed(%s) = %v", ip, got)
		}
	}
	if _, err := ParseAllow("10.0.0.0/33"); err == nil {
		t.Error("bad range accepted")
	}
	if _, err := ParseAllow("example.com"); err == nil {
		t.Error("host name accepted")
	}
}

func TestRedisTLSAndPlain(t *testing.T) {
	be := newBackend(t, nil)
	cert, pool := testCert(t)
	addr, _ := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "redis", Cert: cert, RequireTLS: true})

	tc, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	tc.Write([]byte("PING\r\n"))
	tc.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 9)
	if _, err := io.ReadFull(tc, got); err != nil || string(got) != "ok:PING\r\n" {
		t.Fatalf("TLS: %q %v", got, err)
	}
	tc.Close()

	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	c.Write([]byte("PING\r\n"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(b), "-ERR") {
		t.Fatalf("plain answer = %q", b)
	}
}

func TestMongoPlainWhenOptional(t *testing.T) {
	be := newBackend(t, nil)
	cert, _ := testCert(t)
	addr, _ := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "mongo", Cert: cert})
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	msg := []byte{0x16, 0x01, 0, 0, 1, 2, 3, 4} // a length that starts like TLS, but isn't
	c.Write(msg)
	if got := readSome(t, c, 3+len(msg)); got[3:] != string(msg) {
		t.Fatalf("got %q", got)
	}
}

func mysqlPacket(seq byte, payload []byte) []byte {
	n := len(payload)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload...)
}

func TestMySQLRequireTLS(t *testing.T) {
	greeting := mysqlPacket(0, []byte("\x0a8.4.0\x00greeting"))
	be := newBackend(t, greeting)
	addr, log := startProxy(t, Config{Target: be.ln.Addr().String(), Protocol: "mysql", RequireTLS: true})

	// A client that asks for TLS: its SSL request reaches the server.
	c, _ := net.Dial("tcp", addr)
	if got := readSome(t, c, len(greeting)); got != string(greeting) {
		t.Fatalf("greeting = %q", got)
	}
	ssl := make([]byte, 32)
	binary.LittleEndian.PutUint32(ssl, 0x0800|0x0200|0x8000)
	c.Write(mysqlPacket(1, ssl))
	readSome(t, c, 3+36)
	c.Close()

	// A client that doesn't is refused with a MySQL error packet.
	c, _ = net.Dial("tcp", addr)
	defer c.Close()
	readSome(t, c, len(greeting))
	resp := make([]byte, 64)
	binary.LittleEndian.PutUint32(resp, 0x0200|0x8000)
	c.Write(mysqlPacket(1, resp))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(bufio.NewReader(c))
	if len(b) < 8 || b[3] != 2 || b[4] != 0xff || !bytes.Contains(b, []byte("ssl-mode=REQUIRED")) {
		t.Fatalf("answer = %q", b)
	}
	if !strings.Contains(log.String(), "refused 127.0.0.1 unencrypted connection") {
		t.Errorf("log: %s", log)
	}
}

func TestRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	p := New(Config{Burst: 3, RatePerMin: 60, MaxPerIP: 100, Now: func() time.Time { return now }})
	for i := range 3 {
		if why := p.admit("1.2.3.4"); why != "" {
			t.Fatalf("connection %d refused: %s", i, why)
		}
		p.release("1.2.3.4")
	}
	if why := p.admit("1.2.3.4"); why == "" {
		t.Fatal("4th connection in a burst allowed")
	}
	if why := p.admit("5.6.7.8"); why != "" {
		t.Fatalf("other address refused: %s", why)
	}
	now = now.Add(2 * time.Second) // 60/min refills one every second
	if why := p.admit("1.2.3.4"); why != "" {
		t.Fatalf("after refill: %s", why)
	}
	q := New(Config{MaxPerIP: 2, Burst: 10})
	q.admit("9.9.9.9")
	q.admit("9.9.9.9")
	if why := q.admit("9.9.9.9"); !strings.Contains(why, "open connections") {
		t.Fatalf("per-address cap: %q", why)
	}
}

func TestRefusalLogThrottled(t *testing.T) {
	now := time.Unix(1000, 0)
	var log syncBuf
	p := New(Config{Log: &log, Now: func() time.Time { return now }})
	p.refused("1.2.3.4", "not on the allowlist")
	p.refused("1.2.3.4", "not on the allowlist")
	if n := strings.Count(log.String(), "refused"); n != 1 {
		t.Fatalf("%d lines", n)
	}
	now = now.Add(11 * time.Minute)
	p.refused("1.2.3.4", "not on the allowlist")
	if n := strings.Count(log.String(), "refused"); n != 2 {
		t.Fatalf("%d lines after 11 minutes", n)
	}
}

func TestFingerprint(t *testing.T) {
	cp, _, err := NewCert("x", nil)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := Fingerprint(cp)
	if err != nil || len(fp) != 95 || strings.Count(fp, ":") != 31 {
		t.Fatalf("fingerprint %q %v", fp, err)
	}
}
