package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__cli" {
		if e := Main(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "__sandbox" {
		if e := sandboxExec(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPermissionLifetimes(t *testing.T) {
	d := t.TempDir()
	p, e := NewPermissions(d)
	if e != nil {
		t.Fatal(e)
	}
	a := Action{Tool: "shell", Command: "pkg install jq", Directory: d, Network: true}
	for _, scope := range []string{"once", "restart", "always"} {
		if e = p.Add(a, scope); e != nil {
			t.Fatal(e)
		}
		if p.Has(a) != (scope != "once") {
			t.Fatal("wrong lifetime", scope)
		}
		other := a
		other.Directory = "/different"
		if p.Has(other) {
			t.Fatal("grant leaked to other cwd")
		}
		other = a
		other.Command += " curl"
		if p.Has(other) {
			t.Fatal("grant leaked to changed command")
		}
		p, e = NewPermissions(d)
		if e != nil {
			t.Fatal(e)
		}
		if p.Has(a) != (scope == "always") {
			t.Fatal("restart semantics", scope)
		}
	}
	if e = p.Revoke(a.Key()); e != nil {
		t.Fatal(e)
	}
	p, _ = NewPermissions(d)
	if p.Has(a) {
		t.Fatal("revocation not persisted")
	}
}

type scriptedModel struct {
	mu      sync.Mutex
	calls   int
	Command string
	Network bool
	wait    bool
}

func (m *scriptedModel) Complete(ctx context.Context, s string, msg []Message, tools []ToolSpec, delta func(string)) (Message, error) {
	if m.wait {
		<-ctx.Done()
		return Message{}, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if msg[len(msg)-1].Role == "tool" {
		return Message{Role: "assistant", Content: msg[len(msg)-1].Content}, nil
	}
	b, _ := json.Marshal(map[string]any{"command": m.Command, "network": m.Network})
	return Message{Role: "assistant", Calls: []ToolCall{{ID: randomID(), Name: "shell", Arguments: string(b)}}}, nil
}
func newTestEngine(t *testing.T, m Model) *Engine {
	t.Helper()
	d := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = d
	c.CommandTimeout = 3
	e, er := NewEngine(d, c, m, nil)
	if er != nil {
		t.Fatal(er)
	}
	t.Cleanup(e.Close)
	return e
}
func awaitStatus(t *testing.T, e *Engine, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := e.Get(id)
		if j.Status == status {
			return j
		}
		if terminalStatus(j.Status) {
			t.Fatalf("wanted %s, got %#v", status, j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("status timeout", status)
	return Job{}
}

func TestApprovalAndExactlyOnceSubmission(t *testing.T) {
	m := &scriptedModel{Command: "printf ALINA_APPROVED", Network: true}
	e := newTestEngine(t, m)
	j, er := e.SubmitKey("test", "local", "run it", "unique")
	if er != nil {
		t.Fatal(er)
	}
	duplicate, er := e.SubmitKey("test", "local", "run it", "unique")
	if er != nil || duplicate.ID != j.ID {
		t.Fatal("dedup failed", er)
	}
	p := awaitStatus(t, e, j.ID, "approval")
	if er = e.Approve(j.ID, p.Approval.ID, "once", "intruder"); er == nil {
		t.Fatal("unauthorized approval")
	}
	if er = e.Approve(j.ID, "stale", "once", "local"); er == nil {
		t.Fatal("stale approval accepted")
	}
	if er = e.Approve(j.ID, p.Approval.ID, "once", "local"); er != nil {
		t.Fatal(er)
	}
	done := awaitStatus(t, e, j.ID, "completed")
	if !strings.Contains(done.Output, "ALINA_APPROVED") {
		t.Fatal(done.Output)
	}
	if e.Permissions.Has(p.Approval.Action) {
		t.Fatal("once persisted")
	}
	if er = e.Approve(j.ID, p.Approval.ID, "once", "local"); er == nil {
		t.Fatal("approval replay")
	}
}
func TestDeniedCommandHasNoSideEffect(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{Command: "touch forbidden", Network: true})
	j, _ := e.Submit("test", "local", "do it")
	p := awaitStatus(t, e, j.ID, "approval")
	if er := e.Approve(j.ID, p.Approval.ID, "deny", "local"); er != nil {
		t.Fatal(er)
	}
	done := awaitStatus(t, e, j.ID, "completed")
	if !strings.Contains(done.Output, "denied") {
		t.Fatal(done.Output)
	}
	if _, er := os.Stat(filepath.Join(e.Config.WorkDir, "forbidden")); !os.IsNotExist(er) {
		t.Fatal("denied operation ran")
	}
}
func TestCancellationAndRestart(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	j, _ := e.Submit("test", "local", "wait")
	awaitStatus(t, e, j.ID, "running")
	if er := e.Cancel(j.ID, "local"); er != nil {
		t.Fatal(er)
	}
	awaitStatus(t, e, j.ID, "cancelled")
	stored := Job{ID: "interrupted", Session: "test", Status: "approval", Approval: &Approval{ID: "old"}}
	if er := writeJSON(filepath.Join(e.Dir, "jobs", "interrupted.json"), stored); er != nil {
		t.Fatal(er)
	}
	n, er := NewEngine(e.Dir, e.Config, e.Model, nil)
	if er != nil {
		t.Fatal(er)
	}
	defer n.Close()
	old, _ := n.Get("interrupted")
	if old.Status != "interrupted" || old.Approval != nil {
		t.Fatal(old)
	}
}
func TestShellNetworkFilterAndTimeout(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("network filter unavailable on this OS")
	}
	d := t.TempDir()
	out, e := runShell(context.Background(), Action{Command: "printf LOCAL_OK", Directory: d}, 3)
	if e != nil || !strings.Contains(out, "exit_code: 0\nLOCAL_OK") {
		t.Fatal(out, e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "NETWORK_REACHED") }))
	defer srv.Close()
	command := "curl --noproxy '*' --max-time 2 -sS " + srv.URL
	out, e = runShell(context.Background(), Action{Command: command, Directory: d}, 3)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out, "NETWORK_REACHED") || strings.HasPrefix(out, "exit_code: 0\n") {
		t.Fatal("network restriction bypassed:", out)
	}
	if python, err := exec.LookPath("python3"); err == nil {
		port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
		out, err = runShell(context.Background(), Action{Command: python + " -c 'import socket; socket.create_connection((\"127.0.0.1\", " + port + "), timeout=1)'", Directory: d}, 3)
		if err != nil || strings.HasPrefix(out, "exit_code: 0\n") {
			t.Fatal("interpreter bypassed filter", out, err)
		}
	}
	out, e = runShell(context.Background(), Action{Command: command, Directory: d, Network: true}, 3)
	if e != nil || !strings.Contains(out, "NETWORK_REACHED") {
		t.Fatal("approved network failed", out, e)
	}
	start := time.Now()
	out, e = runShell(context.Background(), Action{Command: "sleep 30 & wait", Directory: d}, 1)
	if e != nil || time.Since(start) > 5*time.Second || !strings.Contains(out, "deadline exceeded") {
		t.Fatal("timeout failed", out, e)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOAuthRefreshIsDedicatedAndPersisted(t *testing.T) {
	d := t.TempDir()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"test-account"}}`))
	token := "header." + payload + ".signature"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Error("wrong refresh request")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer srv.Close()
	client := srv.Client()
	transport := client.Transport
	client.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		target := strings.TrimPrefix(srv.URL, "http://")
		r.URL.Scheme = "http"
		r.URL.Host = target
		return transport.RoundTrip(r)
	})
	writeJSON(filepath.Join(d, "chatgpt.json"), Credential{Refresh: "old-refresh", Expires: 1})
	a := Auth{Dir: d, Client: client}
	c, err := a.Get(context.Background())
	if err != nil || c.AccountID != "test-account" {
		t.Fatal(c.AccountID, err)
	}
	a.Get(context.Background())
	if calls != 1 {
		t.Fatal("valid access token refreshed twice")
	}
	b, _ := os.ReadFile(filepath.Join(d, "chatgpt.json"))
	if !bytes.Contains(b, []byte("new-refresh")) {
		t.Fatal("rotated token not persisted")
	}
}
func TestDaemonAndCLIOnUnixSocket(t *testing.T) {
	d, err := os.MkdirTemp("", "alina-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	c := DefaultConfig()
	c.WorkDir = d
	if err = SaveConfig(d, c); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	start := func() *exec.Cmd {
		cmd := exec.Command(self, "__cli", "serve")
		cmd.Env = append(os.Environ(), "ALINA_HOME="+d)
		var logs bytes.Buffer
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	cmd := start()
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var r map[string]any
		if localRequest(context.Background(), d, "GET", "/v1/status", nil, &r) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var status map[string]any
	if err = localRequest(context.Background(), d, "GET", "/v1/status", nil, &status); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(socketPath(d))
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket permission")
	}
	cli := exec.Command(self, "__cli", "status")
	cli.Env = append(os.Environ(), "ALINA_HOME="+d)
	out, err := cli.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("Alina")) {
		t.Fatal(string(out), err)
	}
	second := exec.Command(self, "__cli", "serve")
	second.Env = append(os.Environ(), "ALINA_HOME="+d)
	if out, err = second.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("already running")) {
		t.Fatal("instance lock failed", string(out), err)
	}
	var j Job
	if err = localRequest(context.Background(), d, "POST", "/v1/jobs", map[string]string{"session": "smoke", "message": "hello"}, &j); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		localRequest(context.Background(), d, "GET", "/v1/jobs/"+j.ID, nil, &j)
		if terminalStatus(j.Status) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if j.Status != "failed" || !strings.Contains(j.Error, "login required") {
		t.Fatal("missing auth not surfaced", j)
	}
	cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon shutdown timeout")
	}
	stopped = true
}
func TestPackageClassification(t *testing.T) {
	for _, s := range []string{"pkg install jq", "python -m pip install foo", "sh -c 'apt-get update'", "/usr/bin/dpkg -i x.deb"} {
		a := shellAction(s, "/tmp", false)
		if a.Reason == "" {
			t.Fatal("missing approval", s)
		}
		if !a.Network {
			t.Fatal("approved package action would lack network", s)
		}
	}
	if sandboxAvailable() {
		for _, s := range []string{"go version", "npm test", "git status", "python --version"} {
			if a := shellAction(s, "/tmp", false); a.Reason != "" {
				t.Fatal("unnecessary local approval", s)
			}
		}
	}
}

func TestResponsesSSEAndCitations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["store"] != false || b["stream"] != true || r.Header.Get("originator") != "alina" {
			t.Error("bad codex request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example"}]}]},{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}]}}`+"\n\n")
	}))
	defer srv.Close()
	p := Provider{Client: srv.Client()}
	var delta string
	m, e := p.responses(context.Background(), srv.URL, "test-model", "secret", "account", "session", []Message{{Role: "user", Content: "hello"}}, toolSpecs(), false, func(s string) { delta += s })
	if e != nil {
		t.Fatal(e)
	}
	if delta != "Hello" || len(m.Calls) != 1 || !strings.Contains(m.Content, "https://example.com") {
		t.Fatal(m)
	}
	_, input := responseInput([]Message{m, {Role: "tool", CallID: "call_1", Content: "done"}})
	if len(input) != 3 {
		t.Fatal("response items not preserved")
	}
}
func TestTruncatedStreamFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}))
	defer srv.Close()
	p := Provider{Client: srv.Client()}
	_, e := p.responses(context.Background(), srv.URL, "m", "k", "", "s", nil, nil, false, nil)
	if e == nil {
		t.Fatal("accepted partial response")
	}
}

func TestOpenCodeProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "messages", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-opencode-session") != "session" || !strings.HasPrefix(r.Header.Get("User-Agent"), "alina/") {
					t.Error("missing client identity")
				}
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "chat":
					if b["messages"] == nil {
						t.Error("missing messages")
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"ok","tool_calls":[{"id":"c","function":{"name":"shell","arguments":"{}"}}]}}]}`)
				case "messages":
					if r.Header.Get("anthropic-version") == "" {
						t.Error("missing anthropic header")
					}
					fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"c","name":"shell","input":{}}]}`)
				case "responses":
					fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"function_call","call_id":"c","name":"shell","arguments":"{}"}]}`)
				}
			}))
			defer srv.Close()
			c := DefaultConfig()
			c.Provider = "opencode-go"
			c.OpenCodeKey = "secret"
			c.OpenCodeAPI = protocol
			p := Provider{Config: c, Client: srv.Client(), BaseURL: srv.URL}
			m, e := p.Complete(context.Background(), "session", []Message{{Role: "user", Content: "hello"}}, toolSpecs(), nil)
			if e != nil || m.Content != "ok" || len(m.Calls) != 1 {
				t.Fatal(m, e)
			}
		})
	}
}
func TestSearchProviders(t *testing.T) {
	for _, name := range []string{"tavily", "brave"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "tavily" {
					if r.Header.Get("Authorization") != "Bearer tavily-secret" {
						t.Error("missing token")
					}
					fmt.Fprint(w, `{"results":[{"title":"Termux","url":"https://termux.dev","content":"Terminal"}]}`)
				} else {
					if r.Header.Get("X-Subscription-Token") != "brave-secret" || r.URL.Query().Get("q") != "termux" {
						t.Error("bad brave request")
					}
					fmt.Fprint(w, `{"web":{"results":[{"title":"Termux","url":"https://termux.dev","description":"Terminal"}]}}`)
				}
			}))
			defer srv.Close()
			s := Search{Config: SearchConfig{TavilyKey: "tavily-secret", BraveKey: "brave-secret"}, Client: srv.Client(), TavilyURL: srv.URL, BraveURL: srv.URL}
			r, e := s.Run(context.Background(), "s", name, "termux")
			if e != nil || !strings.Contains(r, "https://termux.dev") {
				t.Fatal(r, e)
			}
		})
	}
}

func TestTelegramOwnerAndDuplicateUpdates(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{Command: "printf test", Network: true})
	sent := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent++; fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer srv.Close()
	tg := NewTelegram(e.Dir, TelegramConfig{Enabled: true, Token: "fake", OwnerID: 42}, e, srv.Client())
	tg.BaseURL = srv.URL
	var u tgUpdate
	json.Unmarshal([]byte(`{"update_id":1,"message":{"text":"hi","from":{"id":43},"chat":{"id":43,"type":"private"}}}`), &u)
	if er := tg.process(context.Background(), u); er != nil {
		t.Fatal(er)
	}
	if len(e.Jobs("")) != 0 || sent != 0 {
		t.Fatal("unauthorized user accepted")
	}
	u.Message.From.ID = 42
	u.Message.Chat.ID = 42
	if er := tg.process(context.Background(), u); er != nil {
		t.Fatal(er)
	}
	if er := tg.process(context.Background(), u); er != nil {
		t.Fatal(er)
	}
	if len(e.Jobs("")) != 1 {
		t.Fatal("duplicate Telegram job")
	}
}
func TestSetupPersistenceAndEOF(t *testing.T) {
	d := t.TempDir()
	input := "1\n\n" + d + "\nn\nn\nnone\nn\nn\nn\n\nn\nUTC\n\nn\ns\n"
	var out bytes.Buffer
	if e := Setup(context.Background(), d, bufio.NewReader(strings.NewReader(input)), &out); e != nil {
		t.Fatal(e, out.String())
	}
	c, e := LoadConfig(d)
	if e != nil || c.Search.Default != "none" {
		t.Fatal(c, e)
	}
	c.OpenCodeKey = "test-secret"
	if e = SaveConfig(d, c); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(filepath.Join(d, "config.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("secret file permissions")
	}
	before, _ := os.ReadFile(filepath.Join(d, "config.json"))
	if e = Setup(context.Background(), d, bufio.NewReader(strings.NewReader("")), &out); e == nil {
		t.Fatal("EOF accepted")
	}
	after, _ := os.ReadFile(filepath.Join(d, "config.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled setup changed config")
	}
	if strings.Contains(out.String(), "test-secret") {
		t.Fatal("secret printed")
	}
}
func TestAPIRejectsTraversalAndUnknownScope(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	h := handler(e)
	r := httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(`{"session":"../escape","message":"hi"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
