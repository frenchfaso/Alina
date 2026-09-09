package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTextOnlyAttachmentUsesMetadataContext(t *testing.T) {
	e, _, _, bodies, mu := controlEngine(t)
	c, err := e.models(e.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "model", "small-fixture", c); err != nil {
		t.Fatal(err)
	}
	j, err := e.Receive("image-chat", "telegram:1", "Please list the attached filename.", "image-request", Attachment{Image: true, Name: "photo.png", Path: e.Workspace() + "/photo.png", MIME: "image/png", Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		got, _ := e.Get(j.ID)
		if terminalStatus(got.Status) {
			mu.Lock()
			n := len(*bodies)
			mu.Unlock()
			if got.Status != "completed" {
				t.Fatalf("text-only metadata request failed: %s; provider requests=%d", got.Error, n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout")
}

func TestCatalogRefreshLeavesCachedReadersAvailable(t *testing.T) {
	e, _, _, _, _ := controlEngine(t)
	if _, err := e.models(e.ctx, true); err != nil {
		t.Fatal(err)
	}
	e.catalog.mu.Lock()
	e.catalog.cached.Fetched = time.Now().Add(-25 * time.Hour)
	e.catalog.retry = time.Time{}
	e.catalog.mu.Unlock()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := e.Model.(*Provider)
	p.Client = &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(controlCatalog))}, nil
	})}
	go func() { defer close(finished); _, _ = e.models(e.ctx, true) }()
	<-entered
	read := make(chan struct{})
	go func() { _, _ = e.models(e.ctx, false); close(read) }()
	blocked := false
	select {
	case <-read:
	case <-time.After(150 * time.Millisecond):
		blocked = true
	}
	close(release)
	<-finished
	<-read
	if blocked {
		t.Fatal("cached-only read blocked behind provider network refresh")
	}
}

func TestPreferenceChangeRepreparesQueuedRequest(t *testing.T) {
	e, _, _, bodies, mu := controlEngine(t)
	c, err := e.models(e.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	release, err := e.gate.acquire(e.ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.Submit("waiting-chat", "telegram:1", "Hello")
	if err != nil {
		release()
		t.Fatal(err)
	}
	queued := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		e.gate.mu.Lock()
		queued = e.gate.foreground > 0
		e.gate.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !queued {
		release()
		t.Fatal("never queued")
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "model", "small-fixture", c); err != nil {
		release()
		t.Fatal(err)
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "think", "none", c); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	awaitStatus(t, e, j.ID, "completed")
	mu.Lock()
	raw, _ := json.Marshal((*bodies)[0]["model"])
	mu.Unlock()
	if string(raw) != `"small-fixture"` {
		t.Fatalf("next request used old model %s after preference saved", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	body := (*bodies)[0]
	if body["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatal("old effort sent", body)
	}
	for _, tool := range body["tools"].([]any) {
		if tool.(map[string]any)["name"] == "view_image" {
			t.Fatal("old vision tools sent")
		}
	}
	if strings.Contains(body["instructions"].(string), "Use view_image to inspect saved images") {
		t.Fatal("old vision prompt sent")
	}

}

func TestCatalogRefreshRejectsAccountChange(t *testing.T) {
	e, _, _, _, _ := controlEngine(t)
	if _, err := e.models(e.ctx, true); err != nil {
		t.Fatal(err)
	}
	e.catalog.mu.Lock()
	e.catalog.cached.Fetched = time.Now().Add(-25 * time.Hour)
	e.catalog.retry = time.Time{}
	e.catalog.mu.Unlock()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	p := e.Model.(*Provider)
	p.Client = &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(controlCatalog))}, nil
	})}
	go func() { _, err := e.models(e.ctx, true); finished <- err }()
	<-entered
	if err := writeJSON(filepath.Join(e.Dir, "chatgpt.json"), Credential{Access: "other", AccountID: "other", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		close(release)
		<-finished
		t.Fatal(err)
	}
	if _, err := e.models(e.ctx, false); err == nil {
		close(release)
		<-finished
		t.Fatal("old account's cached metadata reused")
	}
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("old account's refresh accepted")
	}
	if c, err := e.models(e.ctx, false); err == nil || c.Key != "" {
		t.Fatal("old refresh repopulated new account")
	}
}

func TestCatalogFirstLoadWaiterCanCancel(t *testing.T) {
	e, _, _, _, _ := controlEngine(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := e.Model.(*Provider)
	p.Client = &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(controlCatalog))}, nil
	})}
	go func() { defer close(finished); _, _ = e.models(e.ctx, true) }()
	<-entered
	ctx, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := e.models(ctx, true); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error("waiter did not honor context", err)
		}
	case <-time.After(time.Second):
		t.Error("waiter blocked behind first load")
	}
	close(release)
	<-finished
}

func TestTelegramStopCancelsAllOwnedWorkAndKeepsOtherPeople(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	client := &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	first, err := e.Submit("one", tg.owner(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Submit("one", tg.owner(), "queued")
	if err != nil {
		t.Fatal(err)
	}
	third, err := e.submit("two", tg.owner(), "scheduled", "scheduled", "task")
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.Submit("other", "telegram:77", "other person's work")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, first.ID, "running")
	if err = tg.stopControl(e.ctx, 42, e, tg.owner(), "stop-all"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID, third.ID} {
		awaitStatus(t, e, id, "cancelled")
	}
	got, _ := e.Get(other.ID)
	if terminalStatus(got.Status) {
		t.Fatal("other person's work was stopped", got)
	}
	late, err := e.Submit("one", tg.owner(), "later work")
	if err != nil {
		t.Fatal(err)
	}
	if err = tg.stopControl(e.ctx, 42, e, tg.owner(), "stop-all"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.Get(late.ID)
	if terminalStatus(got.Status) {
		t.Fatal("retried stop cancelled new work", got)
	}
}

func TestTelegramStopDismissesApproval(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{Command: "printf must-not-run", Network: true})
	client := &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	job, err := e.Submit("approval", tg.owner(), "do work")
	if err != nil {
		t.Fatal(err)
	}
	pending := awaitStatus(t, e, job.ID, "approval")
	if err = tg.stopControl(e.ctx, 42, e, tg.owner(), "stop-approval"); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, job.ID, "cancelled")
	if err = e.Approve(job.ID, pending.Approval.ID, "once", tg.owner()); err == nil {
		t.Fatal("stopped approval still accepted")
	}
}

func TestPreferenceChangeDuringCheckpointRepreparesTurn(t *testing.T) {
	e, _, _, _, _ := controlEngine(t)
	c, err := e.models(e.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	e.Config.ContextTokens = 16384
	history := []Message{}
	for i := 0; i < 6; i++ {
		history = append(history, Message{Role: "user", ArchiveID: fmt.Sprint("old-", i), Content: "Keep reference EVIDENCE-123. " + strings.Repeat("old context ", 1000)}, Message{Role: "assistant", ArchiveID: fmt.Sprint("reply-", i), Content: "Recorded."})
	}
	path := filepath.Join(e.Dir, "sessions", "switch-during-compaction.json")
	if err = writeJSON(path, history); err != nil {
		t.Fatal(err)
	}
	var changed bool
	var final map[string]any
	p := e.Model.(*Provider)
	p.Client = &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		checkpoint := strings.HasPrefix(r.Header.Get("session-id"), "checkpoint-")
		if checkpoint && !changed {
			changed = true
			if err := e.setModelPreference(e.ctx, "telegram:1", "model", "small-fixture", c); err != nil {
				return nil, err
			}
			if err := e.setModelPreference(e.ctx, "telegram:1", "think", "none", c); err != nil {
				return nil, err
			}
		}
		if !checkpoint {
			final = body
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Retain EVIDENCE-123 and answer the latest request."}]}]}`))}, nil
	})}
	job, err := e.Submit("switch-during-compaction", "telegram:1", "Continue carefully.")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, job.ID, "completed")
	// The completion channel synchronizes test inspection with the transport.
	e.wg.Wait()
	if !changed || final == nil || final["model"] != "small-fixture" || final["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatal("checkpoint change did not reach final request", final)
	}
	for _, tool := range final["tools"].([]any) {
		if tool.(map[string]any)["name"] == "view_image" {
			t.Fatal("old tools survived checkpoint change")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []Message
	if err = json.Unmarshal(raw, &kept); err != nil {
		t.Fatal(err)
	}
	snapshot := &runningJob{Job: Job{Kind: "chat", Owner: "telegram:1"}, ctx: e.ctx}
	e.refreshJobModel(snapshot)
	overhead := estimatedTokens([]Message{{Role: "system", Content: e.prompt(e.toolsFor(snapshot))}}) + estimatedTokens(e.toolsFor(snapshot))
	if estimatedTokens(kept, false)+overhead > e.contextBudget(snapshot)*95/100 {
		t.Fatal("new context budget ignored")
	}
	if !strings.Contains(string(raw), "Continue carefully.") || !strings.Contains(string(raw), "EVIDENCE-123") {
		t.Fatal("checkpoint lost the request or evidence")
	}
}
