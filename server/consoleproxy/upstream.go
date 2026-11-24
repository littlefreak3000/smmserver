package consoleproxy

// Optional pass-through to the real Pretendo Network.
//
// With Config.Pretendo set, the console's own programs (Wii U Menu, friend list, Miiverse, ...) keep talking to the real
// Pretendo servers through this proxy, and only games are answered by the home server. Everything the home server does not
// serve on Pretendo's domain is tunnelled to the real host; the account service (which the home server also answers, for the
// game) is split by the title that is asking: system titles go on to Pretendo, games are answered here.
//
// Only hosts under pretendo.cc are ever contacted, so this is still not an open proxy.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// pretendoHost says whether a host (with or without a port) is Pretendo Network's own domain.
func pretendoHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))

	return host == "pretendo.cc" || strings.HasSuffix(host, ".pretendo.cc")
}

// systemTitle says whether a title ID belongs to one of the Wii U's own programs: system apps (00050010..., the Wii U Menu and
// the friend list) and system applets (00050030..., Miiverse). Games are 00050000...
func systemTitle(title string) bool {
	title = strings.ToUpper(strings.TrimSpace(title))

	return strings.HasPrefix(title, "0005001") || strings.HasPrefix(title, "0005003")
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.cfg.UpstreamDial != nil {
		return p.cfg.UpstreamDial(ctx, network, addr)
	}

	d := net.Dialer{Timeout: 10 * time.Second}

	return d.DialContext(ctx, network, addr)
}

// upstream is the client side of the pass-through: a reverse proxy to the real host.
func (p *Proxy) upstream(scheme, host string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: scheme, Host: host}

	return &httputil.ReverseProxy{
		// * Rewrite (unlike Director) adds no X-Forwarded-For: the console's address is not passed on
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			// * the proxy reads some replies, so it asks for them uncompressed (the Go transport then decodes by itself)
			pr.Out.Header.Del("Accept-Encoding")
		},
		Transport: &http.Transport{
			DialContext:         p.dial,
			TLSClientConfig:     p.cfg.UpstreamTLS,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.cfg.Logf("proxy: Pretendo did not answer %s %s%s: %v", r.Method, host, r.URL.RequestURI(), err)
			p.refuse(w, "upstream did not answer", http.StatusBadGateway)
		},
	}
}

// forwardAccount sends an account-service request of a system title on to the real Pretendo account server.
func (p *Proxy) forwardAccount(w http.ResponseWriter, r *http.Request, hostport string) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}

	p.cfg.Logf("proxy: sent %s %s%s on to the real Pretendo (system title %s)", r.Method, host, r.URL.Path, r.Header.Get("X-Nintendo-Title-ID"))

	rp := p.upstream("https", host)
	if p.cfg.LearnProfile != nil && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/api/people/@me/profile") {
		rp.ModifyResponse = p.learnFromProfile
	}

	rp.ServeHTTP(w, r)
}

// learnFromProfile reads the account name and player ID out of the real Pretendo's profile reply and tells LearnProfile. The
// reply goes on to the console unchanged.
func (p *Proxy) learnFromProfile(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))

	if err != nil {
		return nil // the console gets what was read; nothing is learned
	}

	var person struct {
		PID    uint64 `xml:"pid"`
		UserID string `xml:"user_id"`
	}

	if xml.Unmarshal(body, &person) != nil || person.PID == 0 || person.PID > 0xffffffff || person.UserID == "" {
		p.cfg.Logf("proxy: the profile reply from Pretendo has no usable player ID (%d bytes)", len(body))

		return nil
	}

	p.cfg.Logf("proxy: Pretendo says %q has player ID %d", person.UserID, person.PID)
	p.cfg.LearnProfile(person.UserID, uint32(person.PID))

	return nil
}

// forwardPlain sends a plain absolute-URL request for a Pretendo host on to the real server.
func (p *Proxy) forwardPlain(w http.ResponseWriter, r *http.Request, hostport string) {
	p.cfg.Logf("proxy: sent %s http://%s%s on to the real Pretendo", r.Method, hostport, r.URL.RequestURI())
	p.upstream("http", hostport).ServeHTTP(w, r)
}

// tunnel connects a console's CONNECT straight to the real host: the TLS is between the console and Pretendo.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	hostport := r.Host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(hostport, "443")
	}

	up, err := p.dial(r.Context(), "tcp", hostport)
	if err != nil {
		p.cfg.Logf("proxy: cannot reach %s for the console: %v", hostport, err)
		p.refuse(w, "cannot connect", http.StatusBadGateway)

		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "cannot hijack", http.StatusInternalServerError)

		return
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		up.Close()

		return
	}

	p.cfg.Logf("proxy: tunnel to the real %s", hostport)

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		conn.Close()
		up.Close()

		return
	}

	var wg sync.WaitGroup

	wg.Add(2)

	pipe := func(dst, src net.Conn) {
		defer wg.Done()

		io.Copy(dst, src) //nolint:errcheck

		// * when one side is done, end the other so both copies finish
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite() //nolint:errcheck
		} else {
			dst.Close()
		}
	}

	go pipe(up, conn)
	go pipe(conn, up)

	wg.Wait()
	conn.Close()
	up.Close()
}
