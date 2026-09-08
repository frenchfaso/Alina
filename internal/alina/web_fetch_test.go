package alina

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestWebFetchPublicSmoke(t *testing.T) {
	if os.Getenv("ALINA_TEST_WEB") != "1" {
		t.Skip("set ALINA_TEST_WEB=1 for an actual public HTTPS fetch; no provider credentials used")
	}
	text, err := webFetch(context.Background(), newFetchClient(), `{"url":"https://example.com/"}`)
	if err != nil || !strings.Contains(text, "Example Domain") {
		t.Fatal(text, err)
	}
	t.Log("public DNS, TLS and HTML extraction OK")
	// The same Termux adapter also serves providers, auth, search and Telegram.
	resp, err := newHTTPClient().Get("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}

type fetchTransport func(*http.Request) (*http.Response, error)

func (f fetchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fetchFixture(media, body string) *http.Client {
	c := newFetchClient()
	c.Transport = fetchTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return c
}

func TestWebFetchExtractionAndPagination(t *testing.T) {
	c := fetchFixture("text/html; charset=utf-8", `<title>Example &amp; source</title><style>STYLE_SECRET</style><script>SCRIPT_SECRET</script><h1>Title</h1><p>Hello <b>world</b> — caffè ☕ <a href="/source">source</a></p><ul><li>One</li><li>Two</li></ul><p hidden>HIDDEN_SECRET</p>`)
	var complete strings.Builder
	offset := 0
	for {
		text, err := webFetch(context.Background(), c, jsonText(map[string]any{"url": "https://example.com/start", "offset": offset, "limit": 13}))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			URL, Title, Text string
			Next             int `json:"next_offset"`
		}
		if err = json.Unmarshal([]byte(text), &page); err != nil {
			t.Fatal(err)
		}
		if page.URL != "https://example.com/start" || page.Title != "Example & source" {
			t.Fatal(page)
		}
		complete.WriteString(page.Text)
		if page.Next == 0 {
			break
		}
		if page.Next <= offset {
			t.Fatal("pagination stuck")
		}
		offset = page.Next
	}
	text := complete.String()
	for _, want := range []string{"# Title", "Hello world", "caffè ☕", "https://example.com/source", "- One", "- Two"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "SECRET") {
		t.Fatal("non-readable HTML leaked", text)
	}
	base, _ := url.Parse("https://example.com")
	if text, _, err := pageText(`<p>Hello<b> world</b> and<em> another</em> word.</p>`, base); err != nil || text != "Hello world and another word." {
		t.Fatal(text, err)
	}
	for _, media := range []string{"text/plain", "application/json", "text/markdown"} {
		out, err := webFetch(context.Background(), fetchFixture(media, "caffè ☕"), `{"url":"https://example.com"}`)
		if err != nil || !strings.Contains(out, "caffè ☕") {
			t.Fatal(media, out, err)
		}
	}
}

func TestWebFetchRejectsFilesOversizeAndBadPages(t *testing.T) {
	for _, tc := range []struct{ media, body string }{
		{"application/pdf", "%PDF"}, {"application/octet-stream", "binary"}, {"image/png", "image"}, {"", "text"}, {"text/plain", "bad\x00file"}, {"text/plain", strings.Repeat("x", 2<<20+1)},
	} {
		if _, err := webFetch(context.Background(), fetchFixture(tc.media, tc.body), `{"url":"https://example.com"}`); err == nil {
			t.Errorf("accepted %s len=%d", tc.media, len(tc.body))
		}
	}
	c := fetchFixture("text/plain", "file")
	old := c.Transport
	c.Transport = fetchTransport(func(r *http.Request) (*http.Response, error) {
		resp, err := old.RoundTrip(r)
		resp.Header.Set("Content-Disposition", `attachment; filename="text.txt"`)
		return resp, err
	})
	if _, err := webFetch(context.Background(), c, `{"url":"https://example.com"}`); err == nil {
		t.Fatal("attachment accepted")
	}
	for _, args := range []string{`{"url":"https://example.com","offset":-1}`, `{"url":"https://example.com","offset":5}`, `{"url":"https://example.com","limit":25000}`} {
		if _, err := webFetch(context.Background(), fetchFixture("text/plain", "abc"), args); err == nil {
			t.Fatal(args)
		}
	}
	base, _ := url.Parse("https://example.com/" + strings.Repeat("x", 7000) + "/")
	if _, _, err := pageText(strings.Repeat(`<a href="a">a</a>`, 1000), base); err == nil {
		t.Fatal("link expansion exceeded extraction budget")
	}
}

func TestWebFetchPublicAddressAndRedirectPolicy(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/file", "https://user:secret@example.com/", "http://127.0.0.1", "http://169.254.169.254", "http://[::ffff:127.0.0.1]", "http://[fe80::1%25en0]", "http://100.93.143.27"} {
		if _, err := fetchURL(raw); err == nil {
			t.Error("accepted", raw)
		}
	}
	for _, ip := range []string{"0.1.2.3", "10.0.0.1", "172.16.0.1", "192.168.0.1", "::1", "fc00::1", "100.64.0.1", "224.0.0.1", "64:ff9b::7f00:1"} {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Error(ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(ip)) {
			t.Error(ip)
		}
	}
	for _, dest := range []string{"http://127.0.0.1/private", "http://example.com/downgrade", "https://example.com/loop"} {
		calls := 0
		c := newFetchClient()
		c.Transport = fetchTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("ambient credentials forwarded")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {dest}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		if _, err := webFetch(context.Background(), c, `{"url":"https://example.com/start"}`); err == nil || calls > 5 {
			t.Fatal(dest, calls, err)
		}
	}
	transport := newFetchClient().Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dial allowed a private destination")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := webFetch(ctx, newFetchClient(), `{"url":"https://example.com"}`); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
