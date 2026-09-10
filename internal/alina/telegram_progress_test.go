package alina

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTelegramProgressEditsOneDraftAndKeepsFinalSeparate(t *testing.T) {
	e := singleFamilyEngine(t, nil)
	family := e.localEngine()
	var methods, texts []string
	var chats []int64
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text  string
			Chat  int64 `json:"chat_id"`
			ID    int64 `json:"message_id"`
			Quiet bool  `json:"disable_notification"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods = append(methods, method)
		texts = append(texts, body.Text)
		chats = append(chats, body.Chat)
		if method == "sendMessage" && body.Text != "Done." && !body.Quiet {
			t.Error("progress notification was not silent")
		}
		if method != "sendMessage" && body.ID != 100 {
			t.Error("lost draft ID", body.ID)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":100}}`))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	j := &runningJob{Job: Job{ID: "progress-job", Session: "chat", Owner: telegramOwner(e.Config.Telegram, 42), Kind: "chat", Status: "running", Created: time.Now()}, ctx: e.ctx}
	family.jobs[j.ID] = j
	if err := family.persist(j); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	family.commentary(j, "I am checking the camera.")
	if delay := tg.syncProgress(e.ctx, now); delay <= 0 || len(methods) != 0 {
		t.Fatal("fast-turn gate missing", delay, methods)
	}
	tg.syncProgress(e.ctx, now.Add(2*time.Second))
	family.commentary(j, "Permission is present; checking the API.")
	tg.syncProgress(e.ctx, now.Add(3*time.Second))
	if len(methods) != 1 {
		t.Fatal("edit cooldown missing", methods)
	}
	tg.syncProgress(e.ctx, now.Add(6*time.Second))
	tg.syncProgress(e.ctx, now.Add(10*time.Second))
	if strings.Join(methods, ",") != "sendMessage,editMessageText" {
		t.Fatal(methods)
	}
	j.Status, j.Output = "completed", "Done."
	if err := family.persist(j); err != nil {
		t.Fatal(err)
	}
	delete(family.jobs, j.ID)
	tg.syncProgress(e.ctx, now.Add(11*time.Second))
	if _, err := tg.deliverPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "sendMessage,editMessageText,deleteMessage,sendMessage" || texts[3] != "Done." {
		t.Fatal(methods, texts)
	}
	for _, id := range chats {
		if id != 42 {
			t.Fatal("progress delivered to other family member", chats)
		}
	}
	var receipts int
	if err := family.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key LIKE 'telegram-progress:%'").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal(receipts, err)
	}
}

func TestTelegramProgressRestartCleanupFailureDoesNotBlockFinal(t *testing.T) {
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "interrupted-draft", Session: "chat", Owner: "telegram:42", Kind: "chat", Status: "interrupted", Output: "Work interrupted."}}
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Memory.DB.Exec("INSERT INTO memory_state VALUES('telegram-progress:interrupted-draft','100')"); err != nil {
		t.Fatal(err)
	}
	sent, deletes := 0, 0
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deletes++
			return nil, errors.New("temporary connection failure")
		}
		sent++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":101}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	now := time.Now()
	if delay := tg.syncProgress(e.ctx, now); delay <= 0 {
		t.Fatal("cleanup retry missing")
	}
	tg.syncProgress(e.ctx, now.Add(time.Second))
	if deletes != 1 {
		t.Fatal("cleanup retry ignores cooldown", deletes)
	}
	if _, err := tg.deliverPending(e.ctx); err != nil || sent != 1 {
		t.Fatal("draft blocked final delivery", sent, err)
	}
}

func TestLoopPublishesOnlyUserFacingCommentary(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls++
		if calls == 1 {
			return Message{Role: "assistant", Content: "Checking what is known.", Reasoning: "private reasoning fixture", Calls: []ToolCall{{ID: "memory", Name: "memory", Arguments: `{"action":"intentions"}`}}}, nil
		}
		close(entered)
		<-release
		return Message{Role: "assistant", Content: "All done."}, nil
	}))
	j, err := e.Submit("chat", "local", "Please check.")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("loop stalled")
	}
	e.mu.Lock()
	text := e.jobs[j.ID].commentary
	e.mu.Unlock()
	close(release)
	if text != "Checking what is known." {
		t.Fatal("wrong progress", text)
	}
	awaitStatus(t, e, j.ID, "completed")
}
