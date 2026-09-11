package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHarnessConfigurationAndPrivacy(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	c := e.Config
	c.OpenCodeKey = "SECRET"
	c.Telegram.Token = "BOT_SECRET"
	c.Users = []User{{ID: "elsewhere", Name: "PRIVATE_NAME", TelegramID: 123}}
	text := jsonText(harnessConfig(c))
	for _, secret := range []string{"SECRET", "BOT_SECRET", "PRIVATE_NAME", "elsewhere", "owner_id"} {
		if strings.Contains(text, secret) {
			t.Fatal("config disclosed private state", text)
		}
	}
	if err := SaveConfig(e.Dir, e.Config); err != nil {
		t.Fatal(err)
	}
	j := &runningJob{Job: Job{ID: "inspect", Session: "local", Owner: "local", Kind: "chat", Created: time.Now()}, ctx: e.ctx}
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(e.Dir, "config.json"))
	out, err := e.harnessTool(j, `{"action":"configure","patch":{"reasoning_effort":"high","memory":{"dream":false}},"reason":"The user asked to change reasoning and disable dream."}`)
	if err != nil || !strings.Contains(out, "active_unchanged") {
		t.Fatal(out, err)
	}
	after, _ := os.ReadFile(filepath.Join(e.Dir, "config.json"))
	if !bytes.Equal(before, after) || e.Config.ReasoningEffort != "low" {
		t.Fatal("staging changed active/saved config")
	}
	tx, err := readRestart(e.Dir)
	if err != nil || tx.Next.ReasoningEffort != "high" || tx.Next.Memory.Dream {
		t.Fatal(tx, err)
	}
	for _, patch := range []string{`{"users":[]}`, `{"telegram":{"enabled":false}}`, `{"memory":null}`, `{"context_tokens":1}`, `{"search":{"tavily_key":"x"}}`, `{"bogus":1}`} {
		_, err = e.harnessTool(j, `{"action":"configure","reason":"user request","patch":`+patch+`}`)
		if err == nil {
			t.Fatal("accepted", patch)
		}
	}
	for _, kind := range []string{"dream", "initiative", "task"} {
		j.Kind = kind
		if _, err = e.harnessTool(j, `{"action":"configure","reason":"test","patch":{"verbosity":"high"}}`); err == nil {
			t.Fatal("autonomous mutation", kind)
		}
	}
	j.Kind = "chat"
	tx.State = "done"
	if err := writeJSON(restartPath(e.Dir), tx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.harnessTool(j, `{"action":"configure","reason":"user request","patch":{"verbosity":"high"}}`); err == nil {
		t.Fatal("overwrote unacknowledged restart outcome")
	}
	tx.State = "staged"
	if err := writeJSON(restartPath(e.Dir), tx); err != nil {
		t.Fatal(err)
	}
	j.ServiceNotice = "Restart complete"
	if _, err = e.harnessTool(j, `{"action":"restart","reason":"retry"}`); err == nil {
		t.Fatal("restart loop allowed")
	}
	j.ServiceNotice = ""
	if _, err = e.harnessTool(j, `{"action":"restart","reason":"user request"}`); err == nil {
		t.Fatal("foreground restart accepted")
	}
	if _, err = e.harnessTool(j, `{"action":"diagnose","job_id":"other-family"}`); err == nil {
		t.Fatal("unknown job logs exposed")
	}
	if out, err = e.harnessTool(j, `{"action":"diagnose"}`); err != nil || !strings.Contains(out, `"memory_integrity":"ok"`) {
		t.Fatal(out, err)
	}
}

func TestHarnessManualRefreshPreservesProcedures(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	path := filepath.Join(e.Workspace(), "procedures")
	if err := writeText(filepath.Join(path, "index.md"), "My procedure\n"); err != nil {
		t.Fatal(err)
	}
	_ = writeText(filepath.Join(path, "markitdown.md"), "personal converter notes")
	_ = writeText(filepath.Join(path, "harness.md"), "old version")
	for i := 0; i < 2; i++ {
		if err := e.initWorkspace(); err != nil {
			t.Fatal(err)
		}
	}
	index, _ := os.ReadFile(filepath.Join(path, "index.md"))
	guide, _ := os.ReadFile(filepath.Join(path, "harness.md"))
	converter, _ := os.ReadFile(filepath.Join(path, "markitdown.md"))
	if !strings.Contains(string(index), "My procedure") || strings.Count(string(index), "(harness.md)") != 1 || !strings.Contains(string(guide), Version) || string(converter) != "personal converter notes" {
		t.Fatal("manual upgrade damaged personal procedures")
	}
}

func TestRestartWaitsForDeliveryAndIdleWork(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{})
	tx := restartTransaction{ID: "restart", JobID: "job", AfterID: "after", Owner: "telegram:123", State: "waiting"}
	j := &runningJob{Job: Job{ID: tx.JobID, Owner: tx.Owner, Session: "telegram", Status: "completed", Continuation: tx.AfterID, Created: time.Now()}}
	_ = e.persist(j)
	_ = writeJSON(restartPath(e.Dir), tx)
	request := func() bool {
		t.Helper()
		w := httptest.NewRecorder()
		restartHandler(e).ServeHTTP(w, httptest.NewRequest("POST", "/v1/harness/restart/prepare", strings.NewReader(`{"id":"restart"}`)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct{ Ready bool }
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result.Ready
	}
	if request() || e.restarting.Load() {
		t.Fatal("restarted before Telegram receipt")
	}
	_, _ = e.Memory.DB.Exec("INSERT INTO memory_state VALUES(?,?)", "telegram-delivered:job", "completed")
	e.inFlight = 1
	if request() {
		t.Fatal("restarted with active work")
	}
	e.inFlight = 0
	if !request() || !e.restarting.Load() {
		t.Fatal("did not drain")
	}
	if _, err := e.Submit("new", "local", "hi"); err == nil {
		t.Fatal("accepted work after draining")
	}
	e.restarting.Store(false)
	j.PendingSteering = 1
	_ = e.persist(j)
	w := httptest.NewRecorder()
	restartHandler(e).ServeHTTP(w, httptest.NewRequest("POST", "/v1/harness/restart/prepare", strings.NewReader(`{"id":"restart"}`)))
	if w.Code != 409 {
		t.Fatal("pending steering lost", w.Code)
	}
}

func TestSelfRestartAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "rollback"}[fail], func(t *testing.T) {
			dir, err := os.MkdirTemp("", "alina-self-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			t.Setenv("ALINA_TEST_FAIL_START", "1")
			c := DefaultConfig()
			c.WorkDir = dir
			c.Memory.Dream = false
			if err = SaveConfig(dir, c); err != nil {
				t.Fatal(err)
			}
			e, err := NewEngine(dir, c, &scriptedModel{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			tx := restartTransaction{ID: randomID(), Owner: "local", JobID: "original", AfterID: "resumed", State: "waiting", Previous: c, Next: c}
			tx.Next.ReasoningEffort = "high"
			if fail {
				tx.Next.Model = "startup-failure-fixture"
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
			tx.Base = contentID(string(raw))
			if err = e.persist(&runningJob{Job: Job{ID: tx.JobID, Session: "local", Owner: "local", Kind: "chat", Input: "Change reasoning, then report your status", Output: "Restarting.", Status: "completed", Continuation: tx.AfterID, Created: time.Now()}}); err != nil {
				t.Fatal(err)
			}
			e.Close()
			if err = writeJSON(restartPath(dir), tx); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			defer func() {
				stopCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				_ = stopDaemon(stopCtx, dir)
			}()
			if err = startDaemon(ctx, dir, io.Discard); err != nil {
				t.Fatal(err)
			}
			first, err := daemonStatus(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			// Real detached helper subprocess, same path used by the native tool.
			if err = launchRestart(dir, tx.ID); err != nil {
				t.Fatal(err)
			}
			var resumed Job
			for ctx.Err() == nil {
				if err = localRequest(ctx, dir, "GET", "/v1/jobs/"+tx.AfterID, nil, &resumed); err == nil && terminalStatus(resumed.Status) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if ctx.Err() != nil {
				log, _ := os.ReadFile(filepath.Join(dir, "logs", "daemon.log"))
				t.Fatal(ctx.Err(), string(log))
			}
			status, err := daemonStatus(ctx, dir)
			if err != nil || status["pid"] == first["pid"] {
				t.Fatal("not restarted", status, err)
			}
			saved, err := LoadConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			expected := "high"
			if fail {
				expected = "low"
			}
			if saved.ReasoningEffort != expected || saved.Model != c.Model {
				t.Fatal("wrong saved config", saved.ReasoningEffort, saved.Model)
			}
			if resumed.Session != "local" || resumed.Owner != "local" || resumed.ServiceNotice == "" {
				t.Fatal("lost continuation", resumed)
			}
			if fail && !strings.Contains(resumed.ServiceNotice, "restored") {
				t.Fatal("rollback not reported", resumed)
			}
			if !fail && !strings.Contains(resumed.ServiceNotice, "passed") {
				t.Fatal("readiness not reported", resumed)
			}
			// No auth exists: the outcome must survive even when the resumed model fails.
			if resumed.Status != "failed" || resumed.Output != resumed.ServiceNotice {
				t.Fatal("lost lifecycle outcome on model failure", resumed)
			}
		})
	}
}

func TestRestartRejectsConcurrentConfigWrite(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	_ = SaveConfig(dir, c)
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	tx := restartTransaction{Base: contentID(string(raw)), Previous: c, Next: c}
	tx.Next.ReasoningEffort = "high"
	c.Verbosity = "high"
	_ = SaveConfig(dir, c)
	if err := installRestartConfig(dir, tx, false); err == nil {
		t.Fatal("overwrote intervening configuration")
	}
	saved, _ := LoadConfig(dir)
	if saved.Verbosity != "high" || saved.ReasoningEffort != "low" {
		t.Fatal("intervening edit lost")
	}
}

func TestRestartCompletionIsIdempotentAndCancellationDoesNotReplay(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "resumed"}[resume], func(t *testing.T) {
			var calls atomic.Int32
			e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
				calls.Add(1)
				return Message{Role: "assistant", Content: "Verified."}, nil
			}))
			tx := restartTransaction{ID: "restart", JobID: "original", AfterID: "after", Owner: "local", State: "done", Outcome: "Lifecycle outcome", Resume: resume}
			status := "cancelled"
			if resume {
				status = "completed"
			}
			if err := e.persist(&runningJob{Job: Job{ID: tx.JobID, Session: "same-chat", Owner: "local", Input: "requested work", Status: status, Created: time.Now()}}); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(restartPath(e.Dir), tx); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				restartHandler(e).ServeHTTP(w, httptest.NewRequest("POST", "/v1/harness/restart/complete", strings.NewReader(`{"id":"restart"}`)))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			next := awaitStatus(t, e, tx.AfterID, "completed")
			want := int32(0)
			if resume {
				want = 1
			}
			if calls.Load() != want || next.Session != "same-chat" || next.ServiceNotice != tx.Outcome {
				t.Fatal("duplicate/replayed/misrouted completion", calls.Load(), next)
			}
		})
	}
}

func TestTelegramRetriesInputDuringSelfRestart(t *testing.T) {
	e := newTestEngine(t, nil)
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, nil)
	m := &tgMessage{Text: "new request"}
	m.From.ID = 42
	m.Chat.ID = 42
	m.Chat.Type = "private"
	e.restarting.Store(true)
	if err := tg.processUpdate(e.ctx, tgUpdate{ID: 10, Message: m}); !errors.Is(err, errHarnessRestarting) {
		t.Fatal("incoming update was acknowledged/dropped", err)
	}
	if len(e.Jobs("")) != 0 {
		t.Fatal("submitted while draining")
	}
}

func TestTerminalFollowsRestartContinuation(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "interactive"}[interactive], func(t *testing.T) {
			dir, err := os.MkdirTemp("", "alina-chat-restart-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			ln, err := net.Listen("unix", socketPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/jobs/after" {
					if requests.Add(1) == 1 {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(Job{ID: "after", Status: "completed", Output: "Restart verified"})
					return
				}
				_ = json.NewEncoder(w).Encode(Job{ID: "before", Status: "completed", Output: "Restarting", Continuation: "after"})
			})}
			go server.Serve(ln)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var out bytes.Buffer
			if interactive {
				err = chat(ctx, dir, "local", bufio.NewReader(strings.NewReader("hello\n")), &out)
			} else {
				err = waitJob(ctx, dir, "before", bufio.NewReader(strings.NewReader("")), &out)
			}
			if err != nil || !strings.Contains(out.String(), "Restart verified") || strings.Count(out.String(), "Restarting") != 1 {
				t.Fatal(out.String(), err)
			}
		})
	}
}
