package alina

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
)

func TestAttentionFadesAndRecallsWithoutFeedback(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	old, err := m.Note(ctx, now.AddDate(0, 0, -90), "telegram", "a", "lesson", "OLD_ORCHID useful procedure "+strings.Repeat("detail ", 110), "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err = m.Note(ctx, now.Add(time.Duration(i)*time.Second), "local", "b", "fact", fmt.Sprintf("NEW_%d %s", i, strings.Repeat("detail ", 110)), ""); err != nil {
			t.Fatal(err)
		}
	}
	text, err := m.FocusContext(ctx, now)
	if err != nil || strings.Contains(text, "OLD_ORCHID") || len(text) > focusBytes {
		t.Fatal(text, err)
	}
	for i := 0; i < 3; i++ {
		if _, err = m.Recall(ctx, "OLD_ORCHID"); err != nil {
			t.Fatal(err)
		}
		if _, err = m.FocusContext(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	m.DB.QueryRow("SELECT count(*) FROM attention").Scan(&count)
	if count != 0 {
		t.Fatal("automatic selection reinforced memory", count)
	}
	if _, err = m.Read(ctx, old, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	text, err = m.FocusContext(ctx, now.Add(time.Minute))
	if err != nil || !strings.Contains(text, "OLD_ORCHID") {
		t.Fatal(text, err)
	}
	id, err := m.Note(ctx, now.Add(time.Hour), "local", "c", "lesson", "OLD_ORCHID useful procedure "+strings.Repeat("detail ", 110), "")
	if err != nil || id != old {
		t.Fatal("recurrence created duplicate", id, err)
	}
	if w := attentionWeight(now.Add(-30*24*time.Hour).Format(time.RFC3339), now); math.Abs(w-0.5) > 0.0001 {
		t.Fatal(w)
	}
	pin := true
	if err = m.Focus(ctx, old, &pin, now); err != nil {
		t.Fatal(err)
	}
	text, err = m.FocusContext(ctx, now.AddDate(5, 0, 0))
	if err != nil || !strings.Contains(text, "OLD_ORCHID") {
		t.Fatal(text, err)
	}
	corrected, err := m.Note(ctx, now, "local", "correction", "lesson", "REVISED_ORCHID procedure", old)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Focus(ctx, old, nil, now); err == nil {
		t.Fatal("obsolete note revived")
	}
	text, err = m.Read(ctx, old, now)
	if err != nil || !strings.Contains(text, "SUPERSEDED by "+corrected) {
		t.Fatal(text, err)
	}
}

func TestSharedChannelsHaveContinuityWithoutEmbeddingCalls(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(_ context.Context, s string, msg []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if s == "telegram" && !strings.Contains(jsonText(msg), "CROSS_CHANNEL_ORCHID") {
			t.Fatal("channel change lost shared context")
		}
		return Message{Role: "assistant", Content: "Recorded."}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("ordinary turn made an embedding request")
		w.WriteHeader(500)
	}))
	defer server.Close()
	e.Memory.Config.Memory.EmbeddingURL = server.URL
	j, err := e.Submit("local", "local", "CROSS_CHANNEL_ORCHID the restore path is /tmp/orchid")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	j, err = e.Submit("telegram", "telegram:1", "Continue the restore discussion")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	e.Memory.Config.Memory.EmbeddingURL = ""
	r, err := e.Memory.Recall(e.ctx, "CROSS_CHANNEL_ORCHID")
	if err != nil || len(r.Hits) == 0 || r.Hits[0].Session != "local" {
		t.Fatal(r, err)
	}
}

func TestLegacyArchiveMigrationIsSearchableAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "memory"), 0700)
	db, err := driver.Open(filepath.Join(dir, "memory", "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE facts(id TEXT PRIMARY KEY,day TEXT,stamp TEXT,kind TEXT,text TEXT,supersedes TEXT NOT NULL DEFAULT '',session TEXT,job TEXT);
 INSERT INTO facts VALUES('old-note','2020-01-01','2020-01-01T00:00:00Z','fact','LEGACY_CEDAR note','','local','job');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	path := filepath.Join(dir, "sessions", "telegram", "old.json")
	if err = writeJSON(path, []Message{{Role: "user", Content: "LEGACY_CEDAR full event"}, {Role: "assistant", Content: "ack"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c := DefaultConfig()
	c.WorkDir = dir
	m, err := OpenMemory(dir, c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Recall(context.Background(), "LEGACY_CEDAR")
	if err != nil || len(r.Hits) != 2 {
		t.Fatal(r, err)
	}
	for _, hit := range r.Hits {
		if hit.Kind == "user" && (hit.Time != "" || hit.Session != "telegram") {
			t.Fatal("invented event provenance", hit)
		}
	}
	var count int
	m.DB.QueryRow("SELECT count(*) FROM journal").Scan(&count)
	m.DB.Close()
	m, err = OpenMemory(dir, c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DB.Close()
	var afterCount int
	m.DB.QueryRow("SELECT count(*) FROM journal").Scan(&afterCount)
	if afterCount != count {
		t.Fatal("duplicate migration", count, afterCount)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("migration changed original file")
	}
}

func TestShortManyTurnsDoNotCompactAndOverheadCounts(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls++
		return Message{Role: "assistant", Content: "Earlier work is recorded in the shared archive."}, nil
	}))
	j := &runningJob{Job: Job{Session: "pressure"}, ctx: e.ctx}
	history := []Message{}
	for i := 0; i < 110; i++ {
		history = append(history, Message{Role: "user", Content: "hi"})
	}
	path := filepath.Join(e.Dir, "sessions", "pressure.json")
	if _, err := e.compact(j, history, path); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("message count caused compaction")
	}
	history = []Message{{Role: "user", Content: strings.Repeat("large context ", 1400)}}
	if _, err := e.compact(j, history, path, e.Config.ContextTokens*95/100-5000); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("prompt/tool overhead ignored")
	}
}

func TestReflectionPreservesConcurrentNewEventsAndDeniesShell(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	now := time.Now()
	e.Memory.Record(e.ctx, now, "local", "before", "user", "Before reflection")
	calls := 0
	e.Model = modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls++
		if calls == 1 {
			if err := e.Memory.Record(e.ctx, now, "telegram", "during", "user", "During reflection"); err != nil {
				t.Fatal(err)
			}
		}
		return Message{Role: "assistant", Content: "No change."}, nil
	})
	j := &runningJob{Job: Job{Session: "reflection", ID: "dream", Kind: "dream", Owner: "system"}, ctx: e.ctx}
	if _, err := e.dream(j, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dream(j, now); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("new concurrent event consumed by old reflection", calls)
	}
	if _, err := e.reflectionTool(j, ToolCall{Name: "shell", Arguments: `{"command":"touch forbidden"}`}); err == nil {
		t.Fatal("reflection executed shell")
	}
	old, _ := e.Memory.Soul()
	writeText(filepath.Join(e.Dir, "soul.md"), "# Alina\nUser edit.")
	if err := e.Memory.reviseSoul(e.ctx, now, old, "# Alina\nChanged.", "test"); err == nil {
		t.Fatal("overwrote concurrent soul edit")
	}
}

func TestEveryPinnedNoteFitsFocus(t *testing.T) {
	m, now := memoryFixture(t)
	ctx := context.Background()
	pin := true
	ids := []string{}
	for i := 0; i < 30; i++ {
		id, err := m.Note(ctx, now, "local", "pins", "fact", fmt.Sprintf("pin-%d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Focus(ctx, id, &pin, now); err != nil {
			break
		}
		ids = append(ids, id)
	}
	text, err := m.FocusContext(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 17 {
		t.Fatal("fixture did not exercise more than sixteen pins", len(ids))
	}
	for _, id := range ids {
		if !strings.Contains(text, id) {
			t.Fatal("pinned note omitted", id)
		}
	}
	if len(text) > focusBytes {
		t.Fatal("focus budget exceeded")
	}
}
