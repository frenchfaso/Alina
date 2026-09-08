package alina

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func runIdleWorker(t *testing.T, run func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	synctest.Wait()
}

func TestSchedulerRearmsOnTaskChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
			calls.Add(1)
			return Message{Role: "assistant", Content: "done"}, nil
		}))
		s := e.Scheduler
		s.tasks = map[string]ScheduledTask{}
		runIdleWorker(t, s.Run)
		late, err := s.AddOnce("later", time.Now().Add(2*time.Hour).Format(time.RFC3339), "later", "local", "user", "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		early, err := s.AddOnce("earlier", time.Now().Add(time.Minute).Format(time.RFC3339), "earlier", "local", "user", "")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err = s.Change(early.ID, "pause", "local"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("paused task ran")
		}
		if err = s.Change(early.ID, "resume", "local"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("new deadline did not wake scheduler", calls.Load())
		}
		if err = s.Change(late.ID, "remove", "local"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Hour)
		synctest.Wait()
		if calls.Load() != 1 || !s.nextWake().IsZero() {
			t.Fatal("removed or completed task was scheduled again", calls.Load())
		}
	})
}

func TestSchedulerWaitsForRunningOccurrence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
			calls.Add(1)
			if calls.Load() == 1 {
				select {
				case <-release:
				case <-ctx.Done():
					return Message{}, ctx.Err()
				}
			}
			return Message{Role: "assistant", Content: "done"}, nil
		}))
		j, err := e.Submit("earlier", "local", "long occurrence")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		e.Scheduler.tasks = map[string]ScheduledTask{"repeat": {ID: "repeat", Enabled: true, Cron: "@every 1h", Timezone: "UTC", Next: time.Now().Add(time.Minute), CatchUp: true, LastJob: j.ID, Owner: "local", Prompt: "next occurrence", Kind: "chat"}}
		runIdleWorker(t, e.Scheduler.Run)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 1 || !e.Scheduler.nextWake().IsZero() {
			t.Fatal("overlapping occurrence or busy overdue timer", calls.Load())
		}
		close(release)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("completion did not wake overdue task", calls.Load())
		}
	})
}

func TestIdleWorkersDoNotPollStorage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newTestEngine(t, nil)
		e.Scheduler.tasks = map[string]ScheduledTask{}
		events, err := openEventLog(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer events.Close()
		e.Events = events
		tg := NewTelegram(e.Dir, TelegramConfig{OwnerID: 42}, e, nil)
		runIdleWorker(t, e.Scheduler.Run)
		runIdleWorker(t, tg.notify)
		// With no deadline or delivery left, neither worker should touch storage.
		if err = e.Memory.DB.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		b, err := os.ReadFile(events.path)
		if err != nil || len(b) != 0 {
			t.Fatal("idle worker polled closed storage", string(b), err)
		}
	})
}

func TestTelegramDeliveryWakesAndRetriesWithoutNewEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newTestEngine(t, nil)
		var attempts []time.Time
		var attemptsMu sync.Mutex
		snapshot := func() []time.Time {
			attemptsMu.Lock()
			defer attemptsMu.Unlock()
			return append([]time.Time(nil), attempts...)
		}
		client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			attemptsMu.Lock()
			defer attemptsMu.Unlock()
			attempts = append(attempts, time.Now())
			status := http.StatusBadGateway
			if len(attempts) == 4 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
		})}
		tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
		runIdleWorker(t, tg.notify)
		start := time.Now()
		j := &runningJob{Job: Job{ID: "wake-delivery", Session: "local", Owner: tg.owner(), Status: "completed", Created: start, Output: "done"}}
		if err := e.persist(j); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(snapshot()) != 1 {
			t.Fatal("completion did not wake delivery immediately", snapshot())
		}
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if len(snapshot()) != 4 {
			t.Fatal("delivery did not retry", snapshot())
		}
		for i, seconds := range []int{0, 5, 15, 35} {
			if snapshot()[i].Sub(start) != time.Duration(seconds)*time.Second {
				t.Fatal("unexpected retry delay", snapshot())
			}
		}
		pending, err := tg.pendingNotifications(context.Background())
		if err != nil || len(pending) != 0 {
			t.Fatal("delivery receipt missing", pending, err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(snapshot()) != 4 {
			t.Fatal("delivered message sent again")
		}
	})
}

func TestTelegramLongPollRemainsResponsive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newTestEngine(t, nil)
		polling := make(chan struct{}, 1)
		incoming := make(chan struct{})
		var replied atomic.Bool
		client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			body := `{"ok":true,"result":{}}`
			if strings.HasSuffix(r.URL.Path, "/getUpdates") {
				var payload struct{ Timeout int }
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Timeout != 50 {
					t.Errorf("long poll timeout: %d, %v", payload.Timeout, err)
				}
				polling <- struct{}{}
				select {
				case <-incoming:
					body = `{"ok":true,"result":[{"update_id":1,"message":{"text":"/help","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			} else if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				replied.Store(true)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
		runIdleWorker(t, tg.Run)
		<-polling
		time.Sleep(time.Second)
		incoming <- struct{}{}
		synctest.Wait()
		if !replied.Load() {
			t.Fatal("message waited for long poll timeout")
		}
	})
}

func TestFocusViewRefreshesOnUse(t *testing.T) {
	e := newTestEngine(t, nil)
	m := e.Memory
	now := time.Now()
	id, err := m.Note(e.ctx, now, "local", "note", "fact", "Remember the garden", "")
	if err != nil {
		t.Fatal(err)
	}
	check := func(needle string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(m.Dir, "memory", "focus.md"))
		if err != nil || !strings.Contains(string(b), needle) {
			t.Fatal("focus projection is stale", needle, string(b), err)
		}
	}
	check("Remember the garden")
	later := now.Add(30 * 24 * time.Hour)
	if _, err = m.ReadPage(e.ctx, "focus", 0, later); err != nil {
		t.Fatal(err)
	}
	check("attention 0.50")
	if _, err = m.ReadPage(e.ctx, id, 0, later); err != nil {
		t.Fatal(err)
	}
	check("attention 1.00")
	pin := true
	if err = m.Focus(e.ctx, id, &pin, later); err != nil {
		t.Fatal(err)
	}
	check(fmt.Sprintf("%s [fact; pinned]", id))
}

func TestSchedulerRetriesFailedCheckpointWithoutReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
			calls.Add(1)
			return Message{Role: "assistant", Content: "done"}, nil
		}))
		s := e.Scheduler
		s.tasks = map[string]ScheduledTask{}
		task, err := s.AddOnce("checkpoint", time.Now().Add(time.Minute).Format(time.RFC3339), "run once", "local", "user", "")
		if err != nil {
			t.Fatal(err)
		}
		badDir := filepath.Join(t.TempDir(), "file")
		if err = os.WriteFile(badDir, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		original := s.Dir
		s.Dir = badDir
		runIdleWorker(t, s.Run)
		time.Sleep(90 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 || !s.List()[0].Enabled {
			t.Fatal("failed checkpoint replayed work or lost pending occurrence", calls.Load())
		}
		s.mu.Lock()
		s.Dir = original
		s.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		stored := s.List()[0]
		if calls.Load() != 1 || stored.ID != task.ID || stored.Enabled || stored.LastJob == "" {
			t.Fatal("checkpoint was not recovered idempotently", calls.Load(), stored)
		}
		restarted, err := NewScheduler(original, e)
		if err != nil {
			t.Fatal(err)
		}
		if err = restarted.Tick(time.Now()); err != nil || calls.Load() != 1 {
			t.Fatal("restart replayed completed occurrence", calls.Load(), err)
		}
	})
}
