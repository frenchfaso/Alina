package alina

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type calendarTransport func(*http.Request) (*http.Response, error)

func (f calendarTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func calendarFixture(t *testing.T) (*Engine, *runningJob, *int) {
	t.Helper()
	dir := t.TempDir()
	e := &Engine{AdminDir: dir, Config: Config{Users: []User{{ID: "alice", Family: "home"}, {ID: "bob", Family: "home"}, {ID: "eve", Family: "other"}}}}
	e.global = e
	if err := os.MkdirAll(filepath.Join(dir, "integrations"), 0700); err != nil {
		t.Fatal(err)
	}
	c := calendarCredential{Type: "service_account", Email: "alina@sample.iam.gserviceaccount.com", Key: "TEST_PRIVATE_KEY", KeyID: "key", TokenURI: calendarTokenURL}
	if err := writeJSON(filepath.Join(dir, "integrations", "google-calendar.json"), c); err != nil {
		t.Fatal(err)
	}
	e.calendar.token = &oauth2.Token{AccessToken: "TEST_ACCESS_TOKEN", Expiry: time.Now().Add(time.Hour)}
	e.calendar.keyID = "key"
	calls := new(int)
	e.calendar.client = &http.Client{Transport: calendarTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.URL.Scheme != "https" || r.URL.Host != "www.googleapis.com" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer TEST_ACCESS_TOKEN" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		body := `{"accessRole":"reader"}`
		if r.URL.Query().Get("fields") != "accessRole" {
			q := r.URL.Query()
			if q.Get("singleEvents") != "true" || q.Get("orderBy") != "startTime" || q.Get("maxResults") != "50" || q.Get("pageToken") != "page-two" {
				t.Fatalf("bad event query %v", q)
			}
			if strings.Contains(q.Get("fields"), "description") || strings.Contains(q.Get("fields"), "attendees") {
				t.Fatal("overbroad projection")
			}
			body = `{"accessRole":"reader","timeZone":"Europe/Rome","nextPageToken":"page-three","items":[{"id":"event","summary":"Appointment","description":"PRIVATE_DESCRIPTION","attendees":[{"email":"PRIVATE_GUEST"}],"start":{"date":"2026-09-25"},"end":{"date":"2026-09-26"}}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return e, &runningJob{Job: Job{Owner: "local:alice", Kind: "chat"}, ctx: context.Background()}, calls
}
func calendarCall(t *testing.T, e *Engine, j *runningJob, a calendarArgs) string {
	t.Helper()
	out, err := e.calendarTool(j, jsonText(a))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func calendarConnect(t *testing.T, e *Engine, j *runningJob, shared bool) calendarBinding {
	t.Helper()
	var b calendarBinding
	out := calendarCall(t, e, j, calendarArgs{Action: "connect", CalendarID: "alice@example.com", Consent: true, ShareFamily: shared})
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestCalendarOnboardingAndIsolation(t *testing.T) {
	e, j, calls := calendarFixture(t)
	setup := calendarCall(t, e, j, calendarArgs{Action: "setup"})
	if !strings.Contains(setup, "data_use") || !strings.Contains(setup, "alina@sample") || strings.Contains(setup, "TEST_PRIVATE_KEY") || *calls != 0 {
		t.Fatal("unsafe setup", setup)
	}
	if _, err := e.calendarTool(j, `{"action":"connect","calendar_id":"alice@example.com"}`); err == nil || *calls != 0 {
		t.Fatal("missing consent accepted")
	}
	b := calendarConnect(t, e, j, false)
	j.Owner = "local:bob"
	if got := calendarCall(t, e, j, calendarArgs{Action: "list"}); got != "[]" {
		t.Fatal("personal binding leaked", got)
	}
	if _, err := e.calendarTool(j, jsonText(calendarArgs{Action: "events", ID: b.ID})); err == nil {
		t.Fatal("personal events accessible")
	}
	j.Owner = "local:alice"
	shared := calendarConnect(t, e, j, true)
	if shared.ID != b.ID {
		t.Fatal("relink changed identity")
	}
	j.Owner = "local:bob"
	if !strings.Contains(calendarCall(t, e, j, calendarArgs{Action: "list"}), b.ID) {
		t.Fatal("family binding missing")
	}
	if _, err := e.calendarTool(j, jsonText(calendarArgs{Action: "disconnect", ID: b.ID})); err == nil {
		t.Fatal("other member disconnected owner")
	}
	out := calendarCall(t, e, j, calendarArgs{Action: "events", ID: b.ID, Start: "2026-09-25T00:00:00+02:00", End: "2026-09-26T00:00:00+02:00", PageToken: "page-two"})
	if !strings.Contains(out, "page-three") || !strings.Contains(out, "Appointment") || strings.Contains(out, "PRIVATE_") {
		t.Fatal("projection/pagination failed", out)
	}
	e.Config.Users[0].Family = "moved"
	if got := calendarCall(t, e, j, calendarArgs{Action: "list"}); got != "[]" {
		t.Fatal("stale family grant", got)
	}
	j.Owner = "local:eve"
	if got := calendarCall(t, e, j, calendarArgs{Action: "list"}); got != "[]" {
		t.Fatal("cross-family leak", got)
	}
	j.Owner = "local:alice"
	if err := os.Remove(filepath.Join(e.AdminDir, "integrations", "google-calendar.json")); err != nil {
		t.Fatal(err)
	}
	calendarCall(t, e, j, calendarArgs{Action: "disconnect", ID: b.ID})
	if got := calendarCall(t, e, j, calendarArgs{Action: "list"}); got != "[]" {
		t.Fatal("disconnect failed", got)
	}
}
func TestCalendarRejectsUnsafeRequests(t *testing.T) {
	e, j, calls := calendarFixture(t)
	for _, id := range []string{"https://example.com/a@b", "a@b?x=y", "a@b/../x", "a@b\n"} {
		if _, err := e.calendarTool(j, jsonText(calendarArgs{Action: "connect", Consent: true, CalendarID: id})); err == nil {
			t.Fatal("accepted URL/invalid ID")
		}
	}
	for _, kind := range []string{"task", "dream", "initiative"} {
		j.Kind = kind
		if _, err := e.calendarTool(j, `{"action":"connect","consent":true,"calendar_id":"a@b"}`); err == nil {
			t.Fatal("background linking", kind)
		}
		if kind != "task" {
			if _, err := e.calendarTool(j, `{"action":"list"}`); err == nil {
				t.Fatal("background calendar access", kind)
			}
		}
	}
	j.Kind = "chat"
	j.Owner = "local:unknown"
	if _, err := e.calendarTool(j, `{"action":"setup"}`); err == nil {
		t.Fatal("unauthenticated access")
	}
	if *calls != 0 {
		t.Fatal("rejected request reached network")
	}
}
func TestCalendarRequiresReaderAndBounds(t *testing.T) {
	e, j, calls := calendarFixture(t)
	b := calendarConnect(t, e, j, false)
	for _, end := range []string{"2026-09-24T00:00:00Z", "2027-09-25T00:00:00Z", "invalid"} {
		if _, err := e.calendarTool(j, jsonText(calendarArgs{Action: "events", ID: b.ID, Start: "2026-09-25T00:00:00Z", End: end})); err == nil {
			t.Fatal("invalid interval accepted")
		}
	}
	if *calls != 1 {
		t.Fatal("invalid interval reached network")
	}
	for _, role := range []string{"freeBusyReader", "none", ""} {
		e.calendar.client.Transport = calendarTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(jsonText(map[string]any{"accessRole": role, "items": []any{map[string]any{"summary": "DO_NOT_DISCLOSE"}}})))}, nil
		})
		for _, a := range []calendarArgs{{Action: "connect", Consent: true, CalendarID: "a@b"}, {Action: "events", ID: b.ID, Start: "2026-09-25T00:00:00Z", End: "2026-09-26T00:00:00Z"}} {
			out, err := e.calendarTool(j, jsonText(a))
			if err == nil || strings.Contains(out, "DO_NOT_DISCLOSE") {
				t.Fatal("accepted non-reader", role, out, err)
			}
		}
	}
}
func TestCalendarCredentialProtection(t *testing.T) {
	e, j, calls := calendarFixture(t)
	path := filepath.Join(e.AdminDir, "integrations", "google-calendar.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.calendarTool(j, `{"action":"setup"}`); err == nil {
		t.Fatal("public key file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := e.calendarCredential()
	if err != nil {
		t.Fatal(err)
	}
	c.TokenURI = "https://attacker.example/token"
	if err = writeJSON(path, c); err != nil {
		t.Fatal(err)
	}
	if _, err = e.calendarTool(j, `{"action":"setup"}`); err == nil || *calls != 0 {
		t.Fatal("custom token endpoint accepted")
	}
}

func TestCalendarTokenScopeCachingAndErrors(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c := calendarCredential{Email: "alina@sample.iam.gserviceaccount.com", KeyID: "key", Key: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	tokens := 0
	status := 200
	s := calendarState{client: &http.Client{Transport: calendarTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"accessRole":"reader"}`
		code := status
		if r.URL.String() == calendarTokenURL {
			tokens++
			code = 200
			if r.Method != "POST" {
				t.Fatal("token method")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("missing signed JWT")
			}
			data, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var claims map[string]any
			if err = json.Unmarshal(data, &claims); err != nil {
				t.Fatal(err)
			}
			if claims["scope"] != calendarScope || claims["iss"] != c.Email || claims["aud"] != calendarTokenURL || claims["sub"] != nil {
				t.Fatalf("unexpected claims %v", claims)
			}
			body = `{"access_token":"TEST_ACCESS_TOKEN","token_type":"Bearer","expires_in":3600}`
		} else if r.URL.Host != "www.googleapis.com" || r.Header.Get("Authorization") != "Bearer TEST_ACCESS_TOKEN" {
			t.Fatal("unexpected API request")
		}
		if code != 200 {
			body = "SECRET_ERROR_BODY"
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	var result map[string]any
	for range 2 {
		if err = s.get(context.Background(), c, "test/events", nil, &result); err != nil {
			t.Fatal(err)
		}
	}
	if tokens != 1 {
		t.Fatal("token not cached")
	}
	status = 401
	if err = s.get(context.Background(), c, "test/events", nil, &result); err == nil || strings.Contains(err.Error(), "SECRET") || s.token != nil {
		t.Fatal("401 invalidation/redaction failed", err)
	}
	status = 200
	if err = s.get(context.Background(), c, "test/events", nil, &result); err != nil || tokens != 2 {
		t.Fatal("token not renewed", err)
	}
}
