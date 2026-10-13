package alina

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelRetryDoesNotReplayEarlierTools(t *testing.T) {
	var calls atomic.Int32
	var failedRequest string
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, messages []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		switch calls.Add(1) {
		case 1:
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "append_once", Name: "shell", Arguments: `{"command":"printf x >> retry-side-effect"}`}}}, nil
		case 2:
			failedRequest = jsonText(messages)
			return Message{Usage: &TokenUsage{InputTokens: 7, OutputTokens: 3}}, &providerResponseError{Event: "response.failed", Code: "server_error"}
		case 3:
			if jsonText(messages) != failedRequest {
				t.Error("retry changed the inference or replayed the preceding tool")
			}
			return Message{Role: "assistant", Content: "Done.", Usage: &TokenUsage{InputTokens: 11, OutputTokens: 5}}, nil
		default:
			t.Error("unexpected model request")
			return Message{}, errors.New("unexpected request")
		}
	}))
	j, err := e.Submit("retry-fixture", "local", "Append once and confirm.")
	if err != nil {
		t.Fatal(err)
	}
	j = awaitStatus(t, e, j.ID, "completed")
	data, err := os.ReadFile(filepath.Join(e.Config.WorkDir, "retry-side-effect"))
	if err != nil || string(data) != "x" || calls.Load() != 3 {
		t.Fatal("tool was lost or replayed", string(data), calls.Load(), err)
	}
	if j.Usage.InputTokens != 18 || j.Usage.OutputTokens != 8 {
		t.Fatal("failed attempt consumption was lost", j.Usage)
	}
}

func TestModelRetryIsBoundedAndSelective(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int32
	}{
		{"temporary", &providerResponseError{Event: "response.failed", Code: "server_error"}, 2},
		{"incomplete", &providerResponseError{Event: "response.incomplete", Reason: "max_output_tokens"}, 1},
		{"quota", &providerResponseError{Event: "error", Code: "insufficient_quota"}, 1},
		{"unknown", &providerResponseError{Event: "response.failed", Code: "unknown_error"}, 1},
		{"interrupted", &providerResponseError{Event: "stream.interrupted"}, 1},
		{"http rejection", &remoteHTTPError{Status: 429}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
				calls.Add(1)
				return Message{}, tc.err
			}))
			j := &runningJob{Job: Job{ID: "retry-fixture", Kind: "chat"}, ctx: e.ctx}
			_, err := (jobModel{e: e, j: j}).Complete(e.ctx, "fixture", nil, nil, nil)
			if !errors.Is(err, tc.err) || calls.Load() != tc.want {
				t.Fatal("wrong retry count or error", calls.Load(), err)
			}
		})
	}
}

func TestModelRetryRespectsCancellationAndSteering(t *testing.T) {
	for _, mode := range []string{"cancel", "work deadline", "steer", "search steer"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			var j *runningJob
			var e *Engine
			e = newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
				calls.Add(1)
				switch mode {
				case "cancel":
					time.AfterFunc(25*time.Millisecond, cancel)
				case "steer", "search steer":
					e.mu.Lock()
					j.PendingSteering = 1
					e.mu.Unlock()
				}
				return Message{}, &providerResponseError{Event: "error", Code: "service_unavailable"}
			}))
			j = &runningJob{Job: Job{ID: "retry-fixture", Kind: "chat"}, ctx: ctx}
			callCtx := ctx
			if mode == "work deadline" {
				var cancelWork context.CancelFunc
				j.workCtx, cancelWork = context.WithTimeout(e.ctx, 100*time.Millisecond)
				defer cancelWork()
				callCtx = e.ctx
			}
			started := time.Now()
			var err error
			if mode == "search steer" {
				e.Search = &Search{Config: e.Config.Search}
				_, err = (jobModel{e: e, j: j}).infer(callCtx, "search", func(ctx context.Context) (Message, error) {
					return e.Model.Complete(ctx, "fixture", nil, nil, nil)
				})
			} else {
				_, err = (jobModel{e: e, j: j}).Complete(callCtx, "fixture", nil, nil, nil)
			}
			want := error(context.Canceled)
			if mode == "steer" || mode == "search steer" {
				want = errRequestChanged
			} else {
				if mode == "work deadline" {
					want = context.DeadlineExceeded
				}
				if time.Since(started) > 500*time.Millisecond {
					t.Fatal("backoff ignored cancellation")
				}
			}
			if !errors.Is(err, want) || calls.Load() != 1 {
				t.Fatal("retry dispatched stale or cancelled work", calls.Load(), err)
			}
		})
	}
}

func TestModelRetryCountsAutonomyAttempts(t *testing.T) {
	var calls atomic.Int32
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		calls.Add(1)
		return Message{}, &providerResponseError{Event: "error", Code: "server_error"}
	}))
	e.Config.Autonomy.Enabled = true
	e.Config.Autonomy.MaxCalls = 1
	j := &runningJob{Job: Job{ID: "retry-fixture", Kind: "initiative"}, ctx: e.ctx}
	_, err := (jobModel{e: e, j: j}).Complete(e.ctx, "fixture", nil, nil, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatal("retry exceeded the daily inference budget", calls.Load(), err)
	}
}

func TestResearchRetryChecksSteeringAfterWaitingForGate(t *testing.T) {
	first := make(chan struct{})
	var calls atomic.Int32
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		if calls.Add(1) == 1 {
			close(first)
		}
		return Message{}, &providerResponseError{Event: "error", Code: "server_error"}
	}))
	e.Search = &Search{Config: e.Config.Search}
	j := &runningJob{Job: Job{ID: "retry-search", Kind: "chat"}, ctx: e.ctx}
	result := make(chan error, 1)
	go func() {
		_, err := (jobModel{e: e, j: j}).infer(e.ctx, "search", func(ctx context.Context) (Message, error) {
			return e.Model.Complete(ctx, "search-fixture", nil, nil, nil)
		})
		result <- err
	}()
	<-first
	release, err := e.gate.acquire(e.ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.gate.mu.Lock()
		waiting := e.gate.foreground > 0
		e.gate.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry never waited for the gate")
		}
		time.Sleep(time.Millisecond)
	}
	e.mu.Lock()
	j.PendingSteering = 1
	e.mu.Unlock()
	release()
	release = nil
	if err := <-result; !errors.Is(err, errRequestChanged) || calls.Load() != 1 {
		t.Fatal("research retry ignored steering received while waiting", calls.Load(), err)
	}
}
