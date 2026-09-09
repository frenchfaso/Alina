package alina

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTelegramTypingRecoversTransientFailures(t *testing.T) {
	for _, failure := range []string{"network", "server", "rejected"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &Engine{jobChanged: &wakeSignal{}, jobs: map[string]*runningJob{
					"chat": {Job: Job{Status: "running", Kind: "chat", Owner: "telegram:42"}},
				}}
				var calls atomic.Int32
				tg := NewTelegram("", TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						switch failure {
						case "network":
							return nil, errors.New("temporary network failure")
						case "server":
							return &http.Response{StatusCode: 502, Body: http.NoBody}, nil
						case "rejected":
							return &http.Response{StatusCode: 429, Body: http.NoBody}, nil
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
				})})
				runIdleWorker(t, tg.typing)
				if calls.Load() != 1 {
					t.Fatal("missing initial activity", calls.Load())
				}
				time.Sleep(4 * time.Second)
				synctest.Wait()
				if failure == "rejected" {
					if calls.Load() != 1 {
						t.Fatal("API rate limit was retried too soon", calls.Load())
					}
					time.Sleep(26 * time.Second)
					synctest.Wait()
				}
				if calls.Load() != 2 {
					t.Fatal("activity did not recover", calls.Load())
				}
				time.Sleep(4 * time.Second)
				synctest.Wait()
				if calls.Load() != 3 {
					t.Fatal("activity was not renewed", calls.Load())
				}
			})
		})
	}
}

func TestTelegramTypingToleratesSlowNetwork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &Engine{jobChanged: &wakeSignal{}, jobs: map[string]*runningJob{
			"chat": {Job: Job{Status: "running", Kind: "chat", Owner: "telegram:42"}},
		}}
		var delivered atomic.Bool
		tg := NewTelegram("", TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(3 * time.Second):
				delivered.Store(true)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
			}
		})})
		runIdleWorker(t, tg.typing)
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if !delivered.Load() {
			t.Fatal("typing timed out before the mobile connection could respond")
		}
	})
}

func TestTelegramTypingTracksQueuedFamilyChat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := familyEngine(t, nil)
		calls := map[int64]int{}
		var callsMu sync.Mutex
		checkCalls := func(want int) {
			t.Helper()
			callsMu.Lock()
			defer callsMu.Unlock()
			if calls[42] != want || len(calls) != 1 {
				t.Fatalf("typing calls = %v, want only %d for requester 42", calls, want)
			}
		}
		tg := NewTelegram("", e.Config.Telegram, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			var body struct {
				ChatID int64  `json:"chat_id"`
				Action string `json:"action"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action != "typing" || !strings.HasSuffix(r.URL.Path, "/sendChatAction") {
				t.Error("unexpected typing request", err, body)
			}
			callsMu.Lock()
			calls[body.ChatID]++
			callsMu.Unlock()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
		})})
		scope, _ := tg.engineFor(42)
		release := make(chan struct{})
		// Keep the normal engine run loop queued without issuing model calls.
		scope.mu.Lock()
		scope.sessionTail["queued-chat"] = release
		scope.mu.Unlock()
		runIdleWorker(t, tg.typing)
		t.Cleanup(func() { close(release) })
		job, err := scope.Submit("queued-chat", telegramOwner(tg.Config, 42), "pending request")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		checkCalls(1)
		time.Sleep(4 * time.Second)
		synctest.Wait()
		checkCalls(2)
		if err = scope.Cancel(job.ID, job.Owner); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		checkCalls(2)
	})
}

// Ensure cancelling the worker interrupts an in-flight typing request.
func TestTelegramTypingShutdownCancelsRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &Engine{jobChanged: &wakeSignal{}, jobs: map[string]*runningJob{
			"chat": {Job: Job{Status: "running", Kind: "chat", Owner: "telegram:42"}},
		}}
		var stopped bool
		tg := NewTelegram("", TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			stopped = true
			return nil, r.Context().Err()
		})})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); tg.typing(ctx) }()
		synctest.Wait()
		cancel()
		<-done
		if !stopped {
			t.Fatal("in-flight typing request was not cancelled")
		}
	})
}

func TestTelegramPollReconnectsAfterStalledNetwork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newTestEngine(t, nil)
		var polls, timeouts atomic.Int32
		tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/getUpdates") {
				var body struct {
					Timeout int `json:"timeout"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Timeout != 50 {
					t.Error("long-poll duration changed", err, body)
				}
				polls.Add(1)
				<-r.Context().Done()
				if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
					timeouts.Add(1)
				}
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
		})})
		runIdleWorker(t, tg.Run)
		time.Sleep(64 * time.Second)
		synctest.Wait()
		if polls.Load() != 1 || timeouts.Load() != 0 {
			t.Fatal("poll cancelled without long-poll headroom", polls.Load(), timeouts.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if polls.Load() != 1 || timeouts.Load() != 1 {
			t.Fatal("stalled poll did not time out", polls.Load(), timeouts.Load())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if polls.Load() != 2 {
			t.Fatal("receiver did not reconnect", polls.Load())
		}
	})
}
