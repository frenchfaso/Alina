package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type modelFunc func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error)

func (f modelFunc) Complete(c context.Context, s string, m []Message, t []ToolSpec, d func(string)) (Message, error) {
	return f(c, s, m, t, d)
}

func TestApprovalLeavesOtherSessionsAvailable(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(ctx context.Context, s string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if s == "blocked" && m[len(m)-1].Role != "tool" {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "call", Name: "shell", Arguments: `{"command":"printf pending","network":true}`}}}, nil
		}
		return Message{Role: "assistant", Content: "ready"}, nil
	}))
	first, err := e.Submit("blocked", "local", "do work")
	if err != nil {
		t.Fatal(err)
	}
	pending := awaitStatus(t, e, first.ID, "approval")
	second, err := e.Submit("other", "local", "talk")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, second.ID, "completed")
	queued, err := e.Submit("blocked", "local", "follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := e.Get(queued.ID); j.Status != "queued" {
		t.Fatal("same-session order lost", j)
	}
	if err = e.Approve(first.ID, pending.Approval.ID, "deny", "local"); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, first.ID, "completed")
	// The follow-up may request its own permission, but only after the prior turn.
	awaitStatus(t, e, queued.ID, "approval")
}
func TestModelGatePrefersForeground(t *testing.T) {
	var gate modelGate
	release, err := gate.acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	order := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r, _ := gate.acquire(context.Background(), true); order <- "background"; r() }()
	go func() { defer wg.Done(); r, _ := gate.acquire(context.Background(), false); order <- "foreground"; r() }()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		n := gate.foreground
		gate.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no waiter")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	if got := <-order; got != "foreground" {
		t.Fatal(got)
	}
	wg.Wait()
}
func TestContinuationCompactsAndKeepsTranscript(t *testing.T) {
	marker := "Keep the verified backup path /verified/backup and check unfinished restore"
	model := modelFunc(func(ctx context.Context, s string, m []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		if strings.HasPrefix(s, "checkpoint-") {
			return Message{Role: "assistant", Content: marker}, nil
		}
		text := jsonText(m)
		if !strings.Contains(text, marker) {
			return Message{}, errors.New("lost checkpoint")
		}
		return Message{Role: "assistant", Content: "continued"}, nil
	})
	e := newTestEngine(t, model)
	history := []Message{{Role: "user", Content: marker}}
	for i := 0; i < 240; i++ {
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("old %d", i)}, Message{Role: "assistant", Content: "done"})
	}
	path := filepath.Join(e.Dir, "sessions", "long.json")
	if err := writeJSON(path, history); err != nil {
		t.Fatal(err)
	}
	j, err := e.Submit("long", "local", "continue")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	b, _ := os.ReadFile(path)
	var saved []Message
	if err = json.Unmarshal(b, &saved); err != nil || len(saved) > 100 {
		t.Fatal(len(saved), err)
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "sessions", "long", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	b, _ = os.ReadFile(files[0])
	if !strings.Contains(string(b), marker) {
		t.Fatal("original transcript lost")
	}
}
func TestLargeToolExchangeCompactsWithoutOrphanCalls(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{Role: "assistant", Content: "Task is unfinished. Tools completed; next check the output."}, nil
	}))
	history := []Message{{Role: "user", Content: "inspect"}, {Role: "assistant"}}
	for n := 0; n < 8; n++ {
		id := fmt.Sprint(n)
		history[1].Calls = append(history[1].Calls, ToolCall{ID: id, Name: "shell", Arguments: `{}`})
		history = append(history, Message{Role: "tool", CallID: id, Content: strings.Repeat("x", 48000)})
	}
	j := &runningJob{Job: Job{Session: "huge"}, ctx: e.ctx}
	next, err := e.compact(j, history, filepath.Join(e.Dir, "sessions", "huge.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Role != "user" {
		t.Fatal("orphan tool exchange", len(next))
	}
}
func TestRestartResumeAndLazyJobs(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	old := Job{ID: "old", Session: "work", Owner: "local", Input: "finish original intention", Status: "running", Kind: "chat"}
	if err := writeJSON(filepath.Join(dir, "jobs", "old.json"), old); err != nil {
		t.Fatal(err)
	}
	writeText(filepath.Join(dir, "jobs", "broken.json"), "{")
	history := []Message{{Role: "user", Content: old.Input}, {Role: "assistant", Calls: []ToolCall{{ID: "unknown", Name: "shell", Arguments: `{"command":"touch side-effect"}`}}}}
	writeJSON(filepath.Join(dir, "sessions", "work.json"), history)
	model := modelFunc(func(ctx context.Context, s string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if !strings.Contains(jsonText(m), "do not assume execution succeeded") || !strings.Contains(jsonText(m), old.Input) {
			return Message{}, errors.New("resume lost uncertainty or intention")
		}
		return Message{Role: "assistant", Content: "checked"}, nil
	})
	e, err := NewEngine(dir, c, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if len(e.jobs) != 0 {
		t.Fatal("historical jobs held in RAM")
	}
	j, _ := e.Get("old")
	if j.Status != "interrupted" {
		t.Fatal(j)
	}
	if _, err = e.Resume("old", "telegram:1"); err == nil {
		t.Fatal("owner check missing")
	}
	resumed, err := e.Resume("old", "local")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, resumed.ID, "completed")
	if _, err = os.Stat(filepath.Join(dir, "side-effect")); !os.IsNotExist(err) {
		t.Fatal("replayed unrecorded tool")
	}
}
func TestRecentMemoryPagesCorrectionsAndSoulFallback(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	m := e.Memory
	ctx := context.Background()
	now := time.Now()
	if err := m.Record(ctx, now, "old", "j", "user", "UNIQUE_EARLY preference"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := m.Record(ctx, now, "old", "j", "tool:shell", strings.Repeat("è日本語", 2200)); err != nil {
			t.Fatal(err)
		}
	}
	found, err := m.Recall(ctx, "UNIQUE_EARLY")
	if err != nil || len(found.Hits) == 0 {
		t.Fatal(found, err)
	}
	source, err := m.Read(ctx, found.Hits[0].ID, now)
	if err != nil || !strings.Contains(source, "UNIQUE_EARLY") {
		t.Fatal(source, err)
	}
	var text strings.Builder
	offset := 0
	for n := 0; n < 100; n++ {
		p, err := m.ReadPage(ctx, "today", offset, now)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(p.Text) {
			t.Fatal("split Unicode")
		}
		text.WriteString(p.Text)
		if p.Next == 0 {
			break
		}
		if p.Next <= offset {
			t.Fatal("cursor did not advance")
		}
		offset = p.Next
	}
	entries, _ := m.entries(ctx, now.In(m.loc).Format("2006-01-02"))
	var expected strings.Builder
	fmt.Fprintf(&expected, "# %s\n", now.In(m.loc).Format("2006-01-02"))
	for _, entry := range entries {
		expected.WriteString(entryText(entry))
	}
	if text.String() != expected.String() {
		t.Fatal("paging lost or duplicated content")
	}
	correction, err := m.Note(ctx, now, "local", "correct", "preference", "UNIQUE_EARLY now means the revised preference", found.Hits[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found, err = m.Recall(ctx, "UNIQUE_EARLY")
	if err != nil || len(found.Hits) != 1 || found.Hits[0].ID != correction {
		t.Fatal(found, err)
	}
	good := "# Alina\nCuriosa e concreta."
	writeText(filepath.Join(e.Dir, "soul.md"), good)
	if _, err = e.prompt(ctx); err != nil {
		t.Fatal(err)
	}
	writeText(filepath.Join(e.Dir, "soul.md"), strings.Repeat("x", 1601))
	prompt, err := e.prompt(ctx)
	if err != nil || !strings.Contains(prompt, good) || !strings.Contains(prompt, "last valid") {
		t.Fatal(prompt, err)
	}
}
func TestOneShotAndPersonalBudgetSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	c.Autonomy.Enabled = true
	c.Autonomy.MaxCalls = 1
	model := modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{Role: "assistant", Content: "experiment finished"}, nil
	})
	e, err := NewEngine(dir, c, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err := e.Memory.Intend(e.ctx, Intention{Title: "Learn", Why: "test", Next: "inspect a local tool", Stop: "when documented"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * time.Minute)
	task, err := e.Scheduler.AddOnce("Explore", at.Format(time.RFC3339), "inspect", "local", "self", i.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Scheduler.mu.Lock()
	dream := e.Scheduler.tasks["dream"]
	dream.Enabled = false
	e.Scheduler.tasks["dream"] = dream
	e.Scheduler.mu.Unlock()
	if err = e.Scheduler.Tick(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	tasks := e.Scheduler.List()
	var job string
	for _, v := range tasks {
		if v.ID == task.ID {
			job = v.LastJob
			if v.Enabled {
				t.Fatal("one-shot still enabled")
			}
		}
	}
	awaitStatus(t, e, job, "completed")
	if err = e.Scheduler.Tick(at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(e.Jobs("alina")) != 1 {
		t.Fatal("wake-up replayed")
	}
	e.Close()
	e, err = NewEngine(dir, c, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	intents, err := e.Memory.Intentions(e.ctx, true)
	if err != nil || len(intents) != 1 {
		t.Fatal(intents, err)
	}
	j, err := e.submit("budget", "alina", "another experiment", "", "initiative")
	if err != nil {
		t.Fatal(err)
	}
	failed := awaitStatus(t, e, j.ID, "failed")
	if !strings.Contains(failed.Error, "daily personal exploration budget") {
		t.Fatal(failed)
	}
}
func TestDeclaredNetworkAndInstallClassification(t *testing.T) {
	if a := declaredAction("curl https://example.test/info", "/tmp", true, false, false); a.Reason != "" {
		t.Fatal(a)
	}
	for _, cmd := range []string{"curl -o file https://example.test", "wget https://example.test/file", "pip install example", "pkg install example"} {
		if a := declaredAction(cmd, "/tmp", true, false, false); a.Reason == "" {
			t.Fatal("missing approval", cmd)
		}
	}
	if a := declaredAction("npm run build", "/tmp", false, false, false); a.Reason != "" {
		t.Fatal("local script requires installation consent", a)
	}
	if a := declaredAction("python custom.py", "/tmp", true, true, false); a.Reason == "" {
		t.Fatal("declared arbitrary download accepted")
	}
}

func TestDreamRetrievesEvidenceBeforeRevisingSoul(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	now := time.Now()
	if err := e.Memory.Record(e.ctx, now.AddDate(0, 0, -9), "experience", "j", "user", "unique_old: the verification failed, revise the procedure"); err != nil {
		t.Fatal(err)
	}
	consolidator := &dreamModel{}
	looked := false
	model := modelFunc(func(ctx context.Context, s string, msg []Message, tools []ToolSpec, d func(string)) (Message, error) {
		if !strings.HasPrefix(s, "dream-soul-") {
			return consolidator.Complete(ctx, s, msg, tools, d)
		}
		if len(tools) == 0 {
			return Message{}, errors.New("reflection has no memory access")
		}
		if msg[len(msg)-1].Role != "tool" {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "lookup", Name: "memory", Arguments: `{"action":"search","query":"unique_old"}`}}}, nil
		}
		if !strings.Contains(msg[len(msg)-1].Content, "verification failed") {
			return Message{}, errors.New("older evidence was not retrieved")
		}
		looked = true
		return Message{Role: "assistant", Content: `{"soul":"# Alina\nPreferisco verificare le mie interpretazioni.","reason":"Una procedura tentata non era riuscita."}`}, nil
	})
	j := &runningJob{Job: Job{Kind: "dream", Owner: "system", Session: "dream", ID: "reflection"}, ctx: e.ctx}
	if _, err := e.Memory.Dream(e.ctx, model, now, func(call ToolCall) (string, error) { return e.reflectionTool(j, call) }); err != nil {
		t.Fatal(err)
	}
	if !looked {
		t.Fatal("no evidence check")
	}
	var id string
	if err := e.Memory.DB.QueryRow("SELECT id FROM evidence LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	text, err := e.Memory.Read(e.ctx, id, now)
	if err != nil || !strings.Contains(text, "verification failed") {
		t.Fatal("cited evidence was lost", text, err)
	}
}

func TestInvalidCheckpointRetainsOriginalSession(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{}, errors.New("provider unavailable")
	}))
	history := []Message{}
	for i := 0; i < 101; i++ {
		history = append(history, Message{Role: "user", Content: fmt.Sprint(i)})
	}
	path := filepath.Join(e.Dir, "sessions", "unchanged.json")
	writeJSON(path, history)
	before, _ := os.ReadFile(path)
	j := &runningJob{Job: Job{Session: "unchanged"}, ctx: e.ctx}
	if _, err := e.compact(j, history, path); err == nil {
		t.Fatal("expected summary failure")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed checkpoint changed original")
	}
}

func TestExplicitInstallRequiresConsentInStrictMode(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(ctx context.Context, s string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if m[len(m)-1].Role == "tool" {
			return Message{Role: "assistant", Content: "stopped"}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: "install", Name: "shell", Arguments: `{"command":"printf local-installer","install":true}`}}}, nil
	}))
	j, err := e.Submit("install", "local", "install")
	if err != nil {
		t.Fatal(err)
	}
	p := awaitStatus(t, e, j.ID, "approval")
	if !strings.Contains(p.Approval.Action.Reason, "installation") {
		t.Fatal(p)
	}
	if err = e.Approve(j.ID, p.Approval.ID, "deny", "local"); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
}
