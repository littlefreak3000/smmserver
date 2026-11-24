// Package consoleproxy lets a Wii U that was patched with Inkay talk to the home server.
//
// Inkay rewrites the console's Nintendo Network addresses to Pretendo's (account.pretendo.cc and friends) and switches off
// the console's certificate checks. A console with its Internet connection set to use an HTTP proxy then sends those requests
// to the proxy: HTTPS ones as a CONNECT, plain ones as absolute URLs. This is that proxy, for exactly the hosts the home
// server answers (account service, connection test, its own course downloads) and nothing else, so it is not an open proxy.
// Pretendo's self-hosting guide does the same with mitmproxy and nginx.
package consoleproxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config says what the proxy serves.
type Config struct {
	// Account answers every host that contains "account." (account.pretendo.cc, c.account.pretendo.cc, account.nintendo.net).
	Account http.Handler
	// S3 is where requests for the server's own address and course-download port go (host:port, usually 127.0.0.1:9000);
	// empty serves no downloads through the proxy (consoles then reach the S3 address directly).
	S3 string
	// S3Port is the port that marks a request as meant for the S3 ("9000").
	S3Port string
	Logf   func(format string, args ...any)
}

// Proxy is the HTTP proxy handler.
type Proxy struct {
	cfg  Config
	cert tls.Certificate
	s3   *httputil.ReverseProxy
}

const connTestBody = `<!DOCTYPE html><html><head><title>HTML Page</title></head><body bgcolor="#FFFFFF">This is test.html page</body></html>`

// New builds the proxy and a certificate for the hidden TLS endpoints (the console does not check it once Inkay is on).
func New(cfg Config) (*Proxy, error) {
	cert, err := selfSigned()
	if err != nil {
		return nil, err
	}

	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}

	p := &Proxy{cfg: cfg, cert: cert}

	if cfg.S3 != "" {
		p.s3 = httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: cfg.S3})
	}

	return p, nil
}

func selfSigned() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "account.pretendo.cc", Organization: []string{"Super Mario Maker home server"}},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"account.pretendo.cc", "c.account.pretendo.cc", "account.nintendo.net", "c.account.nintendo.net"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// tlsConfig accepts what an old console offers: TLS 1.0 and up, RSA key exchange with CBC.
func (p *Proxy) tlsConfig() *tls.Config {
	var suites []uint16

	for _, s := range tls.CipherSuites() {
		suites = append(suites, s.ID)
	}

	for _, s := range tls.InsecureCipherSuites() {
		suites = append(suites, s.ID)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{p.cert},
		MinVersion:   tls.VersionTLS10,
		CipherSuites: suites,
	}
}

// ServeHTTP handles proxy requests (CONNECT and absolute URLs) and says what this port is for to anyone else.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.IsAbs():
		p.cfg.Logf("proxy: %s %s", r.Method, r.URL.String())
		p.route(w, r, r.URL.Host)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "This is the home server's proxy port for a Wii U patched with Inkay. See docs/setup-wiiu.md.")
	}
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if !p.known(host) {
		p.cfg.Logf("proxy: refused CONNECT %s (not a host the home server answers)", host)
		http.Error(w, "not served here", http.StatusForbidden)

		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)

		return
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}

	p.cfg.Logf("proxy: CONNECT %s", host)

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		conn.Close()

		return
	}

	tc := tls.Server(conn, p.tlsConfig())
	if err := tc.Handshake(); err != nil {
		p.cfg.Logf("proxy: TLS handshake with the console failed for %s: %v", host, err)
		conn.Close()

		return
	}

	st := tc.ConnectionState()
	p.cfg.Logf("proxy: TLS with the console for %s: version %#x, cipher %#x, server name %q", host, st.Version, st.CipherSuite, st.ServerName)

	// * Record the first bytes the console sends, so a request the HTTP server cannot make sense of is not silent.
	tap := &tapConn{Conn: tc}

	var served atomic.Bool

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served.Store(true)
			p.cfg.Logf("proxy: %s https://%s%s", r.Method, host, r.URL.RequestURI())
			if h, _, err := net.SplitHostPort(host); err == nil {
				r.Host = h
			} else {
				r.Host = host
			}

			p.route(w, r, host)
		}),
		ReadHeaderTimeout: 30 * time.Second,
	}

	_ = srv.Serve(&oneConn{conn: tap})

	if !served.Load() {
		p.cfg.Logf("proxy: connection for %s ended without a request the proxy understood; %d bytes received, %s, last read error: %v", host, tap.total, describeStart(tap.first, tap.total), tap.err())
	}
}

// describeStart says what a connection began with, for the log: the request line and headers (up to the blank line that
// ends them, with credentials hidden), and only the size of anything after that, since a body can hold a password hash.
// When no blank line is there, it says so: the request was cut off or never finished.
func describeStart(first []byte, total int) string {
	head, _, found := strings.Cut(string(first), "\r\n\r\n")

	lines := strings.Split(head, "\r\n")
	for i, l := range lines {
		if name, _, ok := strings.Cut(l, ":"); ok && (strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie")) {
			lines[i] = name + ": <hidden>"
		}
	}

	head = strings.Join(lines, "\r\n")

	if found {
		return fmt.Sprintf("the request line and headers were complete (%d bytes, then %d bytes of body, not logged): %q", len(head)+4, total-len(head)-4, head)
	}

	return fmt.Sprintf("NO end of headers (blank line) was seen in the first %d bytes: %q", len(first), head)
}

// tapConn remembers the first bytes read from a connection.
type tapConn struct {
	net.Conn
	mu      sync.Mutex
	first   []byte
	total   int
	lastErr error
}

func (t *tapConn) err() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.lastErr
}

func (t *tapConn) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)

	t.mu.Lock()
	t.total += n

	if err != nil {
		t.lastErr = err
	}

	// * only the copy is limited; the caller must always be told how many bytes it really got
	if room := 4096 - len(t.first); room > 0 && n > 0 {
		t.first = append(t.first, b[:min(n, room)]...)
	}
	t.mu.Unlock()

	return n, err
}

// known says whether a host:port is something the home server answers.
func (p *Proxy) known(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}

	host = strings.ToLower(host)

	return strings.Contains(host, "account.") || strings.Contains(host, "conntest") || spotpass(host) || (p.s3 != nil && port == p.cfg.S3Port)
}

// spotpass says whether a host is one of the Wii U's SpotPass (BOSS) services: the policy list (nppl), task lists (npts),
// file downloads (npdl), file lists (npfl) and push notifications (npns). The game and the system use them to download
// extra data in the background. They are not served here; the proxy only records what the console asks for (see route).
func spotpass(host string) bool {
	label, _, _ := strings.Cut(strings.ToLower(host), ".")

	switch label {
	case "nppl", "npts", "npdl", "npfl", "npns":
		return true
	}

	return false
}

// logRequest records a request the home server does not answer: the line, and every header except credentials, so a
// missing service can be worked out from what the console sends.
func (p *Proxy) logRequest(r *http.Request, hostport string) {
	names := make([]string, 0, len(r.Header))
	for k := range r.Header {
		names = append(names, k)
	}

	sort.Strings(names)

	var b strings.Builder

	for _, k := range names {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Cookie") {
			fmt.Fprintf(&b, " %s=<hidden>", k)
			continue
		}

		fmt.Fprintf(&b, " %s=%q", k, strings.Join(r.Header[k], ","))
	}

	p.cfg.Logf("proxy: SpotPass request %s %s%s (body %d bytes) headers:%s", r.Method, hostport, r.URL.RequestURI(), r.ContentLength, b.String())
}

func (p *Proxy) route(w http.ResponseWriter, r *http.Request, hostport string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}

	host = strings.ToLower(host)

	switch {
	case strings.Contains(host, "account."):
		p.cfg.Account.ServeHTTP(w, r)
	case strings.Contains(host, "conntest"):
		w.Header().Set("X-Organization", "Nintendo")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, connTestBody)
	case p.s3 != nil && port == p.cfg.S3Port:
		p.s3.ServeHTTP(w, r)
	case spotpass(host):
		p.logRequest(r, hostport)
		http.Error(w, "not found", http.StatusNotFound)
	default:
		p.cfg.Logf("proxy: no answer for %s (the home server does not stand in for it)", hostport)
		http.Error(w, "not served by the home server", http.StatusNotFound)
	}
}

// oneConn is a listener that hands out one connection and then reports closed.
type oneConn struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (o *oneConn) Accept() (net.Conn, error) {
	var c net.Conn

	o.once.Do(func() {
		c = &notifyConn{Conn: o.conn, closed: make(chan struct{})}
		o.done = c.(*notifyConn).closed
	})

	if c != nil {
		return c, nil
	}

	<-o.done

	return nil, errors.New("closed")
}

func (o *oneConn) Close() error   { return nil }
func (o *oneConn) Addr() net.Addr { return o.conn.LocalAddr() }

type notifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (n *notifyConn) Close() error {
	n.once.Do(func() { close(n.closed) })

	return n.Conn.Close()
}
