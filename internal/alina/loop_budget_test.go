package alina

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkBudgetClosesWithoutMoreTools(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, m []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		calls++
		if !strings.HasPrefix(m[len(m)-1].Content, "<execution_budget>") {
			t.Error("model cannot see remaining budget")
		}
		if len(tools) == 0 {
			if len(tools) != 0 || !strings.Contains(jsonText(m), "verified artifact") {
				t.Error("final report lost evidence or allowed more tools")
			}
			return Message{Role: "assistant", Content: "Verified artifact saved; remaining checks are unfinished."}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: fmt.Sprint(calls), Name: "write", Arguments: `{"path":"result.txt","content":"verified artifact"}`}}}, nil
	}))
	e.Config.MaxSteps = 2
	e.Config.WorkDir = t.TempDir()
	j, err := e.Submit("bounded", "local", "Work on the fixture")
	if err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, j.ID, "completed")
	if !done.Partial || calls != 3 || !strings.Contains(done.Output, "unfinished") {
		t.Fatal(done, calls)
	}
	if b, err := os.ReadFile(filepath.Join(e.Config.WorkDir, "result.txt")); err != nil || string(b) != "verified artifact" {
		t.Fatal(string(b), err)
	}
	var n int
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE content LIKE '%<execution_budget>%' ").Scan(&n); err != nil || n != 0 {
		t.Fatal("budget entered memory", n, err)
	}
}

func TestDelegateUsesModelContextBeyondOldBudgets(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, s string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls++
		if strings.HasPrefix(s, "checkpoint-") {
			t.Fatal("compacted below the native threshold")
		}
		if calls == 25 {
			return Message{Role: "assistant", Content: "Verified after 25 work requests.", Usage: &TokenUsage{InputTokens: 100000}}, nil
		}
		return Message{Role: "assistant", Usage: &TokenUsage{InputTokens: 100000}, Calls: []ToolCall{{ID: fmt.Sprint(calls), Name: "read", Arguments: `{"path":"source.txt"}`}}}, nil
	}))
	j := &runningJob{Job: Job{ID: "native-worker", Session: "native-worker", Kind: "delegate", Input: "Finish assigned research", Model: delegateModel, Reasoning: "medium"}, ctx: e.ctx, model: &catalogModel{ID: delegateModel, Context: 272000, Levels: []string{"medium"}, Default: "medium"}}
	os.MkdirAll(e.delegateWorkspace(j), 0700)
	os.WriteFile(filepath.Join(e.delegateWorkspace(j), "source.txt"), []byte("evidence"), 0600)
	path := filepath.Join(e.Dir, "delegates", j.ID, "trace.json")
	history := []Message{{Role: "user", Content: strings.Repeat("old evidence ", 18000)}, {Role: "assistant", Content: "Inspected."}}
	if estimatedTokens(history) <= 64000 {
		t.Fatal("fixture did not exceed old limit")
	}
	if err := writeJSON(path, history); err != nil {
		t.Fatal(err)
	}
	out, err := e.turn(j)
	if err != nil || calls != 25 || out != "Verified after 25 work requests." || j.Usage.InputTokens != 2500000 {
		t.Fatal(out, err, calls, j.Usage)
	}
}

func TestDelegateCompactsTraceWithoutSharedMemory(t *testing.T) {
	checkpoints := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, s string, m []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		if estimatedTokens(m)+estimatedTokens(tools) > 16384 {
			t.Error("worker exceeded its catalog context window")
		}
		if strings.HasPrefix(s, "checkpoint-") {
			checkpoints++
			return Message{Role: "assistant", Content: "Verified source /research/source. Continue the assigned task."}, nil
		}
		if !strings.Contains(jsonText(m), "/research/source") || lastInteraction(m).Content != "Current task" {
			t.Error("checkpoint or task missing")
		}
		return Message{Role: "assistant", Content: "Completed from the checkpoint."}, nil
	}))
	e.Config.ContextTokens = 32768
	// Even the globally selected model must respect the worker's catalog cap.
	j := &runningJob{Job: Job{ID: "compact-worker", Session: "compact-worker", Kind: "delegate", Input: "Current task", Model: delegateModel}, ctx: e.ctx, model: &catalogModel{ID: delegateModel, Context: 16384, Levels: []string{"medium"}, Default: "medium"}}
	path := filepath.Join(e.Dir, "delegates", j.ID, "trace.json")
	history := []Message{}
	for i := 0; i < 40; i++ {
		history = append(history, Message{Role: "user", Content: strings.Repeat("source ", 900)}, Message{Role: "assistant", Content: "read"})
	}
	if err := writeJSON(path, history); err != nil {
		t.Fatal(err)
	}
	out, err := e.turn(j)
	if err != nil || checkpoints == 0 || !strings.Contains(out, "checkpoint") {
		t.Fatal(out, err, checkpoints)
	}
	archives, _ := filepath.Glob(filepath.Join(e.Dir, "delegates", j.ID, "archives", "*.json"))
	shared, _ := filepath.Glob(filepath.Join(e.Dir, "sessions", j.Session, "*.json"))
	var n int
	e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE session=?", j.Session).Scan(&n)
	if len(archives) != 1 || len(shared) != 0 || n != 0 {
		t.Fatal("worker evidence crossed into shared archives", archives, shared, n)
	}
}

func TestWorkerDeadlineReservesReportAndHonorsStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			work, stopWork := context.WithCancel(ctx)
			defer stopWork()
			calls := 0
			e := newTestEngine(t, modelFunc(func(c context.Context, _ string, _ []Message, tools []ToolSpec, _ func(string)) (Message, error) {
				calls++
				if len(tools) == 0 {
					if c.Err() != nil {
						t.Error("report has no reserved context", c.Err(), tools)
					}
					return Message{Role: "assistant", Content: "Partial: source lookup unfinished."}, nil
				}
				if stop {
					cancel()
				} else {
					stopWork()
				}
				return Message{}, context.DeadlineExceeded
			}))
			j := &runningJob{Job: Job{ID: "deadline-worker", Session: "deadline-worker", Kind: "delegate", Input: "Research"}, ctx: ctx, workCtx: work}
			// A deadline is required by the production inference limiter.
			deadlineWork, deadlineCancel := context.WithTimeout(work, 30*time.Second)
			defer deadlineCancel()
			j.workCtx = deadlineWork
			out, err := e.turn(j)
			if stop {
				if err == nil || calls != 1 || out != "" {
					t.Fatal("stop resurrected the worker", out, err, calls)
				}
			} else if err != nil || calls != 2 || !j.Partial || !strings.Contains(out, "Partial") {
				t.Fatal(out, err, calls, j.Partial)
			}
		})
	}
}

func TestDelegateWaitDoesNotPollAndWakesForSteering(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e := delegateTestEngine(t, modelFunc(func(ctx context.Context, _ string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
		return Message{Role: "assistant", Content: "Verified worker report."}, nil
	}))
	parent := delegateParent(e)
	defer parent.cancel()
	out, err := e.delegateTool(parent, `{"action":"start","task":"Verify the fixture"}`)
	if err != nil {
		t.Fatal(err)
	}
	var child struct{ ID string }
	json.Unmarshal([]byte(out), &child)
	<-started
	waiting := make(chan string, 1)
	go func() {
		result, err := e.delegateTool(parent, jsonText(map[string]any{"action": "wait", "id": child.ID}))
		if err != nil {
			result = err.Error()
		}
		waiting <- result
	}()
	parent.steerSignal <- struct{}{}
	select {
	case result := <-waiting:
		if !strings.Contains(result, "steering") {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("wait ignored steering")
	}
	if calls.Load() != 1 {
		t.Fatal("waiting spent model calls", calls.Load())
	}
	close(release)
	result, err := e.delegateTool(parent, jsonText(map[string]any{"action": "wait", "id": child.ID}))
	if err != nil || !strings.Contains(result, "Verified worker report") {
		t.Fatal(result, err)
	}
	more, err := e.collectDelegates(parent, func(Message) error { t.Error("duplicate worker report"); return nil })
	if more || err != nil {
		t.Fatal(more, err)
	}
}

func TestClosingParentCollectsWorkerBeforeReport(t *testing.T) {
	var parentCalls atomic.Int32
	e := delegateTestEngine(t, modelFunc(func(_ context.Context, session string, m []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		if strings.HasPrefix(session, "delegate-") {
			return Message{Role: "assistant", Content: "Verified delegated evidence."}, nil
		}
		parentCalls.Add(1)
		if len(tools) == 0 {
			if !strings.Contains(jsonText(m), "Verified delegated evidence") {
				t.Error("closing parent lost worker result")
			}
			return Message{Role: "assistant", Content: "Partial result includes delegated evidence."}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: "start", Name: "delegate", Arguments: `{"action":"start","task":"Return verified evidence"}`}}}, nil
	}))
	e.Config.MaxSteps = 1
	j, err := e.Submit("closing-parent", "local", "Delegate this task")
	if err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, j.ID, "completed")
	if !done.Partial || parentCalls.Load() != 2 || !strings.Contains(done.Output, "delegated evidence") {
		t.Fatal(done, parentCalls.Load())
	}
}

func TestFinalReportCannotExecuteTools(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls++
		return Message{Role: "assistant", Calls: []ToolCall{{ID: fmt.Sprint(calls), Name: "write", Arguments: jsonText(map[string]any{"path": "result.txt", "content": fmt.Sprint(calls)})}}}, nil
	}))
	e.Config.MaxSteps = 1
	e.Config.WorkDir = t.TempDir()
	j, err := e.Submit("no-final-tools", "local", "Modify fixture")
	if err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, j.ID, "failed")
	b, err := os.ReadFile(filepath.Join(e.Config.WorkDir, "result.txt"))
	if err != nil || string(b) != "1" || calls != 2 || !strings.Contains(done.Error, "tool-free") {
		t.Fatal("closing tool executed", string(b), err, done, calls)
	}
}

func TestPartialDreamDoesNotAdvanceExperienceCursor(t *testing.T) {
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, _ []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		if len(tools) == 0 {
			return Message{Role: "assistant", Content: "Partial reflection; an unresolved question remains."}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: "inspect", Name: "harness", Arguments: `{"action":"status"}`}}}, nil
	}))
	e.Config.MaxSteps = 1
	if err := e.Memory.Record(e.ctx, time.Now(), "source", "source-job", "user", "A new experience to reflect on."); err != nil {
		t.Fatal(err)
	}
	j := &runningJob{Job: Job{ID: "partial-dream", Session: "partial-dream", Kind: "dream"}, ctx: e.ctx}
	out, err := e.dream(j, time.Now())
	if err == nil || !j.Partial || !strings.Contains(out, "Partial reflection") {
		t.Fatal(out, err, j.Partial)
	}
	var n int
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key='reflected-through'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial dream consumed experiences", n, err)
	}
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM dreams").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial dream recorded as completed", n, err)
	}
}
