package consoleproxy_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
