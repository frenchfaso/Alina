package alina

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func singleFamilyEngine(t *testing.T, model Model) *Engine {
	t.Helper()
	c := familyConfig(t.TempDir())
	c.Users = c.Users[:2]
	e, err := NewEngine(t.TempDir(), c, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestDreamOverviewBoundedAndEvidenceRetained(t *testing.T) {
	m, now := memoryFixture(t)
	for i := 0; i < 30; i++ {
		text := strings.Repeat("Experience ", 100)
		if i == 0 {
			text = "Check camera.\nTool request: shell " + strings.Repeat("RAW_ARGUMENT ", 10000)
		}
		if err := m.Record(context.Background(), now, "chat", "event-"+strconv.Itoa(i), "assistant", text); err != nil {
			t.Fatal(err)
		}
		if err := m.Record(context.Background(), now, "chat", "event-"+strconv.Itoa(i), "tool:result", strings.Repeat("RAW_OUTPUT ", 10000)); err != nil {
			t.Fatal(err)
		}
	}
	view, through, err := m.dreamOverview(context.Background(), 0, 4000)
	if err != nil || len(view) > 4100 || !strings.Contains(view, "Tool: shell") || strings.Contains(view, "RAW_") || !strings.Contains(view, "More experiences remain") {
		t.Fatal(len(view), err, view)
	}
	next, end, err := m.dreamOverview(context.Background(), through, 4000)
	if err != nil || end <= through || strings.Contains(next, "Check camera") {
		t.Fatal(end, through, err)
	}
	var count int
	if err = m.DB.QueryRow("SELECT count(*) FROM journal WHERE content LIKE '%RAW_OUTPUT%'").Scan(&count); err != nil || count != 30 {
		t.Fatal(count, err)
	}
}

func TestDreamUsesNormalBudgetAndSharesHistory(t *testing.T) {
	calls := 0
	e := singleFamilyEngine(t, modelFunc(func(ctx context.Context, _ string, msg []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls++
		if ctx.Value(reasoningEffortKey{}) != "low" {
			return Message{}, errors.New("missing dream effort")
		}
		if calls < 8 {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: strconv.Itoa(calls), Name: "memory", Arguments: `{"action":"intentions"}`}}}, nil
		}
		return Message{Role: "assistant", Content: "Dream: a quieter approach to moonflowers."}, nil
	}))
	family := e.localEngine()
	if err := family.Memory.Record(e.ctx, time.Now(), "chat", "event", "user", "Consider moonflowers."); err != nil {
		t.Fatal(err)
	}
	j, err := e.submit("dream-test", "system", "reflect", "", "dream")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	if calls != 8 {
		t.Fatal(calls)
	}
	page, err := family.readMemory(e.ctx, "dreams", 0, time.Now())
	if err != nil || !strings.Contains(page.Text, "moonflowers") || !strings.Contains(page.Text, j.ID) {
		t.Fatal(page, err)
	}
	page, err = family.readMemory(e.ctx, j.ID, 0, time.Now())
	if err != nil || !strings.Contains(page.Text, "reflection:tool:result") {
		t.Fatal(page, err)
	}
	results, err := family.recall(e.ctx, "moonflowers")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range results.Hits {
		if hit.Kind == "reflection:assistant" {
			found = true
			if _, err := family.readMemory(e.ctx, hit.ID, 0, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatal("dream not searchable", results)
	}
	status := e.dreamStatus()
	if status["last_completed"] == nil || status["next"] == nil || status["last_attempt"].(map[string]any)["outcome"] != "completed" {
		t.Fatal(status)
	}
	second, err := e.submit("dream-empty", "system", "reflect", "", "dream")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, second.ID, "completed")
	if calls != 8 || e.dreamStatus()["last_attempt"].(map[string]any)["outcome"] != "skipped" {
		t.Fatal("empty dream used model", calls)
	}
}

func TestDreamFailureAndConcurrentExperienceKeepCursors(t *testing.T) {
	e := singleFamilyEngine(t, nil)
	family := e.localEngine()
	if err := family.Memory.Record(e.ctx, time.Now(), "chat", "first", "user", "First experience"); err != nil {
		t.Fatal(err)
	}
	e.Model = modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{}, errors.New("fixture failure")
	})
	j, err := e.submit("dream-failed", "system", "reflect", "", "dream")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "failed")
	_, _, changed, err := e.dreamScopes(e.ctx)
	if err != nil || !changed {
		t.Fatal("failed dream advanced cursor", err)
	}
	e.Model = modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		if err := family.Memory.Record(e.ctx, time.Now(), "chat", "later", "user", "Concurrent experience"); err != nil {
			return Message{}, err
		}
		return Message{Role: "assistant", Content: "I considered the first experience."}, nil
	})
	j, err = e.submit("dream-completed", "system", "reflect", "", "dream")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	view, _, changed, err := e.dreamScopes(e.ctx)
	if err != nil || !changed || !strings.Contains(view, "Concurrent experience") || strings.Contains(view, "First experience") {
		t.Fatal(view, err)
	}
}

func TestPersonalArchiveUpgradePreservesData(t *testing.T) {
	c := familyConfig(t.TempDir())
	c.Users = c.Users[:1]
	c.Users[0].Family = ""
	dir := t.TempDir()
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.localEngine().Memory.Note(e.ctx, time.Now(), "old-chat", "old-job", "fact", "Original keepsake", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	c.Users[0].Family = "alex"
	c.Users = append(c.Users, User{ID: "bea", Name: "Bea", Family: "alex", TelegramID: 84})
	e, err = NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	if e.localEngine().Dir != dir || len(e.scopes) != 1 {
		t.Fatal("archive moved")
	}
	page, err := e.localEngine().readMemory(e.ctx, id, 0, time.Now())
	if err != nil || !strings.Contains(page.Text, "Original keepsake") {
		t.Fatal(page, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "people-state.json"))
	var state map[string]string
	if err != nil || json.Unmarshal(b, &state) != nil || state["legacy_scope"] != "family-alex" {
		t.Fatal(state, err)
	}
}
