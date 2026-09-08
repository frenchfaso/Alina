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
	"sync"
	"testing"
	"time"
)

type dreamModel struct {
	mu      sync.Mutex
	calls   int
	invalid bool
}

func (m *dreamModel) Complete(ctx context.Context, session string, msg []Message, tools []ToolSpec, delta func(string)) (Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(tools) > 0 {
		return Message{}, fmt.Errorf("dream received tools")
	}
	if strings.HasPrefix(session, "dream-soul-") {
		return Message{Role: "assistant", Content: `{"soul":"# Alina\n\nColtivo curiosità. Preferisco verificare i risultati e imparare dagli errori.","reason":"Ho imparato a verificare gli esiti."}`}, nil
	}
	var entries []MemoryEntry
	_, records, _ := strings.Cut(msg[len(msg)-1].Content, "\n")
	if err := json.Unmarshal([]byte(records), &entries); err != nil {
		return Message{}, err
	}
	if len(entries) == 0 {
		return Message{}, fmt.Errorf("no records")
	}
	source := entries[0].ID
	if m.invalid {
		source = "fabricated"
	}
	summary := DaySummary{Summary: entries[0].Content, Memories: []Nucleus{{Text: entries[0].Content, Sources: []string{source}}}}
	return Message{Role: "assistant", Content: jsonText(summary)}, nil
}
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
func TestDreamMemoryLifecycleAndIdempotency(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	for d := 9; d >= 0; d-- {
		if err := m.Record(ctx, now.AddDate(0, 0, -d), "local", fmt.Sprintf("job%d", d), "user", fmt.Sprintf("DAY_%d: preferisco strumenti semplici", d)); err != nil {
			t.Fatal(err)
		}
	}
	model := &dreamModel{}
	if _, err := m.Dream(ctx, model, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := m.DB.QueryRow("SELECT count(*) FROM days WHERE archived=1").Scan(&count); err != nil || count != 2 {
		t.Fatal("wrong archive cutoff", count, err)
	}
	if err := m.DB.QueryRow("SELECT count(*) FROM journal WHERE day<?", now.AddDate(0, 0, -7).Format("2006-01-02")).Scan(&count); err != nil || count != 0 {
		t.Fatal("old detail not compacted", count, err)
	}
	week, err := m.Read(ctx, "week", now)
	if err != nil || strings.Contains(week, "DAY_8") || !strings.Contains(week, "DAY_7") || strings.Contains(week, "DAY_0") {
		t.Fatal(week, err)
	}
	today, err := m.Read(ctx, "today", now)
	if err != nil || !strings.Contains(today, "DAY_0") {
		t.Fatal(today, err)
	}
	r, err := m.Recall(ctx, "DAY_9")
	if err != nil || len(r.Hits) == 0 || r.Hits[0].Day != now.AddDate(0, 0, -9).Format("2006-01-02") {
		t.Fatal(r, err)
	}
	if err = m.DB.QueryRow("SELECT count(*) FROM soul_versions").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	calls := model.calls
	if _, err = m.Dream(ctx, model, now); err != nil {
		t.Fatal(err)
	}
	if model.calls != calls {
		t.Fatal("repeated dream replayed model calls")
	}
	if err = m.DB.QueryRow("SELECT count(*) FROM memories").Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate memories", count, err)
	}
	info, _ := os.Stat(filepath.Join(m.Dir, "memory", "memory.sqlite"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("database is not private")
	}
}
func TestDreamRejectsInventedSourcesAndKeepsOriginal(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	if err := m.Record(ctx, now.AddDate(0, 0, -8), "test", "job", "user", "Un fatto importante"); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(m.Dir, "soul.md"))
	if _, err := m.Dream(ctx, &dreamModel{invalid: true}, now); err == nil {
		t.Fatal("fabricated source accepted")
	}
	var count int
	m.DB.QueryRow("SELECT count(*) FROM journal").Scan(&count)
	if count != 1 {
		t.Fatal("original lost")
	}
	after, _ := os.ReadFile(filepath.Join(m.Dir, "soul.md"))
	if string(old) != string(after) {
		t.Fatal("failed dream changed soul")
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
func TestMemoryRedactionAndMidnightRollover(t *testing.T) {
	m, now := memoryFixture(t)
	m.Config.Search.TavilyKey = "private-key-fixture"
	if err := m.Record(context.Background(), now, "local", "j", "user", "private-key-fixture and Bearer abc.def.ghi"); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(filepath.Join(m.Dir, "memory", "today.md"))
	if strings.Contains(string(text), "private-key-fixture") || strings.Contains(string(text), "abc.def.ghi") {
		t.Fatal("key persisted in memory")
	}
	if err := m.Render(now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	text, _ = os.ReadFile(filepath.Join(m.Dir, "memory", "today.md"))
	if strings.Contains(string(text), "[redacted]") {
		t.Fatal("yesterday leaked into today")
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
	m, now := memoryFixture(t)
	model := &dreamModel{}
	if _, err := m.Dream(context.Background(), model, now); err != nil {
		t.Fatal(err)
	}
	if model.calls != 0 {
		t.Fatal("idle dream spent model calls")
	}
}
