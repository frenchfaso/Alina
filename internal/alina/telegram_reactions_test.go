package alina

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reactionFixture(t *testing.T, update, user, message int64, old, next string) tgUpdate {
	t.Helper()
	var u tgUpdate
	raw := fmt.Sprintf(`{"update_id":%d,"message_reaction":{"chat":{"id":%d,"type":"private"},"user":{"id":%d},"message_id":%d,"date":%d,"old_reaction":%s,"new_reaction":%s}}`, update, user, user, message, time.Now().Unix(), old, next)
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTelegramReactionFeedbackRecallAndRemoval(t *testing.T) {
	e := familyEngine(t, nil)
	sends := 0
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatal("unexpected network request")
		}
		sends++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, 100+sends)))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	if err := tg.sendChunks(e.ctx, 42, telegramText("I found a **quieter route**."), nil); err != nil {
		t.Fatal(err)
	}
	added := reactionFixture(t, 10, 42, 101, `[]`, `[{"type":"emoji","emoji":"❤"}]`)
	for i := 0; i < 2; i++ {
		if err := tg.process(e.ctx, added); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the receiver: references and update deduplication are in SQLite.
	tg = NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	removed := reactionFixture(t, 11, 42, 101, `[{"type":"emoji","emoji":"❤"}]`, `[]`)
	if err := tg.process(e.ctx, removed); err != nil {
		t.Fatal(err)
	}
	family, _ := tg.engineFor(84)
	var count int
	if err := family.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE session='telegram-reactions'").Scan(&count); err != nil || count != 2 {
		t.Fatal("reaction replayed or lost", count, err)
	}
	context, err := family.Memory.RelevantContext(e.ctx, time.Now(), "tg-42")
	for _, text := range []string{"Alex", "quieter route", "❤", `"new_reactions":[]`, "not a request or permission"} {
		if err != nil || !strings.Contains(context, text) {
			t.Fatal("missing feedback context", text, err, context)
		}
	}
	other, _ := tg.engineFor(126)
	context, err = other.Memory.RelevantContext(e.ctx, time.Now(), "local")
	if err != nil || strings.Contains(context, "quieter route") {
		t.Fatal("reaction crossed family", context, err)
	}
	_, _, changed, err := e.dreamScopes(e.ctx)
	if err != nil || !changed {
		t.Fatal("dream cannot see feedback", err)
	}
	for _, scope := range e.engines() {
		if len(scope.Jobs("")) != 0 || len(scope.Permissions.List()) != 0 {
			t.Fatal("reaction started work or granted permission")
		}
	}
	if sends != 1 {
		t.Fatal("reaction caused a notification", sends)
	}
}

func TestTelegramReactionIdentityAndReferenceBoundaries(t *testing.T) {
	e := familyEngine(t, nil)
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, nil)
	tg.rememberMessage(e.ctx, 42, 100, "Alina", "home-only message")
	for _, mode := range []string{"unknown-user", "other-family", "other-chat", "group", "anonymous", "bot", "old-scope", "unknown-message", "other-bot"} {
		u := reactionFixture(t, 20, 42, 100, `[]`, `[{"type":"emoji","emoji":"👍"}]`)
		local := NewTelegram(e.AdminDir, tg.Config, e, nil)
		switch mode {
		case "unknown-user":
			u.Reaction.User.ID, u.Reaction.Chat.ID = 999, 999
		case "other-family":
			u.Reaction.User.ID, u.Reaction.Chat.ID = 126, 126
		case "other-chat":
			u.Reaction.Chat.ID = 84
		case "group":
			u.Reaction.Chat.Type = "group"
		case "anonymous":
			u.Reaction.User = nil
		case "bot":
			u.Reaction.User.IsBot = true
		case "old-scope":
			u.Scope = "old-family"
		case "unknown-message":
			u.Reaction.MessageID = 999
		case "other-bot":
			local.Config.Binding = "replacement-bot"
		}
		if err := local.process(e.ctx, u); err != nil {
			t.Fatal(mode, err)
		}
	}
	for _, scope := range e.engines() {
		var count int
		if err := scope.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE session='telegram-reactions'").Scan(&count); err != nil || count != 0 {
			t.Fatal("untrusted reaction recorded", count, err)
		}
	}
}

func TestTelegramPairingPreservesReactionSubscriptionAndFeedback(t *testing.T) {
	e := familyEngine(t, nil)
	u := reactionFixture(t, 30, 42, 100, `[]`, `[{"type":"custom_emoji","custom_emoji_id":"12345"}]`)
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Offset  int64
			Allowed []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(jsonText(req.Allowed), "message_reaction") {
			t.Fatal("pairing unsubscribed from reactions")
		}
		body := `{"ok":true,"result":[]}`
		if req.Offset == 0 {
			body = `{"ok":true,"result":[` + jsonText(u) + `,{"update_id":31,"message":{"text":"/start pairing","from":{"id":84},"chat":{"id":84,"type":"private"}}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	tg.rememberMessage(e.ctx, 42, 100, "Alina", "Remember this file")
	if id, err := pairTelegram(e.ctx, tg, "pairing", e.Config.Users); err != nil || id != 84 {
		t.Fatal(id, err)
	}
	tg = NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	if len(tg.state.Pending) != 1 || tg.state.Pending[0].Reaction == nil || tg.state.Pending[0].Scope != e.Config.Users[0].scope() {
		t.Fatal("reaction lost during pairing", tg.state)
	}
	if err := tg.process(e.ctx, tg.state.Pending[0]); err != nil {
		t.Fatal(err)
	}
	home, _ := tg.engineFor(42)
	context, err := home.Memory.RelevantContext(e.ctx, time.Now(), "local")
	if err != nil || !strings.Contains(context, "12345") {
		t.Fatal("custom reaction lost", context, err)
	}
}

func TestTelegramReactionsNeverApprovePendingWork(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{Command: "printf approved", Network: true})
	tg := NewTelegram(e.Dir, TelegramConfig{OwnerID: 42}, e, nil)
	j, err := e.Submit("chat", tg.owner(), "test approval")
	if err != nil {
		t.Fatal(err)
	}
	before := awaitStatus(t, e, j.ID, "approval")
	tg.rememberMessage(e.ctx, 42, 100, "Alina", "Approve this command?")
	if err := tg.process(e.ctx, reactionFixture(t, 40, 42, 100, `[]`, `[{"type":"emoji","emoji":"👍"}]`)); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Get(j.ID)
	if after.Status != "approval" || after.Approval.ID != before.Approval.ID || len(e.Permissions.List()) != 0 {
		t.Fatal("reaction changed pending consent", after)
	}
}

func TestTelegramDocumentReactionReference(t *testing.T) {
	e := newTestEngine(t, nil)
	e.Config.Telegram = TelegramConfig{Enabled: true, Token: "fixture", OwnerID: 42}
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			t.Fatal("unexpected endpoint")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":123}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, e.Config.Telegram, e, client)
	path := filepath.Join(e.Workspace(), "report.txt")
	if err := os.WriteFile(path, []byte("Useful results"), 0600); err != nil {
		t.Fatal(err)
	}
	j := &runningJob{Job: Job{ID: "file-reaction", Owner: tg.owner(), Kind: "chat"}, ctx: e.ctx}
	if _, err := e.queueFile(j, jsonText(map[string]string{"path": path})); err != nil {
		t.Fatal(err)
	}
	if err := tg.sendDocument(e.ctx, 42, e, j.OutputFiles[0]); err != nil {
		t.Fatal(err)
	}
	if err := tg.process(e.ctx, reactionFixture(t, 50, 42, 123, `[]`, `[{"type":"emoji","emoji":"🔥"}]`)); err != nil {
		t.Fatal(err)
	}
	context, err := e.Memory.RelevantContext(e.ctx, time.Now(), "local")
	if err != nil || !strings.Contains(context, "report.txt") {
		t.Fatal(context, err)
	}
}
