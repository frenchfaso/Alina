package alina

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func TestTelegramFormatting(t *testing.T) {
	chunks := telegramText("Ciao **Frenchfaso** 😊!\n\n[Termux](https://termux.dev) e `a < b`.")
	if len(chunks) != 1 || strings.Contains(chunks[0].Text, "**") || !strings.Contains(chunks[0].Text, "a < b") {
		t.Fatal(chunks)
	}
	kinds := map[string]bool{}
	for _, e := range chunks[0].Entities {
		kinds[e.Type] = true
	}
	if !kinds["bold"] || !kinds["text_link"] || !kinds["code"] {
		t.Fatal(chunks)
	}
	text := strings.Repeat("😊", 2000)
	chunks = telegramText("**" + text + "**")
	var got strings.Builder
	for _, c := range chunks {
		got.WriteString(c.Text)
		units := len(utf16.Encode([]rune(c.Text)))
		if units > 3500 {
			t.Fatal("oversized chunk")
		}
		for _, e := range c.Entities {
			if e.Offset < 0 || e.Offset+e.Length > units {
				t.Fatal("invalid entity")
			}
		}
	}
	if got.String() != text || len(chunks) != 2 {
		t.Fatal("split lost text")
	}
	code := telegramText("```go\n" + strings.Repeat("x", 4000) + "\n```")
	for _, c := range code {
		if len(c.Entities) != 1 || c.Entities[0].Type != "pre" {
			t.Fatal(code)
		}
	}
}

func TestTelegramFormattingFallback(t *testing.T) {
	calls := 0
	tg := NewTelegram("", TelegramConfig{Token: "fixture"}, nil, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		calls++
		body := `{"ok":true,"result":{}}`
		if calls == 1 {
			if b["entities"] == nil {
				t.Fatal("missing styles")
			}
			body = `{"ok":false,"error_code":400}`
		} else if b["entities"] != nil || b["text"] != "Hello" {
			t.Fatal("invalid fallback", b)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err := tg.sendChunks(context.Background(), 42, telegramText("**Hello**"), nil); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}

func TestTelegramQuoteContext(t *testing.T) {
	for _, sameChat := range []bool{true, false} {
		e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		}))
		tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, nil)
		var u tgUpdate
		json.Unmarshal([]byte(`{"update_id":1,"message":{"text":"Let's do this","from":{"id":42},"chat":{"id":42,"type":"private"},"reply_to_message":{"text":"Old proposal","chat":{"id":42,"type":"private"}}}}`), &u)
		if !sameChat {
			u.Message.ReplyTo.Chat.ID = 84
		}
		if err := tg.process(e.ctx, u); err != nil {
			t.Fatal(err)
		}
		j := awaitStatus(t, e, "tg1", "completed")
		if strings.Contains(j.Input, "Old proposal") != sameChat {
			t.Fatal("quote scope", j.Input)
		}
	}
}

func TestTelegramTypingOnlyForActiveChat(t *testing.T) {
	e := newTestEngine(t, nil)
	var calls atomic.Int32
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "sendChatAction") {
			t.Error("unexpected endpoint")
		}
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	})})
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { defer close(done); tg.typing(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("idle activity")
	}
	e.mu.Lock()
	e.jobs["test"] = &runningJob{Job: Job{ID: "test", Status: "running", Owner: tg.owner(), Kind: "dream"}}
	e.mu.Unlock()
	e.jobChanged.wake()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("dream typing")
	}
	e.mu.Lock()
	e.jobs["test"].Kind = "chat"
	e.mu.Unlock()
	e.jobChanged.wake()
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatal("chat typing not started")
	}
}

func TestTelegramSetupRecoveryKeepsPairing(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		dir := t.TempDir()
		c := DefaultConfig()
		c.WorkDir = dir
		c.Telegram = TelegramConfig{Enabled: enabled, Token: "fixture-existing", OwnerID: 42, Binding: "existing"}
		c.Users = []User{{ID: "owner", Name: "Alex", TelegramID: 42, Family: "home"}}
		c.LocalUser = "owner"
		if err := SaveConfig(dir, c); err != nil {
			t.Fatal(err)
		}
		setupCredential(t, dir)
		fixture := &setupFixture{t: t}
		input := "s\n"
		if enabled {
			fixture.botFailures = 1
			input = "3\n"
		}
		if err := fixture.run(dir, input, false); err != nil {
			t.Fatal(err, fixture.out.String())
		}
		after, err := LoadConfig(dir)
		if err != nil || !after.Telegram.Enabled || after.Telegram.Token != c.Telegram.Token || after.Telegram.Binding != c.Telegram.Binding || len(after.Users) != 1 {
			t.Fatal("configuration lost", err)
		}
		for _, call := range fixture.calls {
			if strings.Contains(call, "getUpdates") {
				t.Fatal("unnecessary pairing")
			}
		}
	}
	// Explicitly disabling Telegram remains possible in the advanced wizard.
	c := DefaultConfig()
	c.Telegram.Enabled = true
	w := wizard{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("n\n")), out: io.Discard}
	if err := w.telegram(&c); err != nil || c.Telegram.Enabled {
		t.Fatal(err)
	}
}

func TestTelegramFileSnapshotAndRetry(t *testing.T) {
	e := newTestEngine(t, nil)
	e.Config.Telegram = TelegramConfig{Enabled: true, Token: "fixture", OwnerID: 42}
	j := &runningJob{Job: Job{ID: "outgoing", Session: "local", Owner: telegramOwner(e.Config.Telegram, 42), Status: "running", Kind: "chat"}, ctx: e.ctx}
	path := filepath.Join(e.Workspace(), "report.txt")
	os.WriteFile(path, []byte("original"), 0600)
	if _, err := e.queueFile(j, jsonText(map[string]string{"path": path})); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("changed"), 0600)
	if _, err := e.queueFile(j, jsonText(map[string]string{"path": filepath.Join(e.AdminDir, "config.json")})); err == nil {
		t.Fatal("administrative file accepted")
	}
	var sends, uploads int
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			sends++
		} else if strings.HasSuffix(r.URL.Path, "sendDocument") {
			uploads++
			if uploads == 1 {
				return nil, errors.New("offline")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			f, h, err := r.FormFile("document")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if h.Filename != "report.txt" || string(b) != "original" || r.FormValue("chat_id") != "42" {
				t.Fatal("bad upload")
			}
		} else {
			t.Fatal("unexpected endpoint")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, e.Config.Telegram, e, client)
	j.Status = "completed"
	j.Output = "Your file"
	e.persist(j)
	if _, err := tg.deliverPending(e.ctx); err == nil {
		t.Fatal("expected transient failure")
	}
	if _, err := tg.deliverPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || uploads != 2 {
		t.Fatal("reply replayed", sends, uploads)
	}
	if pending, err := tg.pendingNotifications(e.ctx); err != nil || len(pending) > 0 {
		t.Fatal(pending, err)
	}
}

func TestTelegramFileToolRespectsFamilyAndTurn(t *testing.T) {
	e := familyEngine(t, nil)
	home := e.scopes["family-home"]
	j := &runningJob{Job: Job{Owner: telegramOwner(e.Config.Telegram, 42), Kind: "chat"}, ctx: e.ctx}
	if !hasTool(home.toolsFor(j), "send_file") {
		t.Fatal("file tool unavailable to Telegram user")
	}
	for _, change := range []struct{ owner, kind string }{
		{telegramOwner(e.Config.Telegram, 126), "chat"},
		{"local:alex", "chat"},
		{telegramOwner(e.Config.Telegram, 42), "dream"},
		{telegramOwner(e.Config.Telegram, 42), "initiative"},
	} {
		j.Owner, j.Kind = change.owner, change.kind
		if hasTool(home.toolsFor(j), "send_file") {
			t.Fatal("file tool exposed outside a user delivery context")
		}
		if _, err := home.queueFile(j, `{"path":"anything"}`); err == nil {
			t.Fatal("file tool bypassed scope")
		}
	}
}
