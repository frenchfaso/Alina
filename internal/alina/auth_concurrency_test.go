package alina

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func refreshAccessToken(account string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, account)))
	return "header." + payload + ".signature"
}

func refreshReply(access, refresh string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(jsonText(map[string]any{
		"access_token": access, "refresh_token": refresh, "expires_in": 3600,
	})))}
}

func TestAuthCancelledInferenceDoesNotWaitForAnotherWorker(t *testing.T) {
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), Credential{Access: "old", Refresh: "fixture", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseRefresh := func() { once.Do(func() { close(release) }) }
	defer releaseRefresh()
	auth := &Auth{Dir: dir, Client: &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		close(entered)
		<-release
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	e := newTestEngine(t, nil)
	worker := &runningJob{Job: Job{ID: "worker-refresh", Kind: "delegate", Owner: "telegram:1"}, ctx: e.ctx}
	workerDone := make(chan error, 1)
	go func() {
		_, err := (jobModel{e: e, j: worker}).infer(worker.ctx, "turn", func(ctx context.Context) (Message, error) {
			_, err := auth.Get(ctx)
			return Message{}, err
		})
		workerDone <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	main := &runningJob{Job: Job{ID: "main-waiter", Kind: "chat", Owner: "telegram:2"}, ctx: ctx}
	mainDone := make(chan error, 1)
	go func() {
		_, err := (jobModel{e: e, j: main}).infer(ctx, "turn", func(ctx context.Context) (Message, error) {
			_, err := auth.Get(ctx)
			return Message{}, err
		})
		mainDone <- err
	}()
	// The main inference enters its independent gate while the worker owns
	// the refresh. Cancelling it must not depend on the worker's connection.
	for until := time.Now().Add(time.Second); ; {
		e.gate.mu.Lock()
		acquired := e.gate.busy
		e.gate.mu.Unlock()
		if acquired {
			break
		}
		if time.Now().After(until) {
			t.Fatal("main inference never acquired its gate")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-mainDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled inference returned another error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled inference waited for the unrelated refresh")
	}
	select {
	case <-workerDone:
		t.Fatal("cancelling another person interrupted the worker's refresh")
	default:
	}
	releaseRefresh()
	<-workerDone
}

func TestAuthConcurrentRejectionsReuseRotatedCredential(t *testing.T) {
	dir := t.TempDir()
	old := Credential{Access: "old", Refresh: "old-refresh", AccountID: "account", Expires: time.Now().Add(time.Hour).Unix()}
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), old); err != nil {
		t.Fatal(err)
	}
	access := refreshAccessToken("account")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseRefresh := func() { once.Do(func() { close(release) }) }
	defer releaseRefresh()
	var renewals atomic.Int32
	auth := &Auth{Dir: dir, Client: &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if got := r.Form.Get("refresh_token"); got != old.Refresh {
			t.Error("unexpected refresh token", got)
		}
		if renewals.Add(1) == 1 {
			close(entered)
		} else {
			t.Error("the rejected credential was refreshed more than once")
		}
		select {
		case <-release:
			return refreshReply(access, "rotated-refresh"), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}}
	type result struct {
		credential Credential
		err        error
	}
	const callers = 8
	results := make(chan result, callers)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range callers {
		go func() {
			credential, err := auth.get(ctx, old.Access)
			results <- result{credential, err}
		}()
	}
	<-entered
	releaseRefresh()
	for range callers {
		select {
		case result := <-results:
			if result.err != nil || result.credential.Access != access || result.credential.Refresh != "rotated-refresh" {
				t.Fatal("caller did not reuse the renewed credential", result)
			}
		case <-ctx.Done():
			t.Fatal("renewal waiter did not finish", ctx.Err())
		}
	}
	// Recreating Auth must recover the persisted rotated token without another
	// exchange, rather than depending on state in the first Auth instance.
	recovered := &Auth{Dir: dir, Client: auth.Client}
	credential, err := recovered.Get(ctx)
	if err != nil || credential.Access != access || credential.Refresh != "rotated-refresh" || renewals.Load() != 1 {
		t.Fatal("renewal was not persisted or shared", credential, err, renewals.Load())
	}
}

func TestAuthCancelledRefreshLetsLiveWaiterRecover(t *testing.T) {
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), Credential{Refresh: "old-refresh"}); err != nil {
		t.Fatal(err)
	}
	access := refreshAccessToken("account")
	entered := make(chan struct{})
	var renewals atomic.Int32
	auth := &Auth{Dir: dir, Client: &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if renewals.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return refreshReply(access, "rotated-refresh"), nil
	})}}
	leaderCtx, stopLeader := context.WithCancel(context.Background())
	defer stopLeader()
	leader := make(chan error, 1)
	go func() { _, err := auth.Get(leaderCtx); leader <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waiter := make(chan error, 1)
	go func() {
		credential, err := auth.Get(ctx)
		if err == nil && credential.Access != access {
			err = errors.New("wrong recovered access token")
		}
		waiter <- err
	}()
	stopLeader()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled renewal did not report cancellation", err)
	}
	select {
	case err := <-waiter:
		if err != nil || renewals.Load() != 2 {
			t.Fatal("live waiter could not recover from cancelled renewal", err, renewals.Load())
		}
	case <-ctx.Done():
		t.Fatal("cancelled renewal stranded its waiter", ctx.Err())
	}
}

func TestAuthRefreshFailureCanRetry(t *testing.T) {
	dir := t.TempDir()
	old := Credential{Refresh: "old-refresh"}
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), old); err != nil {
		t.Fatal(err)
	}
	var renewals atomic.Int32
	auth := &Auth{Dir: dir, Client: &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if renewals.Add(1) == 1 {
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return refreshReply(refreshAccessToken("account"), "rotated-refresh"), nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := auth.Get(ctx); err == nil {
		t.Fatal("failed renewal succeeded")
	}
	credential, err := auth.Get(ctx)
	if err != nil || credential.Refresh != "rotated-refresh" || renewals.Load() != 2 {
		t.Fatal("failed renewal prevented recovery", credential, err, renewals.Load())
	}
}
