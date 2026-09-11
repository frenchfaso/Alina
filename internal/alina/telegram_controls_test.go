package alina

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const controlCatalog = `{"models":[
 {"slug":"gpt-6-astra","visibility":"list","context_window":272000,"input_modalities":["text","image"],"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}]},
 {"slug":"small-fixture","visibility":"list","context_window":8192,"input_modalities":["text"],"default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"none"},{"effort":"high"}]},
 {"slug":"hidden-fixture","visibility":"hide","context_window":8192,"supported_reasoning_levels":[]}
]}`

func controlEngine(t *testing.T) (*Engine, *atomic.Int32, *atomic.Bool, *[]map[string]any, *sync.Mutex) {
	t.Helper()
	var requests atomic.Int32
	var offline atomic.Bool
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			requests.Add(1)
			if offline.Load() {
				http.Error(w, "unavailable", 503)
				return
			}
			if r.URL.Query().Get("client_version") != catalogClientVersion || r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("ChatGPT-Account-ID") != "account" {
				t.Error("missing catalog auth/protocol headers")
			}
			io.WriteString(w, controlCatalog)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Verified.\"}]}]}}\n\n")
	}))
	t.Cleanup(server.Close)
	e := newTestEngine(t, nil)
	if err := writeJSON(filepath.Join(e.Dir, "chatgpt.json"), Credential{Access: "fixture", AccountID: "account", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	e.Model = &Provider{Config: e.Config, Auth: &Auth{Dir: e.Dir, Client: server.Client()}, Client: server.Client(), BaseURL: server.URL + "/responses", Workspace: e.Workspace()}
	return e, &requests, &offline, &bodies, &mu
}
func TestProviderCatalogCacheAndAccountIsolation(t *testing.T) {
	e, count, offline, _, _ := controlEngine(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if _, err := e.models(e.ctx, true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("catalog fetched per caller", count.Load())
	}
	c, err := e.models(e.ctx, true)
	if err != nil || len(c.Models) != 2 {
		t.Fatal(c, err)
	}
	first, _ := c.model(defaultModel)
	if strings.Contains(jsonText(first.Levels), "ultra") || len(first.Levels) != 5 {
		t.Fatal("offered orchestration as effort", first)
	}
	// Reload persisted metadata without a request.
	e.catalog = reasoningCatalog{}
	if _, err = e.models(e.ctx, true); err != nil || count.Load() != 1 {
		t.Fatal("cache not persisted", count.Load(), err)
	}
	raw, _ := os.ReadFile(filepath.Join(e.Dir, "model-catalog.json"))
	if strings.Contains(string(raw), "account") || strings.Contains(string(raw), "Bearer") {
		t.Fatal("private auth cached")
	}
	e.catalog.mu.Lock()
	e.catalog.cached.Fetched = time.Now().Add(-25 * time.Hour)
	e.catalog.retry = time.Time{}
	e.catalog.mu.Unlock()
	offline.Store(true)
	c, err = e.models(e.ctx, true)
	if err != nil || !c.Stale || count.Load() != 2 {
		t.Fatal("no stale-cache fallback", c, err, count.Load())
	}
	if _, err = e.models(e.ctx, true); err != nil || count.Load() != 2 {
		t.Fatal("missing failure backoff")
	}
	if err = writeJSON(filepath.Join(e.Dir, "chatgpt.json"), Credential{Access: "fixture", AccountID: "other", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if c, err = e.models(e.ctx, false); err == nil || c.Key != "" {
		t.Fatal("metadata reused across accounts", c, err)
	}
}
func TestPersonalModelAndReasoningReachProvider(t *testing.T) {
	e, _, _, bodies, mu := controlEngine(t)
	c, err := e.models(e.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "model", "small-fixture", c); err != nil {
		t.Fatal(err)
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "think", "medium", c); err == nil {
		t.Fatal("invalid effort accepted")
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "think", "none", c); err != nil {
		t.Fatal(err)
	}
	m, effort := e.selectedModel(e.ctx, "telegram:2", c)
	if m.ID != defaultModel || effort != "low" {
		t.Fatal("preferences crossed people", m, effort)
	}
	j, err := e.Submit("personal", "telegram:1", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	result := awaitStatus(t, e, j.ID, "completed")
	if result.Model != "small-fixture" || result.Reasoning != "none" || e.Config.Model != defaultModel {
		t.Fatal("wrong effective model", result)
	}
	mu.Lock()
	body := (*bodies)[0]
	mu.Unlock()
	if body["model"] != "small-fixture" || body["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatal(body)
	}
	for _, tool := range body["tools"].([]any) {
		if tool.(map[string]any)["name"] == "view_image" {
			t.Fatal("vision tool offered for text-only model")
		}
	}
	noVision := &Provider{VisionDisabled: true}
	prepared := noVision.prepareImages([]Message{{Role: "user", Attachments: []Attachment{{Image: true, Path: "must-not-read.png"}}}})
	if len(prepared[0].imageInputs) != 0 || !strings.Contains(prepared[0].Content, "does not support image input") {
		t.Fatal("text-only model received image")
	}
	r := &runningJob{Job: Job{Kind: "chat", Owner: "telegram:1"}, ctx: e.ctx}
	e.refreshJobModel(r)
	if e.contextBudget(r) != 8192 {
		t.Fatal("context not capped for smaller model")
	}
	if err = e.setModelPreference(e.ctx, "telegram:1", "model", "default", c); err != nil {
		t.Fatal(err)
	}
	m, effort = e.selectedModel(e.ctx, "telegram:1", c)
	if m.ID != defaultModel || effort != "low" {
		t.Fatal("default did not reset model and reasoning")
	}
	e.refreshJobModel(r)
	if r.Model != defaultModel || !r.model.Vision || e.contextBudget(r) != e.Config.ContextTokens {
		t.Fatal("running job did not refresh capabilities")
	}
	// Background inference retains global settings, independent of personal preferences.
	dream := &runningJob{Job: Job{Kind: "dream", Owner: "alina", Session: "dream", ID: "dream"}, ctx: e.ctx}
	e.refreshJobModel(dream)
	if dream.model != nil {
		t.Fatal("personal preference applied to dream")
	}
	e.Config.Model = "explicit-model-outside-catalog"
	e.refreshJobModel(r)
	if r.model != nil || r.Model != e.Config.Model || r.Reasoning != e.Config.ReasoningEffort {
		t.Fatal("catalog overrode explicitly configured model")
	}
}
func TestTelegramModelMenusRejectStaleButtons(t *testing.T) {
	e, count, _, _, _ := controlEngine(t)
	var sent []map[string]any
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		sent = append(sent, b)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	owner := tg.owner()
	if err := tg.modelControl(e.ctx, 42, e, owner, "think", "", 0); err != nil {
		t.Fatal(err)
	}
	raw := jsonText(sent)
	if !strings.Contains(raw, "max") || strings.Contains(raw, "ultra") {
		t.Fatal(raw)
	}
	c, _ := e.models(e.ctx, true)
	old := tg.selectorToken(e, owner, "think", c)
	if err := e.setModelPreference(e.ctx, owner, "model", "small-fixture", c); err != nil {
		t.Fatal(err)
	}
	var update tgUpdate
	_ = json.Unmarshal([]byte(`{"callback_query":{"id":"button","data":"p:`+old+`:think:high","from":{"id":42},"message":{"chat":{"id":42,"type":"private"}}}}`), &update)
	if err := tg.processUpdate(e.ctx, update); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonText(sent[len(sent)-1]), "Menu scaduto") {
		t.Fatal(sent)
	}
	p := e.modelPreference(e.ctx, owner, c.Key)
	if p.Effort != "" {
		t.Fatal("stale button changed preference")
	}
	if err := tg.modelControl(e.ctx, 42, e, owner, "model", "", 0); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("menus fetched catalog repeatedly", count.Load())
	}
	for _, message := range sent {
		if keyboard, ok := message["reply_markup"]; ok {
			var inspect func(any)
			inspect = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					for k, v := range x {
						if k == "callback_data" && len(v.(string)) > 64 {
							t.Fatal("Telegram callback exceeds limit")
						}
						inspect(v)
					}
				case []any:
					for _, v := range x {
						inspect(v)
					}
				}
			}
			inspect(keyboard)
		}
	}
}

func TestTelegramStopRetryDoesNotCancelNewWork(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	var sends atomic.Int32
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if sends.Add(1) == 1 {
			return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":false}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	first, err := e.Submit("chat", tg.owner(), "first")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, first.ID, "running")
	if err = tg.stopControl(e.ctx, 42, e, tg.owner(), "update-1"); err == nil {
		t.Fatal("expected failed acknowledgement")
	}
	awaitStatus(t, e, first.ID, "cancelled")
	second, err := e.Submit("chat", tg.owner(), "second")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, second.ID, "running")
	if err = tg.stopControl(e.ctx, 42, e, tg.owner(), "update-1"); err != nil {
		t.Fatal(err)
	}
	current, _ := e.Get(second.ID)
	if current.Status != "running" {
		t.Fatal("retry cancelled later request", current.Status)
	}
}
