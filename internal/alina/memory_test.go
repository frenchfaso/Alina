package alina

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func memoryFixture(t *testing.T) (*Memory, time.Time) {
	t.Helper()
	c := DefaultConfig()
	c.Timezone = "Europe/Rome"
	c.WorkDir = t.TempDir()
	m, err := OpenMemory(c.WorkDir, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DB.Close() })
	return m, time.Date(2026, 9, 8, 3, 0, 0, 0, m.loc)
}
func TestDreamReflectsWithoutAgeTiers(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, msg []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		calls++
		if len(tools) != 3 || !strings.Contains(msg[0].Content, systemPrompt) {
			t.Fatal("reflection did not use shared prompt/tools")
		}
		return Message{Role: "assistant", Content: "Nothing needs changing."}, nil
	}))
	m := e.Memory
	now := time.Now()
	ctx := e.ctx
	for d := 9; d >= 0; d-- {
		if err := m.Record(ctx, now.AddDate(0, 0, -d), "local", fmt.Sprintf("job%d", d), "user", fmt.Sprintf("DAY_%d: preferisco strumenti semplici", d)); err != nil {
			t.Fatal(err)
		}
	}
	j := &runningJob{Job: Job{ID: "dream-test", Session: "reflection", Kind: "dream", Owner: "system"}, ctx: ctx}
	if _, err := e.dream(j, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := m.DB.QueryRow("SELECT count(*) FROM journal WHERE role='user'").Scan(&count); err != nil || count != 10 {
		t.Fatal("archive changed", count, err)
	}
	if err := m.DB.QueryRow("SELECT count(*) FROM days").Scan(&count); err != nil || count != 0 {
		t.Fatal("calendar compaction returned", count, err)
	}
	if err := m.DB.QueryRow("SELECT count(*) FROM soul_versions").Scan(&count); err != nil || count != 0 {
		t.Fatal("no-op reflection rewrote soul", count, err)
	}
	r, err := m.Recall(ctx, "DAY_9")
	if err != nil || len(r.Hits) == 0 || !strings.Contains(r.Hits[0].Text, "DAY_9") {
		t.Fatal(r, err)
	}
	if _, err = e.dream(j, now); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("reflection replayed itself", calls)
	}
	info, _ := os.Stat(filepath.Join(m.Dir, "memory", "memory.sqlite"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("database is not private")
	}
}
func TestNoteRejectsInventedSourcesAndKeepsOriginal(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	if err := m.Record(ctx, now.AddDate(0, 0, -90), "test", "job", "user", "Un fatto importante"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Note(ctx, now, "test", "job", "lesson", "invented", "", []string{"fabricated"}); err == nil {
		t.Fatal("fabricated evidence accepted")
	}
	var count int
	m.DB.QueryRow("SELECT count(*) FROM journal").Scan(&count)
	if count != 1 {
		t.Fatal("archive changed")
	}
}
func TestMemorySemanticRetrievalAndModelChange(t *testing.T) {
	m, _ := memoryFixture(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Input string }
		json.NewDecoder(r.Body).Decode(&a)
		if r.Header.Get("Authorization") != "Bearer embedding-fixture" {
			t.Error("missing dedicated embedding key")
		}
		vector := []float32{1, 0}
		if strings.Contains(a.Input, "backup") {
			vector = []float32{0, 1}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}})
	}))
	defer srv.Close()
	m.Config.Memory.EmbeddingURL = srv.URL
	m.Config.Memory.EmbeddingModel = "fixture"
	m.Config.Memory.EmbeddingKey = "embedding-fixture"
	for i, text := range []string{"Il mio animale domestico si chiama Luna", "Il backup parte la domenica"} {
		if _, err := m.DB.Exec("INSERT INTO memories(id,day,text,sources) VALUES(?,?,?,?)", fmt.Sprint(i), "2026-08-01", text, "[]"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := m.Reindex(ctx, 100); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	r, err := m.Recall(ctx, "Come si chiama il gatto?")
	if err != nil || len(r.Hits) == 0 || r.Hits[0].ID != "0" || r.Hits[0].Match != "semantic" {
		t.Fatal(r, err)
	}
	m.Config.Memory.EmbeddingModel = "different"
	r, err = m.Recall(ctx, "gatto")
	if err != nil || len(r.Hits) > 0 {
		t.Fatal("incompatible embedding spaces compared", r, err)
	}
	if n, err := m.Reindex(ctx, 100); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}
func TestMemoryRedactionAndDateViews(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	m.Config.Search.TavilyKey = "private-key-fixture"
	if err := m.Record(ctx, now, "local", "j", "user", "private-key-fixture and Bearer abc.def.ghi"); err != nil {
		t.Fatal(err)
	}
	text, err := m.Read(ctx, "today", now)
	if err != nil || strings.Contains(text, "private-key-fixture") || strings.Contains(text, "abc.def.ghi") || !strings.Contains(text, "[redacted]") {
		t.Fatal(text, err)
	}
	if err = m.Render(now.AddDate(0, 0, 40)); err != nil {
		t.Fatal(err)
	}
	text, err = m.Read(ctx, now.In(m.loc).Format("2006-01-02"), now.AddDate(0, 0, 40))
	if err != nil || !strings.Contains(text, "[redacted]") {
		t.Fatal("old event disappeared", text, err)
	}
}
func TestSchedulerCatchupDedupeAndDST(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	s := e.Scheduler
	task, err := s.Add("test", "0 3 * * *", "check logs", "local", true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(72 * time.Hour)
	task.Next = time.Now().Add(-72 * time.Hour)
	s.mu.Lock()
	s.tasks[task.ID] = task
	dream := s.tasks["dream"]
	dream.Enabled = false
	s.tasks["dream"] = dream
	s.save()
	s.mu.Unlock()
	if err = s.Tick(now); err != nil {
		t.Fatal(err)
	}
	jobs := e.Jobs("local")
	if len(jobs) != 1 {
		t.Fatal("missed runs were not coalesced", len(jobs))
	}
	if err = s.Tick(now); err != nil {
		t.Fatal(err)
	}
	if len(e.Jobs("local")) != 1 {
		t.Fatal("duplicate schedule execution")
	}
	// Emulate a crash after submit and before scheduler checkpoint.
	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()
	if err = s.Tick(now); err != nil {
		t.Fatal(err)
	}
	if len(e.Jobs("local")) != 1 {
		t.Fatal("crash checkpoint replayed occurrence")
	}
	schedule, err := parseSchedule("0 3 * * *", "Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Rome")
	before := time.Date(2026, 3, 28, 3, 0, 0, 0, loc)
	next := schedule.Next(before)
	if next.In(loc).Hour() != 3 || next.Sub(before) != 23*time.Hour {
		t.Fatal("DST not calendar based", next)
	}
	if _, err = parseSchedule("@every 1s", "UTC"); err == nil {
		t.Fatal("unbounded schedule accepted")
	}
}
func TestTelegramPairingRequiresPrivateCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"text":"/start wrong","from":{"id":111},"chat":{"id":111,"type":"private"}}},{"update_id":2,"message":{"text":"/start correct","from":{"id":111},"chat":{"id":111,"type":"group"}}},{"update_id":3,"message":{"text":"/start correct","from":{"id":222},"chat":{"id":222,"type":"private"}}}]}`)
	}))
	defer srv.Close()
	tg := NewTelegram(t.TempDir(), TelegramConfig{Token: "fixture"}, nil, newHTTPClient())
	tg.BaseURL = srv.URL
	id, err := pairTelegram(context.Background(), tg, "correct")
	if err != nil || id != 222 {
		t.Fatal(id, err)
	}
}

func TestIdleDreamDoesNotRewriteSoul(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls++
		return Message{}, nil
	}))
	j := &runningJob{Job: Job{Kind: "dream", Session: "idle", ID: "idle", Owner: "system"}, ctx: e.ctx}
	if _, err := e.dream(j, time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("idle dream spent model calls")
	}
}
