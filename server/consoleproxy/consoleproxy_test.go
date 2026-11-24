package consoleproxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/consoleproxy"
)

func setup(t *testing.T) (*http.Client, string) {
	t.Helper()

	account := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "account:"+r.Host+r.URL.Path)
	})

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "s3:"+r.URL.Path)
	}))
	t.Cleanup(s3.Close)

	s3URL, _ := url.Parse(s3.URL)

	p, err := consoleproxy.New(consoleproxy.Config{Account: account, S3: s3URL.Host, S3Port: "9000"})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	proxyURL, _ := url.Parse(srv.URL)

	// * a client like an old console: TLS 1.0 at most, RSA key exchange with AES-CBC, no certificate check
	client := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			MaxVersion:         tls.VersionTLS10,
			CipherSuites:       []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA},
		},
	}}

	return client, srv.URL
}

func get(t *testing.T, c *http.Client, u string) (int, string, http.Header) {
	t.Helper()

	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}

	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(b), resp.Header
}

func TestAccountHostsOverHTTPSWithAnOldTLSClient(t *testing.T) {
	c, _ := setup(t)

	for _, host := range []string{"account.pretendo.cc", "c.account.pretendo.cc", "account.nintendo.net"} {
		code, body, _ := get(t, c, "https://"+host+"/v1/api/oauth20/access_token/generate")
		if code != 200 || body != "account:"+host+"/v1/api/oauth20/access_token/generate" {
			t.Fatalf("%s: %d %q", host, code, body)
		}
	}
}

func TestConnectionTestAndOwnDownloadsAndRefusals(t *testing.T) {
	c, _ := setup(t)

	code, body, h := get(t, c, "http://conntest.nintendowifi.net/")
	if code != 200 || h.Get("X-Organization") != "Nintendo" || !strings.Contains(body, "This is test.html page") {
		t.Fatalf("conntest: %d %q %v", code, body, h)
	}

	if code, body, _ := get(t, c, "http://192.168.1.50:9000/smm/course/1.bin"); code != 200 || body != "s3:/smm/course/1.bin" {
		t.Fatalf("download: %d %q", code, body)
	}

	// * anything else is not forwarded: not an open proxy
	// * and the connection is dropped without an answer, as if the host could not be reached
	if resp, err := c.Get("http://example.com/"); err == nil {
		resp.Body.Close()
		t.Fatalf("plain request to another host got an answer: %d", resp.StatusCode)
	}

	if _, err := c.Get("https://example.com/"); err == nil {
		t.Fatal("a CONNECT to another host must be refused")
	}
}

func TestPortExplainsItselfToABrowser(t *testing.T) {
	c, base := setup(t)

	resp, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "proxy port") {
		t.Fatalf("body %q", b)
	}

	_ = c
}

// The Wii U's SpotPass services are not served, but what the console asks for is recorded (credentials hidden).
func TestSpotPassRequestsAreLoggedAndAnswered404(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)

	p, err := consoleproxy.New(consoleproxy.Config{
		Account:       http.NotFoundHandler(),
		ServeSpotPass: true,
		Logf: func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()

			lines = append(lines, fmt.Sprintf(f, a...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	proxyURL, _ := url.Parse(srv.URL)
	c := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			MaxVersion:         tls.VersionTLS10,
			CipherSuites:       []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA},
		},
	}}

	for _, host := range []string{"nppl.app.pretendo.cc", "npts.app.pretendo.cc", "npdl.cdn.pretendo.cc"} {
		req, _ := http.NewRequest("GET", "https://"+host+"/p01/tasksheet/1/abc/Task?c=US&l=en", nil)
		req.Header.Set("X-Nintendo-Title-Id", "000500001018db00")
		req.Header.Set("Authorization", "Bearer secret-token")

		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d", host, resp.StatusCode)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	all := strings.Join(lines, "\n")

	for _, want := range []string{"SpotPass request GET nppl.app.pretendo.cc:443/p01/tasksheet/1/abc/Task?c=US&l=en", "npdl.cdn.pretendo.cc", `X-Nintendo-Title-Id="000500001018db00"`, "Authorization=<hidden>"} {
		if !strings.Contains(all, want) {
			t.Fatalf("log lacks %q:\n%s", want, all)
		}
	}

	if strings.Contains(all, "secret-token") {
		t.Fatalf("a credential was logged:\n%s", all)
	}
}

// A console that finishes the TLS handshake and then sends something that is not HTTP must leave a trace.
func TestAConnectionWithoutAnUnderstoodRequestIsLogged(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)

	p, err := consoleproxy.New(consoleproxy.Config{
		Account:       http.NotFoundHandler(),
		ServeSpotPass: true,
		Logf: func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()

			lines = append(lines, fmt.Sprintf(f, a...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	fmt.Fprint(conn, "CONNECT nppl.app.pretendo.cc:443 HTTP/1.1\r\nHost: nppl.app.pretendo.cc:443\r\n\r\n")

	br := bufio.NewReader(conn)
	if status, _ := br.ReadString('\n'); !strings.Contains(status, "200") {
		t.Fatalf("CONNECT answered %q", status)
	}

	br.ReadString('\n') // the blank line that ends the response

	tc := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS10, CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA}})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}

	tc.Write([]byte("\x01\x02not http at all\r\n\r\n"))
	tc.Close()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		mu.Lock()
		all := strings.Join(lines, "\n")
		mu.Unlock()

		if strings.Contains(all, "ended without a request") && strings.Contains(all, "not http at all") && strings.Contains(all, "TLS with the console for nppl.app.pretendo.cc:443") {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	t.Fatalf("no trace of the connection:\n%s", strings.Join(lines, "\n"))
}

// * regression: the console's login request is over 1 KB (a header block of about 1.3 KB) and one with a form body can be
// * much longer. The log recorder that wraps the connection once cut every read at its own limit, so the request never
// * arrived whole and the console waited 30 seconds for an answer (error 102-2152).
func TestLongRequestsArriveWholeThroughTheLoggingWrapper(t *testing.T) {
	account := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "header=%d body=%d", len(r.Header.Get("X-Padding")), len(body))
	})

	p, err := consoleproxy.New(consoleproxy.Config{Account: account})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS10, CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA}},
	}}

	for _, n := range []int{1200, 5000, 40000} {
		req, _ := http.NewRequest("POST", "https://account.pretendo.cc/v1/api/oauth20/access_token/generate", strings.NewReader(strings.Repeat("b", n)))
		req.Header.Set("X-Padding", strings.Repeat("h", 900))

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%d byte body: %v", n, err)
		}

		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if want := fmt.Sprintf("header=900 body=%d", n); string(got) != want {
			t.Fatalf("%d byte body: got %q, want %q", n, got, want)
		}
	}
}

// * by default a CONNECT to a SpotPass host is refused at once, like a proxy that cannot reach it: a console that is let in
// * and then given nothing waits for it (the game sat on its loading screen), a refusal fails immediately
func TestSpotPassConnectIsDroppedByDefault(t *testing.T) {
	var (
		mu    sync.Mutex
		lines []string
	)

	p, err := consoleproxy.New(consoleproxy.Config{
		Account: http.NotFoundHandler(),
		Logf: func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()

			lines = append(lines, fmt.Sprintf(f, a...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	fmt.Fprint(conn, "CONNECT nppl.app.pretendo.cc:443 HTTP/1.1\r\nHost: nppl.app.pretendo.cc:443\r\n\r\n")

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if status, _ := bufio.NewReader(conn).ReadString('\n'); status != "" {
		t.Fatalf("CONNECT to a SpotPass host was answered with %q; the connection should be dropped", status)
	}

	mu.Lock()
	defer mu.Unlock()

	if all := strings.Join(lines, "\n"); !strings.Contains(all, "refused CONNECT nppl.app.pretendo.cc:443 (SpotPass") {
		t.Fatalf("no log line for the refusal:\n%s", all)
	}
}

// * Pretendo pass-through (opt-in): system titles reach the real Pretendo, games are answered here

type fakePretendo struct {
	proxy   *httptest.Server
	hosts   []string // addresses the proxy was asked to reach
	learned []string // "name=pid" for every profile reply the proxy read
	mu      sync.Mutex
}

func newFakePretendo(t *testing.T, pretendo bool) (*fakePretendo, *http.Client) {
	t.Helper()

	f := &fakePretendo{}

	// * the "real" account server (TLS) and a TCP echo for tunnelled connections
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// * a test can ask for a profile reply like the real Pretendo's, or for an error
		if r.Header.Get("X-Test-Profile") != "" {
			if code := r.Header.Get("X-Test-Status"); code == "404" {
				w.WriteHeader(http.StatusNotFound)
			}

			fmt.Fprint(w, `<?xml version="1.0"?><person><active_flag>Y</active_flag><pid>1799000123</pid><region>2</region><user_id>SomeUser</user_id></person>`)

			return
		}

		fmt.Fprintf(w, "upstream:%s:%s", r.Host, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { echo.Close() })

	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}

			go func() { io.Copy(c, c); c.Close() }() //nolint:errcheck
		}
	}()

	account := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "account:"+r.Host+r.URL.Path)
	})

	p, err := consoleproxy.New(consoleproxy.Config{
		Account:     account,
		Pretendo:    pretendo,
		UpstreamTLS: &tls.Config{InsecureSkipVerify: true},
		LearnProfile: func(userID string, pid uint32) {
			f.mu.Lock()
			f.learned = append(f.learned, fmt.Sprintf("%s=%d", userID, pid))
			f.mu.Unlock()
		},
		UpstreamDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			f.mu.Lock()
			f.hosts = append(f.hosts, addr)
			f.mu.Unlock()

			target := echo.Addr().String()
			if strings.HasPrefix(addr, "account.") {
				target = strings.TrimPrefix(upstream.URL, "https://")
			}

			var d net.Dialer

			return d.DialContext(ctx, network, target)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	f.proxy = httptest.NewServer(p)
	t.Cleanup(f.proxy.Close)

	proxyURL, _ := url.Parse(f.proxy.URL)

	return f, &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS10, CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA}},
	}}
}

func getWithTitle(t *testing.T, c *http.Client, u, title string) string {
	t.Helper()

	req, _ := http.NewRequest("GET", u, nil)
	if title != "" {
		req.Header.Set("X-Nintendo-Title-ID", title)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s (title %q): %v", u, title, err)
	}

	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)

	return string(b)
}

func TestPretendoModeSplitsTheAccountServiceByTitle(t *testing.T) {
	_, c := newFakePretendo(t, true)

	u := "https://account.pretendo.cc/v1/api/people/@me/profile"

	for title, want := range map[string]string{
		"0005001010040100": "upstream:account.pretendo.cc:/v1/api/people/@me/profile", // Wii U Menu
		"0005001010001C00": "upstream:account.pretendo.cc:/v1/api/people/@me/profile", // friend list
		"000500301001610A": "upstream:account.pretendo.cc:/v1/api/people/@me/profile", // Miiverse
		"000500001018DC00": "account:account.pretendo.cc/v1/api/people/@me/profile",   // the game
		"":                 "account:account.pretendo.cc/v1/api/people/@me/profile",   // no title
	} {
		if got := getWithTitle(t, c, u, title); got != want {
			t.Fatalf("title %q: got %q, want %q", title, got, want)
		}
	}
}

func TestWithoutPretendoModeEveryTitleIsAnsweredHere(t *testing.T) {
	f, c := newFakePretendo(t, false)

	if got := getWithTitle(t, c, "https://account.pretendo.cc/x", "0005001010040100"); got != "account:account.pretendo.cc/x" {
		t.Fatalf("got %q", got)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.hosts) != 0 {
		t.Fatalf("the proxy contacted %v with the pass-through off", f.hosts)
	}
}

func TestPretendoModeTunnelsOtherPretendoHostsAndOnlyThose(t *testing.T) {
	f, _ := newFakePretendo(t, true)

	open := func(host string) (net.Conn, *bufio.Reader, string) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(f.proxy.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}

		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		br := bufio.NewReader(conn)
		status, _ := br.ReadString('\n')

		return conn, br, status
	}

	// * a Pretendo host that the home server does not serve: bytes go to the real host and back untouched
	conn, br, status := open("nppl.app.pretendo.cc:443")
	defer conn.Close()

	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT answered %q", status)
	}

	br.ReadString('\n') // blank line

	fmt.Fprint(conn, "hello\n")

	if line, _ := br.ReadString('\n'); line != "hello\n" {
		t.Fatalf("tunnel returned %q", line)
	}

	// * a host outside pretendo.cc is not tunnelled
	other, _, status2 := open("example.com:443")
	defer other.Close()

	if status2 != "" {
		t.Fatalf("CONNECT to example.com answered %q; it must be dropped", status2)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, h := range f.hosts {
		if strings.Contains(h, "example.com") {
			t.Fatalf("the proxy contacted %s", h)
		}
	}
}

func TestPretendoModeLearnsTheRealPlayerIDFromTheProfileReply(t *testing.T) {
	f, c := newFakePretendo(t, true)

	profile := func(title, status string) string {
		req, _ := http.NewRequest("GET", "https://account.pretendo.cc/v1/api/people/@me/profile", nil)
		req.Header.Set("X-Nintendo-Title-ID", title)
		req.Header.Set("X-Test-Profile", "1")
		req.Header.Set("Accept-Encoding", "gzip") // the proxy must still be able to read the reply

		if status != "" {
			req.Header.Set("X-Test-Status", status)
		}

		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)

		return string(b)
	}

	// * a system title's profile reply is read, and still reaches the console whole
	body := profile("0005001010040100", "")
	if !strings.Contains(body, "<pid>1799000123</pid>") || !strings.Contains(body, "<user_id>SomeUser</user_id>") {
		t.Fatalf("the console did not get the reply: %q", body)
	}

	f.mu.Lock()
	got := append([]string(nil), f.learned...)
	f.mu.Unlock()

	if len(got) != 1 || got[0] != "SomeUser=1799000123" {
		t.Fatalf("learned %v, want [SomeUser=1799000123]", got)
	}

	// * an error reply teaches nothing, and the game's own request (answered here) neither
	profile("0005001010040100", "404")
	profile("000500001018DC00", "")

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.learned) != 1 {
		t.Fatalf("learned %v after an error reply and a game request, want only the first", f.learned)
	}
}
