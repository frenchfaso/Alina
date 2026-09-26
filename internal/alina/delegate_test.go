package alina

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func delegateTestEngine(t *testing.T, model Model) *Engine {
	root, _, _, _, _ := controlEngine(t)
	if _, err := root.models(root.ctx, true); err != nil {
		t.Fatal(err)
	}
	root.catalog.mu.Lock()
	root.catalog.cached.Models = append(root.catalog.cached.Models, catalogModel{ID: delegateModel, Levels: []string{"low", "high"}, Default: "low", Context: 272000})
	root.catalog.mu.Unlock()
	e := newTestEngine(t, model)
	e.global = root
	return e
}
func delegateParent(e *Engine) *runningJob {
	ctx, cancel := context.WithCancel(e.ctx)
	return &runningJob{Job: Job{ID: "parent", Owner: "local", Kind: "chat"}, ctx: ctx, cancel: cancel, steerSignal: make(chan struct{}, 1)}
}
func TestDelegateReportIsolationAndDelivery(t *testing.T) {
	e := delegateTestEngine(t, modelFunc(func(ctx context.Context, s string, m []Message, tools []ToolSpec, _ func(string)) (Message, error) {
		if choice, ok := ctx.Value(selectedModelKey{}).(catalogModel); !ok || choice.ID != delegateModel {
			t.Error("worker model not selected")
		}
		if ctx.Value(reasoningEffortKey{}) != "high" {
			t.Error("worker effort lost")
		}
		if hasTool(tools, "calendar") || hasTool(tools, "delegate") || hasTool(tools, "harness") || hasTool(tools, "memory") || hasTool(tools, "schedule") || hasTool(tools, "send_file") {
			t.Error("private authority exposed")
		}
		if strings.Contains(jsonText(m), "PRIVATE_SOUL") {
			t.Error("personal context leaked")
		}
		return Message{Role: "assistant", Content: "Verified the requested fixture."}, nil
	}))
	parent := delegateParent(e)
	defer parent.cancel()
	file := filepath.Join(e.Workspace(), "input.txt")
	os.MkdirAll(e.Workspace(), 0700)
	os.WriteFile(file, []byte("public input"), 0600)
	out, err := e.delegateTool(parent, jsonText(map[string]any{"action": "start", "task": "Inspect input.txt", "reasoning": "high", "files": []string{file}}))
	if err != nil {
		t.Fatal(err)
	}
	var start struct{ ID, Workspace string }
	json.Unmarshal([]byte(out), &start)
	var returned []Message
	more, err := e.collectDelegates(parent, func(m Message) error { returned = append(returned, m); return nil })
	if err != nil || !more || len(returned) != 1 || !strings.Contains(returned[0].Content, "Verified the requested fixture") {
		t.Fatal(more, err, returned)
	}
	if e.global.delegateBusy.Load() {
		t.Fatal("slot not released before delivery")
	}
	more, err = e.collectDelegates(parent, func(Message) error { t.Fatal("duplicate report"); return nil })
	if err != nil || more {
		t.Fatal(err)
	}
	var n int
	if err = e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE session=?", start.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("worker trace polluted journal", n, err)
	}
	b, err := os.ReadFile(filepath.Join(e.Dir, "delegates", start.ID, "trace.json"))
	if err != nil || !strings.Contains(string(b), "Verified") {
		t.Fatal("missing trace", err)
	}
	if b, err = os.ReadFile(filepath.Join(start.Workspace, "input.txt")); err != nil || string(b) != "public input" {
		t.Fatal("missing input copy", err)
	}
	parent.Owner = "someone-else"
	if _, err = e.delegateTool(parent, jsonText(map[string]any{"action": "trace", "id": start.ID})); err == nil {
		t.Fatal("another owner accessed trace")
	}
}
func TestDelegateCancellationAndBounds(t *testing.T) {
	entered := make(chan struct{}, 1)
	e := delegateTestEngine(t, modelFunc(func(ctx context.Context, session string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if session == "foreground" {
			return Message{Role: "assistant", Content: "ready"}, nil
		}
		entered <- struct{}{}
		<-ctx.Done()
		return Message{}, ctx.Err()
	}))
	parent := delegateParent(e)
	defer parent.cancel()
	if _, err := e.delegateTool(parent, `{"action":"start","task":"wait","reasoning":"ultra"}`); err == nil {
		t.Fatal("unsupported effort accepted")
	}
	if _, err := e.delegateTool(parent, `{"action":"start","task":"wait","reasoning":"low"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	if _, err := e.delegateTool(parent, `{"action":"start","task":"second","reasoning":"low"}`); err == nil {
		t.Fatal("unbounded workers")
	}
	foregroundCtx, stopForeground := context.WithTimeout(e.ctx, time.Second)
	defer stopForeground()
	foreground := &runningJob{Job: Job{ID: "foreground", Session: "foreground", Kind: "chat"}, ctx: foregroundCtx}
	if _, err := (jobModel{e: e, j: foreground}).Complete(foregroundCtx, "foreground", nil, nil, nil); err != nil {
		t.Fatal("worker blocked foreground inference", err)
	}
	parent.cancel()
	for _, child := range parent.delegates {
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Fatal("parent cancellation did not stop worker")
		}
		result, _ := e.Get(child.ID)
		if result.Status != "cancelled" {
			t.Fatal(result)
		}
	}
	if e.global.delegateBusy.Load() {
		t.Fatal("worker slot leaked")
	}
}
func TestDelegateFileAndToolBoundaries(t *testing.T) {
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "worker", Kind: "delegate"}, ctx: e.ctx}
	root := e.delegateWorkspace(j)
	os.MkdirAll(root, 0700)
	outside := filepath.Join(e.Dir, "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	for _, name := range []string{"calendar", "delegate", "harness", "memory", "schedule", "send_file"} {
		if _, err := e.tool(j, ToolCall{Name: name, Arguments: `{}`}); err == nil {
			t.Fatal("forbidden tool", name)
		}
	}
	for _, path := range []string{outside, "escape", "../elsewhere"} {
		if _, err := e.tool(j, ToolCall{Name: "write", Arguments: jsonText(map[string]any{"path": path, "content": "oops"})}); err == nil {
			t.Fatal("escaped file boundary", path)
		}
	}
	if _, err := e.tool(j, ToolCall{Name: "write", Arguments: `{"path":"nested/result.md","content":"report"}`}); err != nil {
		t.Fatal(err)
	}
	if out, err := e.tool(j, ToolCall{Name: "read", Arguments: `{"path":"nested/result.md"}`}); err != nil || !strings.Contains(out, "report") {
		t.Fatal(out, err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("private file modified")
	}
}
func TestDelegateShellIsolation(t *testing.T) {
	if !delegateSandboxAvailable() {
		t.Skip("OS sandbox unavailable")
	}
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "worker", Kind: "delegate"}, ctx: e.ctx}
	root := e.delegateWorkspace(j)
	os.MkdirAll(root, 0700)
	outside := filepath.Join(e.Dir, "private")
	os.WriteFile(outside, []byte("OUTSIDE_SECRET"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("NETWORK_ESCAPE")) }))
	defer server.Close()
	command := "printf local > result; cat result; cat escape; printf bad > " + ("'" + strings.ReplaceAll(outside, "'", "'\"'\"'") + "'") + "; /usr/bin/curl --max-time 1 " + server.URL + "; true"
	out, err := e.tool(j, ToolCall{Name: "shell", Arguments: jsonText(map[string]any{"command": command})})
	if err != nil || hits.Load() != 0 || !strings.Contains(out, "local") || strings.Contains(out, "OUTSIDE_SECRET") {
		t.Fatal(out, err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "OUTSIDE_SECRET" {
		t.Fatal("shell escaped workspace")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "result")); string(b) != "local" {
		t.Fatal("local shell write failed", out)
	}
}

func TestDelegateThroughParentLoopKeepsOnlyReport(t *testing.T) {
	e := delegateTestEngine(t, modelFunc(func(_ context.Context, session string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if strings.HasPrefix(session, "delegate-") {
			if m[len(m)-1].Role == "tool" {
				return Message{Role: "assistant", Content: "Worker verified its artifact."}, nil
			}
			return Message{Role: "assistant", Content: "WORKER_PRIVATE_INTERMEDIATE", Calls: []ToolCall{{ID: "write-report", Name: "write", Arguments: `{"path":"result.md","content":"artifact"}`}}}, nil
		}
		text := jsonText(m)
		if strings.Contains(text, "WORKER_PRIVATE_INTERMEDIATE") || strings.Contains(text, "Tentative answer before report.") {
			t.Error("worker exchange entered main context")
		}
		if strings.Contains(text, "Delegated work result") {
			return Message{Role: "assistant", Content: "Report received and reviewed."}, nil
		}
		if strings.Contains(text, `\"status\":\"queued\"`) || m[len(m)-1].Role == "tool" {
			return Message{Role: "assistant", Content: "Tentative answer before report."}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: "start-worker", Name: "delegate", Arguments: `{"action":"start","task":"Create a tiny artifact and report the result.","reasoning":"low"}`}}}, nil
	}))
	j, err := e.Submit("integration", "local", "Delegate this bounded fixture")
	if err != nil {
		t.Fatal(err)
	}
	result := awaitStatus(t, e, j.ID, "completed")
	if result.Output != "Report received and reviewed." {
		t.Fatal("parent finished before report", result)
	}
	var leaked int
	if err = e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE content LIKE '%WORKER_PRIVATE_INTERMEDIATE%' OR content LIKE '%Tentative answer before report.%'").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("worker trace leaked into memory", leaked, err)
	}
}
