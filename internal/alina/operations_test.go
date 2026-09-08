package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigPatchRedactionValidationAndRepair(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.OpenCodeKey = "DO_NOT_PRINT_KEY"
	c.Search.BraveKey = "OTHER_SECRET"
	c.Telegram = TelegramConfig{Token: "BOT_SECRET", OwnerID: 42}
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := configCLI(context.Background(), dir, nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "DO_NOT_PRINT_KEY") {
		t.Fatal("credential leaked")
	}
	var patch map[string]any
	if err := json.Unmarshal(out.Bytes(), &patch); err != nil {
		t.Fatal(err)
	}
	patch["reasoning_effort"] = "high"
	out.Reset()
	if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(jsonText(patch)), &out); err != nil {
		t.Fatal(err)
	}
	after, err := LoadConfig(dir)
	if err != nil || after.OpenCodeKey != c.OpenCodeKey || after.Search.BraveKey != c.Search.BraveKey || after.ReasoningEffort != "high" {
		t.Fatal("patch lost existing fields", err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	for _, invalid := range []string{`{"memory":{"dreem":false}}`, `{"context_tokens":1}`, `{"telegram":{"enabled":true,"token":null}}`, `[]`, "", `{"search":false}`} {
		out.Reset()
		if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(invalid), &out); err == nil {
			t.Fatal("invalid patch accepted", invalid)
		}
		current, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if !bytes.Equal(before, current) {
			t.Fatal("invalid patch changed config")
		}
	}
	bad := strings.Replace(string(before), `"reasoning_effort": "high"`, `"reasoning_effort": "typo"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(`{"reasoning_effort":"medium","search":{"brave_key":null}}`), &out); err != nil {
		t.Fatal(err)
	}
	after, err = LoadConfig(dir)
	if err != nil || after.Search.BraveKey != "" || after.OpenCodeKey != c.OpenCodeKey {
		t.Fatal("repair/reset failed", err)
	}
	lock, err := lockDaemonState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(`{"reasoning_effort":"high"}`), &out); !errors.Is(err, errStateLocked) {
		t.Fatal("configuration changed while daemon locked", err)
	}
	if err := diagnose(context.Background(), dir, true, false, &out, newHTTPClient()); !errors.Is(err, errStateLocked) {
		t.Fatal("live diagnostics raced a daemon", err)
	}
}

func TestOperationsCLIHasNoLegacyCommands(t *testing.T) {
	for _, name := range []string{"ask", "job", "approve", "cancel", "steer", "resume", "permissions", "revoke", "memory", "dream", "tasks", "login", "intentions", "version"} {
		var out bytes.Buffer
		err := commandCLI(context.Background(), t.TempDir(), []string{name}, bufio.NewReader(strings.NewReader("")), &out, false)
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatal("legacy alias retained", name, err)
		}
	}
	var out bytes.Buffer
	if err := apiCLI(context.Background(), "", nil, strings.NewReader(""), &out); err != nil || !json.Valid(out.Bytes()) {
		t.Fatal("API not discoverable", err)
	}
	for _, path := range []string{"https://example.org/v1/status", "//example.org/v1/status", "/etc/passwd", "/v1/status#fragment"} {
		if err := apiCLI(context.Background(), "", []string{"GET", path}, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("nonlocal API path accepted", path)
		}
	}
}

func TestOperationalEventsTraceWithoutContent(t *testing.T) {
	events, err := openEventLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	calls := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls++
		if calls == 1 {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "PRIVATE_CALL_ARGUMENT", Name: "read", Arguments: `{"path":"/missing/PRIVATE_PATH"}`}}}, nil
		}
		return Message{Role: "assistant", Content: "PRIVATE_REPLY"}, nil
	}))
	e.Events = events
	e.Memory.Events = events
	job, err := e.Submit("local", "local", "PRIVATE_PROMPT")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, job.ID, "completed")
	events.emit("fixture.error", fmt.Errorf("PRIVATE_ERROR contains credential: %w", &remoteHTTPError{Status: 429}), "job_id", job.ID)
	var out bytes.Buffer
	if err := logsCLI(context.Background(), filepath.Dir(filepath.Dir(events.path)), []string{"--job", job.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "PRIVATE_") {
		t.Fatal("private content reached logs", out.String())
	}
	for _, event := range []string{"job.queued", "job.started", "model.started", "model.finished", "tool.started", "tool.finished", "job.finished"} {
		if !strings.Contains(out.String(), event) {
			t.Fatal("missing trace event", event)
		}
	}
	if !strings.Contains(out.String(), `"http_status":429`) {
		t.Fatal("HTTP failure not classified", out.String())
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if !json.Valid(line) {
			t.Fatal("invalid JSONL")
		}
	}
	out.Reset()
	if err := logsCLI(context.Background(), filepath.Dir(filepath.Dir(events.path)), []string{"--level", "ERROR", "--lines", "1"}, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(out.Bytes(), []byte("\n")) != 1 || !strings.Contains(out.String(), "fixture.error") {
		t.Fatal("log filter/tail failed")
	}
}

func TestEventLogConcurrentRotationAndFailure(t *testing.T) {
	dir := t.TempDir()
	events, err := openEventLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	events.limit = 768
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				events.emit("fixture.event", nil, "job_id", "test")
			}
		}()
	}
	wg.Wait()
	files, err := filepath.Glob(events.path + "*")
	if err != nil || len(files) != logCopies {
		t.Fatal("rotation count", files, err)
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || info.Size() > events.limit || info.Mode().Perm() != 0600 {
			t.Fatal("unbounded/nonprivate log", path, err)
		}
		b, _ := os.ReadFile(path)
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatal("torn log record")
			}
		}
	}
	if events.health()["dropped_events"].(int) != 0 {
		t.Fatal("concurrent writes dropped")
	}
	// A full/invalid target must be visible in health without aborting jobs.
	events.mu.Lock()
	events.file.Close()
	events.file = nil
	events.path = dir
	events.mu.Unlock()
	events.emit("fixture.failure", nil)
	if events.health()["ok"] != false || events.health()["dropped_events"].(int) != 1 {
		t.Fatal("write failure was invisible")
	}
}

type eventCapture struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *eventCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}
func (c *eventCapture) text() string { c.mu.Lock(); defer c.mu.Unlock(); return c.b.String() }
func awaitLog(t *testing.T, out *eventCapture, match string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.text(), match) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("log follow did not catch up", out.text())
}

func TestLogFollowAcrossRotationAndPartialLine(t *testing.T) {
	dir := t.TempDir()
	events, err := openEventLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	events.limit = 600
	events.emit("before.rotation", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out eventCapture
	done := make(chan error, 1)
	go func() { done <- logsCLI(ctx, dir, []string{"--follow"}, &out) }()
	awaitLog(t, &out, "before.rotation")
	for i := 0; i < 4; i++ {
		events.emit(fmt.Sprintf("after.rotation.%d", i), nil)
	}
	awaitLog(t, &out, "after.rotation.3")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.text(), "before.rotation") != 1 {
		t.Fatal("follow duplicated initial event")
	}
	for i := 0; i < 4; i++ {
		if strings.Count(out.text(), fmt.Sprintf("after.rotation.%d", i)) != 1 {
			t.Fatal("rotation lost an event", out.text())
		}
	}
	// An incomplete tail must be reread once the newline arrives.
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(events.path, []byte(`{"level":"INFO","msg":"partial`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	out = eventCapture{}
	done = make(chan error, 1)
	go func() { done <- logsCLI(ctx, dir, []string{"--follow"}, &out) }()
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(events.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(".complete\"}\n")
	f.Close()
	awaitLog(t, &out, "partial.complete")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDoctorSafeRepairsAndReadOnlyMemory(t *testing.T) {
	dir, err := os.MkdirTemp("", "alina-diag-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := DefaultConfig()
	c.WorkDir = dir
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_, sqliteErr := e.Memory.DB.Exec("SELECT PRIVATE_DEBUG_COLUMN")
	details := errorInfo(sqliteErr)
	if details["code"] != "database_error" || details["sqlite_code"] == nil || strings.Contains(jsonText(details), "PRIVATE_DEBUG_COLUMN") {
		t.Fatal("SQLite error was misclassified or exposed SQL", details)
	}
	c.Provider = "opencode-go"
	c.OpenCodeAPI = "responses"
	c.OpenCodeKey = "PRIVATE_KEY"
	c.Search.Default = "none"
	if err := SaveConfig(e.Dir, c); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(e.Dir, "config.json"), 0644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socketPath(e.Dir))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	var out bytes.Buffer
	if err := diagnose(context.Background(), e.Dir, false, false, &out, newHTTPClient()); err == nil {
		t.Fatal("loose credential permissions accepted")
	}
	if !json.Valid(out.Bytes()) || strings.Contains(out.String(), "PRIVATE_KEY") {
		t.Fatal("invalid/private diagnostic output")
	}
	out.Reset()
	if err := diagnose(context.Background(), e.Dir, false, true, &out, newHTTPClient()); err != nil {
		t.Fatal(err, out.String())
	}
	if _, err = os.Lstat(socketPath(e.Dir)); !os.IsNotExist(err) {
		t.Fatal("stale socket not removed")
	}
	info, _ := os.Stat(filepath.Join(e.Dir, "config.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions not fixed")
	}
	if _, err := e.Memory.DB.Exec("SELECT 1"); err != nil {
		t.Fatal("doctor broke live memory handle", err)
	}
	if err := os.WriteFile(socketPath(e.Dir), []byte("PRIVATE_FILE"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := diagnose(context.Background(), e.Dir, false, true, &out, newHTTPClient()); err == nil {
		t.Fatal("socket-path file treated as repaired")
	}
	b, _ := os.ReadFile(socketPath(e.Dir))
	if string(b) != "PRIVATE_FILE" {
		t.Fatal("doctor deleted a regular file")
	}
}

func TestAPITraceExcludesRequestAndResponseBodies(t *testing.T) {
	events, err := openEventLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "PRIVATE_RESPONSE", 400) })
	h := logAPI(mux, events)
	r := httptest.NewRequest("POST", "http://alina/v1/jobs?key=PRIVATE_QUERY", strings.NewReader("PRIVATE_BODY"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := os.ReadFile(events.path)
	if bytes.Contains(b, []byte("PRIVATE_")) || !bytes.Contains(b, []byte("POST /v1/jobs")) || w.Header().Get("X-Alina-Request-ID") == "" {
		t.Fatal("request logging failed privacy/trace contract", string(b))
	}
}

func TestMinimalCLIRequestAndJSONErrors(t *testing.T) {
	dir, err := os.MkdirTemp("", "alina-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := DefaultConfig()
	c.WorkDir = dir
	model := modelFunc(func(_ context.Context, _ string, messages []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		found := false
		for _, m := range messages {
			if m.Role == "user" && strings.Contains(m.Content, "CHECK_INPUT") && strings.Contains(m.Content, "PIPE_CONTEXT") {
				found = true
			}
		}
		if !found {
			return Message{}, errors.New("combined input missing")
		}
		return Message{Role: "assistant", Content: "CLI_OK"}, nil
	})
	e, err := NewEngine(dir, c, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ln, err := net.Listen("unix", socketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler(e)}
	defer srv.Close()
	go srv.Serve(ln)
	var out bytes.Buffer
	if err := commandCLI(context.Background(), dir, []string{"chat", "CHECK_INPUT"}, bufio.NewReader(strings.NewReader("PIPE_CONTEXT")), &out, false); err != nil || !strings.Contains(out.String(), "CLI_OK") {
		t.Fatal("one-shot chat failed", err, out.String())
	}
	out.Reset()
	if err := commandCLI(context.Background(), dir, []string{"status"}, bufio.NewReader(strings.NewReader("")), &out, false); err != nil || !json.Valid(out.Bytes()) || strings.Contains(out.String(), "CHECK_INPUT") {
		t.Fatal("status is not a bounded metadata summary", err, out.String())
	}
	out.Reset()
	err = apiCLI(context.Background(), dir, []string{"POST", "/v1/jobs/absent/cancel"}, strings.NewReader(""), &out)
	var apiErr *localAPIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatal("API failure not actionable", err)
	}
	WriteError(&out, err)
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"http_status":400`) {
		t.Fatal("CLI error not structured", out.String())
	}
}

func TestShellDiagnosticsKeepInstanceWithoutCredentials(t *testing.T) {
	t.Setenv("ALINA_HOME", "/example/alina-instance")
	t.Setenv("XDG_CONFIG_HOME", "/example/config")
	t.Setenv("OPENAI_API_KEY", "PRIVATE_ENV_SECRET")
	env := strings.Join(shellEnvironment(), "\n")
	if !strings.Contains(env, "ALINA_HOME=/example/alina-instance") || !strings.Contains(env, "XDG_CONFIG_HOME=/example/config") || strings.Contains(env, "PRIVATE_ENV_SECRET") {
		t.Fatal("diagnostic shell environment is wrong")
	}
}

func TestDoctorLiveChecksWithFixtures(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	c.Telegram = TelegramConfig{Enabled: true, Token: "PRIVATE_BOT", OwnerID: 42}
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	setupCredential(t, dir)
	f := &setupFixture{t: t}
	if err := diagnose(context.Background(), dir, true, false, &f.out, f.client()); err != nil {
		t.Fatal(err, f.out.String())
	}
	if strings.Contains(f.out.String(), "PRIVATE_BOT") {
		t.Fatal("doctor disclosed a credential")
	}
	f.out.Reset()
	f.webhook = true
	if err := diagnose(context.Background(), dir, true, false, &f.out, f.client()); err == nil {
		t.Fatal("webhook conflict passed live diagnostics")
	}
	if !strings.Contains(f.out.String(), "telegram_webhook") {
		t.Fatal("webhook problem not classified")
	}
}
