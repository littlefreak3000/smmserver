package consoleproxy_test

import (
	"bufio"
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
	if code, _, _ := get(t, c, "http://example.com/"); code != http.StatusNotFound {
		t.Fatalf("plain request to another host: %d", code)
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
