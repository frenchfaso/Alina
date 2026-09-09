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
	"sync/atomic"
	"testing"
	"time"
)

func TestSetupSharesDaemonStateLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockDaemonState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, args := range [][]string{{"setup", "--no-start"}, {"setup", "--advanced"}, {"setup", "login"}, {"setup", "telegram"}} {
		// No socket exists yet: checking only reachability misses the lock.
		err := commandCLI(context.Background(), dir, args, bufio.NewReader(strings.NewReader("")), io.Discard, false)
		if !errors.Is(err, errStateLocked) {
			t.Fatalf("%v did not honor the instance lock: %v", args, err)
		}
	}
}

func TestSchedulerPruningRollsBackOnWriteFailure(t *testing.T) {
	e := newTestEngine(t, nil)
	s := e.Scheduler
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("old-%d", i)
		s.tasks[id] = ScheduledTask{ID: id, Once: true, LastJob: "completed"}
	}
	before := jsonText(s.tasks)
	path := filepath.Join(e.Dir, "tasks.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddOnce("Once", time.Now().Add(time.Hour).Format(time.RFC3339), "Do it", "local", "user", "")
	if err == nil {
		t.Fatal("write failure ignored")
	}
	if jsonText(s.tasks) != before {
		t.Fatal("failed save pruned the in-memory task history")
	}
}

func TestDoctorDoesNotHideCorruptionAfterPermissionRepair(t *testing.T) {
	e := newTestEngine(t, nil)
	c := e.Config
	c.Provider, c.Model, c.OpenCodeKey, c.Search.Default = "opencode-go", "fixture", "private-fixture", "none"
	if err := SaveConfig(e.Dir, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.Dir, "tasks.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := diagnose(context.Background(), e.Dir, false, true, &out, newHTTPClient())
	if err == nil || !strings.Contains(out.String(), `"state:tasks.json"`) {
		t.Fatal("corrupt JSON passed after chmod", err, out.String())
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("safe permission repair was not applied")
	}
}

func TestTelegramScheduledApprovalFitsProtocol(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{Command: "printf approved", Network: true})
	buttons := make(chan []string, 4)
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Markup struct {
				Rows [][]struct {
					Data string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		var data []string
		for _, row := range body.Markup.Rows {
			for _, b := range row {
				data = append(data, b.Data)
			}
		}
		if len(data) > 0 {
			buttons <- data
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)), Header: http.Header{}}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	j, err := e.SubmitKey("scheduled", tg.owner(), "Run a scheduled action", "cron-"+strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "approval")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); tg.notify(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case data := <-buttons:
		for _, value := range data {
			if len(value) > 64 {
				t.Fatalf("approval callback is %d bytes: %s", len(value), value)
			}
		}
		var callback tgUpdate
		raw := fmt.Sprintf(`{"callback_query":{"id":"click","data":%q,"from":{"id":42},"message":{"chat":{"id":42,"type":"private"}}}}`, data[0])
		if err := json.Unmarshal([]byte(raw), &callback); err != nil {
			t.Fatal(err)
		}
		if err := tg.process(ctx, callback); err != nil {
			t.Fatal(err)
		}
		awaitStatus(t, e, j.ID, "completed")
	case <-time.After(5 * time.Second):
		t.Fatal("approval not delivered")
	}
}

func TestRemovedTelegramResumeNeverStartsWork(t *testing.T) {
	var calls atomic.Int32
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls.Add(1)
		return Message{Role: "assistant", Content: "done"}, nil
	}))
	var attempts atomic.Int32
	client := &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
		status := 200
		if attempts.Add(1) == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)), Header: http.Header{}}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	old, err := e.Submit("tg-source", tg.owner(), "original task")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, old.ID, "completed")
	u := photoUpdate()
	u.ID, u.Message.Photo, u.Message.Text = 88, nil, "/resume "+old.ID
	if err := tg.process(e.ctx, u); err == nil {
		t.Fatal("fixture should fail the first acknowledgement")
	}
	e.wg.Wait()
	if err := tg.process(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	if calls.Load() != 1 || len(e.Jobs(tg.owner())) != 1 {
		t.Fatalf("removed resume command started work: %d model calls", calls.Load())
	}
}

func TestWebFetchKeepsUTF8AfterLongASCIIPrefix(t *testing.T) {
	for _, media := range []string{"application/json", "text/markdown", "text/plain", "application/xml"} {
		t.Run(media, func(t *testing.T) {
			body := strings.Repeat(" ", 1100) + "caffè 日本語 ☕"
			result, err := webFetch(context.Background(), fetchFixture(media, body), `{"url":"https://example.com"}`)
			if err != nil || !strings.Contains(result, "caffè 日本語 ☕") {
				t.Fatal("UTF-8 text corrupted", err, result)
			}
		})
	}
}

func TestCompletedResponseDoesNotWaitForStreamEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := Provider{Config: DefaultConfig(), Client: srv.Client()}
	answer, err := p.responses(ctx, srv.URL, "fixture", "fixture", "", "test", nil, nil, false, nil)
	if err != nil || answer.Content != "done" {
		t.Fatal("completed response waited for EOF", err)
	}
}

func TestAPIRejectsTrailingJSONBeforeMutation(t *testing.T) {
	e := newTestEngine(t, nil)
	r := httptest.NewRequest("POST", "/v1/tasks", strings.NewReader(`{"name":"test","cron":"@hourly","prompt":"Do work"} {"extra":true}`))
	w := httptest.NewRecorder()
	handler(e).ServeHTTP(w, r)
	if w.Code != 400 || len(e.Scheduler.List()) != 1 {
		t.Fatal("trailing JSON triggered mutation", w.Code)
	}
}

func TestCorrectionCanCiteItsOriginalEvidence(t *testing.T) {
	e := newTestEngine(t, nil)
	m, ctx, now := e.Memory, e.ctx, time.Now()
	old, err := m.Note(ctx, now, "local", "job", "fact", "CEDAR uses the old directory", "")
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := m.Note(ctx, now, "local", "job", "fact", "CEDAR now uses the new directory", old, []string{old})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := m.Recall(ctx, "CEDAR")
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].ID != corrected {
		t.Fatal("a correction hid itself by citing the corrected source", hits, err)
	}
	if err = m.Focus(ctx, corrected, nil, now); err != nil {
		t.Fatal(err)
	}
	text, err := m.Read(ctx, old, now)
	if err != nil || !strings.Contains(text, "SUPERSEDED by "+corrected) || !strings.Contains(text, "old directory") {
		t.Fatal("original evidence was lost", text, err)
	}
}

func TestChatReusesOneLocalConnection(t *testing.T) {
	dir, err := os.MkdirTemp("", "alina-poll-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", socketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var connections, polls atomic.Int32
	srv := &http.Server{ConnState: func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the body so the same connection can be reused.
		io.Copy(io.Discard, r.Body)
		j := Job{ID: "poll-job", Status: "running"}
		if r.Method == "GET" && polls.Add(1) >= 2 {
			j.Status, j.Output = "completed", "done"
		}
		json.NewEncoder(w).Encode(j)
	})}
	defer srv.Close()
	go srv.Serve(ln)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := chat(ctx, dir, "local", bufio.NewReader(strings.NewReader("hello\n")), io.Discard); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 1 {
		t.Fatalf("polling opened %d connections", connections.Load())
	}
}

func TestTaskLimitsCountActiveWorkAndBoundRetainedState(t *testing.T) {
	e := newTestEngine(t, nil)
	s := e.Scheduler
	for i := 0; i < 199; i++ {
		id := fmt.Sprintf("completed-%d", i)
		s.tasks[id] = ScheduledTask{ID: id, Once: true, LastJob: "completed"}
	}
	if _, err := s.Add("Recurring", "@hourly", "Do work", "local", true); err != nil {
		t.Fatal("finished one-shots blocked recurring work", err)
	}
	if len(s.tasks) != 2 {
		t.Fatal("completed history was not pruned", len(s.tasks))
	}
	for i := 0; i < 198; i++ {
		id := fmt.Sprintf("paused-%d", i)
		s.tasks[id] = ScheduledTask{ID: id}
	}
	_, err := s.AddOnce("Next", time.Now().Add(time.Hour).Format(time.RFC3339), "Do work", "local", "user", "")
	if err == nil || len(s.tasks) != 200 {
		t.Fatal("retained state grew without a bound", err, len(s.tasks))
	}
	for i := 0; i < 98; i++ {
		id := fmt.Sprintf("paused-%d", i)
		task := s.tasks[id]
		task.Enabled = true
		s.tasks[id] = task
	}
	if err := s.Change("paused-100", "resume", ""); err == nil || s.tasks["paused-100"].Enabled {
		t.Fatal("resume bypassed the active limit", err)
	}
}

func TestWeekViewPagesThroughSharedArchive(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	for day := 0; day <= 8; day++ {
		if err := m.Record(ctx, now.AddDate(0, 0, -day), "local", "job", "user", fmt.Sprintf("day%d ", day)+strings.Repeat("è日本語", 1800)); err != nil {
			t.Fatal(err)
		}
	}
	var expected strings.Builder
	expected.WriteString("# Previous seven days · archive view\n")
	for day := 7; day >= 1; day-- {
		entries, err := m.entries(ctx, now.In(m.loc).AddDate(0, 0, -day).Format("2006-01-02"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			expected.WriteString(entryText(entry))
		}
	}
	var got strings.Builder
	for offset := 0; ; {
		page, err := m.ReadPage(ctx, "week", offset, now)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(page.Text)
		if page.Next == 0 {
			break
		}
		if page.Next <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = page.Next
	}
	if got.String() != expected.String() {
		t.Fatal("weekly pagination lost, duplicated or included out-of-range events")
	}
}

func TestDisabledDreamConfigStillLoadsScheduler(t *testing.T) {
	c := DefaultConfig()
	c.Memory.Dream = false
	c.Memory.DreamCron = "invalid"
	if err := c.Validate(); err == nil {
		t.Fatal("invalid saved schedule passed validation but would prevent startup")
	}
}

func TestExpiredTelegramCallbackDoesNotBlockUpdates(t *testing.T) {
	e := newTestEngine(t, nil)
	client := &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	var u tgUpdate
	if err := json.Unmarshal([]byte(`{"callback_query":{"id":"expired","data":"a:old-approval:once","from":{"id":42},"message":{"chat":{"id":42,"type":"private"}}}}`), &u); err != nil {
		t.Fatal(err)
	}
	if err := tg.process(e.ctx, u); err != nil {
		t.Fatal("expired callback would block subsequent updates", err)
	}
}

func TestProviderChangeRebuildsForeignRawItems(t *testing.T) {
	responseMessage, err := parseResponse([]json.RawMessage{
		json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"Inspect it"}]}`),
		json.RawMessage(`{"type":"function_call","call_id":"call","name":"read","arguments":"{\"path\":\"file.txt\"}"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Provider, c.OpenCodeAPI, c.OpenCodeKey = "opencode-go", "messages", "fixture"
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(b), `"type":"function_call"`) || !strings.Contains(string(b), `"type":"tool_use"`) {
			return nil, errors.New("Responses items leaked into Messages request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"Inspect again"},{"type":"tool_use","id":"other","name":"read","input":{"path":"other.txt"}}],"stop_reason":"tool_use"}`))}, nil
	})}
	p := Provider{Config: c, Client: client}
	m, err := p.anthropic(context.Background(), "test", []Message{responseMessage}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, input := responseInput([]Message{m})
	encoded := jsonText(input)
	if strings.Contains(encoded, `"type":"tool_use"`) || !strings.Contains(encoded, `"type":"function_call"`) || !strings.Contains(encoded, "other.txt") {
		t.Fatal("Messages items leaked into Responses request", encoded)
	}
}

func TestTelegramDeliversBacklogBeyondRecentHistory(t *testing.T) {
	e := newTestEngine(t, nil)
	sent := make(chan string, 100)
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		sent <- strings.Fields(body.Text)[0]
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	for i := 0; i < 61; i++ {
		j := &runningJob{Job: Job{ID: fmt.Sprintf("backlog-%02d", i), Owner: tg.owner(), Session: "local", Status: "completed", Created: time.Now().Add(time.Duration(i) * time.Second), Output: fmt.Sprintf("backlog-%02d", i)}}
		if err := e.persist(j); err != nil {
			t.Fatal(err)
		}
	}
	tg.state.Delivered["backlog-01"] = "completed"
	tg.state.Delivered["backlog-60"] = "completed"
	if err := tg.saveLocked(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); tg.notify(ctx) }()
	defer func() { cancel(); <-done }()
	seen := map[string]bool{}
	for len(seen) < 59 {
		select {
		case id := <-sent:
			if seen[id] || id == "backlog-01" || id == "backlog-60" {
				t.Fatal("already delivered job was sent again", id)
			}
			seen[id] = true
		case <-ctx.Done():
			t.Fatal("older undelivered jobs fell outside the history window", len(seen))
		}
	}
	// Wait for the last receipt rather than racing the send with cancellation.
	for {
		pending, err := tg.pendingNotifications(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("receipt not persisted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stored := NewTelegram(e.Dir, tg.Config, e, client)
	if len(stored.state.Delivered) != 0 {
		t.Fatal("legacy receipts still grow in JSON")
	}
	pending, err := stored.pendingNotifications(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("restart lost receipts", pending, err)
	}
}
