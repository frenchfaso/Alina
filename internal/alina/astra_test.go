package alina

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAstraOAuthParametersSearchAndUsage(t *testing.T) {
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), Credential{Access: "own-access", AccountID: "own-account", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer own-access" || r.Header.Get("ChatGPT-Account-ID") != "own-account" {
			t.Error("search and chat must reuse Alina's dedicated credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		effort := "low"
		if body["model"] != defaultModel || body["reasoning"].(map[string]any)["effort"] != effort || body["text"].(map[string]any)["verbosity"] != "low" {
			t.Error("incorrect Astra parameters", body)
		}
		for _, key := range []string{"temperature", "top_p", "max_output_tokens", "prompt_cache_retention"} {
			if _, ok := body[key]; ok {
				t.Error("unexpected backend parameter", key)
			}
		}
		if count == 3 {
			tools := body["tools"].([]any)
			if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" || body["tool_choice"] != "required" {
				t.Error("hosted search was not required")
			}
			if !strings.Contains(body["instructions"].(string), "English") {
				t.Error("search notes language")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1200,"output_tokens":140,"input_tokens_details":{"cached_tokens":900,"cache_write_tokens":100},"output_tokens_details":{"reasoning_tokens":120}},"output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done","annotations":[{"type":"url_citation","url":"https://termux.dev","title":"Termux"}]}]}]}}`+"\n\n")
	}))
	defer srv.Close()
	p := &Provider{Config: c, Auth: &Auth{Dir: dir, Client: srv.Client()}, Client: srv.Client(), BaseURL: srv.URL}
	m, err := p.Complete(context.Background(), "test", []Message{{Role: "user", Content: "Inspect"}}, toolSpecs(), nil)
	if err != nil || m.Usage == nil || m.Usage.InputTokens != 1200 || m.Usage.OutputTokens != 140 || m.Usage.ReasoningTokens != 120 || m.Usage.CachedTokens != 900 || m.Usage.CacheWriteTokens != 100 {
		t.Fatal(m.Usage, err)
	}
	_, replay := responseInput([]Message{m})
	if len(replay) != 2 || !strings.Contains(jsonText(replay), `"encrypted_content":"opaque"`) || !strings.Contains(jsonText(replay), `"phase":"final_answer"`) || strings.Contains(jsonText(replay), "input_tokens") {
		t.Fatal("reasoning/phase not preserved or internal usage leaked", replay)
	}
	ctx := context.WithValue(context.Background(), reasoningEffortKey{}, c.CheckpointEffort)
	if _, err = p.Complete(ctx, "checkpoint-test", []Message{{Role: "user", Content: "Summarize"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	s := Search{Config: c.Search, Provider: p, Client: srv.Client()}
	result, err := s.Run(context.Background(), "test", "", "Termux")
	if err != nil || !strings.Contains(result, "https://termux.dev") || count != 3 {
		t.Fatal(result, count, err)
	}
}

func TestAstraConfigMigrationAndLimits(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	if c.ContextTokens != 272000 || c.Search.Default != "openai" || c.Search.OpenAIModel != "" {
		t.Fatal("incorrect defaults")
	}
	c.Model, c.ContextTokens, c.Search.OpenAIModel = "gpt-5.4", 32768, "gpt-5.4"
	legacy := map[string]any{}
	_ = json.Unmarshal([]byte(jsonText(c)), &legacy)
	for _, k := range []string{"reasoning_effort", "checkpoint_reasoning_effort", "verbosity", "model_timeout_seconds"} {
		delete(legacy, k)
	}
	writeJSON(filepath.Join(dir, "config.json"), legacy)
	migrated, err := LoadConfig(dir)
	if err != nil || migrated.Model != defaultModel || migrated.ContextTokens != 272000 || migrated.ModelTimeout != 600 || migrated.Search.OpenAIModel != "" {
		t.Fatal(migrated, err)
	}
	legacy["model"], legacy["context_tokens"] = "custom-model", 65536
	writeJSON(filepath.Join(dir, "config.json"), legacy)
	custom, err := LoadConfig(dir)
	if err != nil || custom.Model != "custom-model" || custom.ContextTokens != 65536 {
		t.Fatal("custom model/budget replaced", err)
	}
	c = DefaultConfig()
	c.ContextTokens = 872000
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.ContextTokens++
	if c.Validate() == nil {
		t.Fatal("backend limit ignored")
	}
	c = DefaultConfig()
	c.ReasoningEffort = "none"
	if c.Validate() == nil {
		t.Fatal("unsupported effort accepted")
	}
}

func TestCompactionStartsOnlyAbove95Percent(t *testing.T) {
	calls := 0
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls++
		if ctx.Value(reasoningEffortKey{}) != "low" || strings.Contains(jsonText(m), "OPAQUE_SECRET") {
			t.Error("checkpoint effort or transcript projection")
		}
		return Message{Role: "assistant", Content: "Continue the original request; earlier results remain in the archive."}, nil
	}))
	j := &runningJob{Job: Job{Session: "threshold"}, ctx: e.ctx}
	path := filepath.Join(e.Dir, "sessions", "threshold.json")
	const overhead = 2000
	threshold := e.Config.ContextTokens * 95 / 100
	history := []Message{{Role: "user", Content: "Keep the original objective"}, {Role: "assistant", Content: "Checked", Raw: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("OPAQUE_SECRET", 90000) + `"}`)}, Context: &contextSample{Model: defaultModel, InputTokens: threshold - 100, OutputTokens: 100, PrefixTokens: overhead}}}
	if _, err := e.compact(j, history, path, overhead); err != nil || calls != 0 {
		t.Fatal("compacted at/below threshold", calls, err)
	}
	history[1].Context.InputTokens++
	next, err := e.compact(j, history, path, overhead)
	if err != nil || calls != 1 || len(next) != 2 || next[0].Context != nil || next[1].Content != history[0].Content {
		t.Fatal("failed compaction above threshold", calls, len(next), err)
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "sessions", "threshold", "*.json"))
	if len(files) != 1 {
		t.Fatal("missing complete archive")
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "OPAQUE_SECRET") {
		t.Fatal("original reasoning lost from archive")
	}
}

func TestRuntimeContextAppendsAndDoesNotBecomeMemory(t *testing.T) {
	var firstSystem string
	var firstInput []any
	calls := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, msg []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		calls++
		system, input := responseInput(msg)
		if calls == 1 {
			firstSystem, firstInput = system, input
		} else if system != firstSystem || len(input) <= len(firstInput) || jsonText(input[:len(firstInput)]) != jsonText(firstInput) {
			t.Error("previous prefix was rewritten")
		}
		if strings.Contains(system, "<runtime_context>") || !strings.Contains(jsonText(input), "runtime_context") {
			t.Error("dynamic context misplaced")
		}
		return Message{Role: "assistant", Content: "Va bene.", Usage: &TokenUsage{InputTokens: 2000, OutputTokens: 100, ReasoningTokens: 80}}, nil
	}))
	for _, text := range []string{"Ciao", "Continua"} {
		j, err := e.Submit("local", "local", text)
		if err != nil {
			t.Fatal(err)
		}
		result := awaitStatus(t, e, j.ID, "completed")
		if result.Usage.InputTokens != 2000 {
			t.Fatal("missing job usage")
		}
	}
	if err := e.Memory.importTranscripts(); err != nil {
		t.Fatal(err)
	}
	var snapshots, originals int
	e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE content LIKE '%<runtime_context>%' ").Scan(&snapshots)
	e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE content='Ciao'").Scan(&originals)
	if snapshots != 0 || originals != 1 {
		t.Fatal("synthetic snapshots archived or user text changed", snapshots, originals)
	}
}

func TestEnglishSoulMigrationPreservesPersonalEdits(t *testing.T) {
	for _, original := range []string{legacyInitialSoul, "# Alina\nUna mia riflessione personale."} {
		dir := t.TempDir()
		writeText(filepath.Join(dir, "soul.md"), original)
		writeText(filepath.Join(dir, "soul.last.md"), original)
		m, err := OpenMemory(dir, DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		got, _ := m.Soul()
		want := original
		if original == legacyInitialSoul {
			want = initialSoul
			var previous string
			if err := m.DB.QueryRow("SELECT previous FROM soul_versions").Scan(&previous); err != nil || previous != original {
				t.Fatal("translation lost its revision history", err)
			}
		}
		if got != want {
			t.Fatal("seed translation or personal edit preservation failed")
		}
		m.DB.Close()
	}
}

func TestModelTimeoutDoesNotChangeOtherClients(t *testing.T) {
	c := DefaultConfig()
	client := newHTTPClient()
	p := &Provider{Config: c, Client: client}
	if p.modelClient().Timeout != 600*time.Second || client.Timeout != 180*time.Second {
		t.Fatal("model timeout leaked into other clients")
	}
	s := Search{Config: c.Search, Provider: &Provider{Config: c}}
	if _, err := s.Run(context.Background(), "test", "", "query"); err == nil {
		t.Fatal("missing login did not fail")
	}
}
