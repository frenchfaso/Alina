package alina

import (
	"context"
	"strings"
	"testing"
)

func TestSearchSelectionDefaultsAndOverrides(t *testing.T) {
	e, count, _, bodies, mu := controlEngine(t)
	p := e.Model.(*Provider)
	e.Search = &Search{Config: e.Config.Search, Provider: p, Client: p.Client}
	ctx := context.WithValue(e.ctx, reasoningEffortKey{}, "low")
	for _, kind := range []string{"chat", "delegate"} {
		j := &runningJob{Job: Job{ID: "search-" + kind, Session: kind, Kind: kind, Model: defaultModel, Reasoning: "low"}, ctx: ctx}
		if _, err := e.tool(j, ToolCall{Name: "web_search", Arguments: `{"query":"fixture"}`}); err != nil {
			t.Fatal(err)
		}
	}
	j := &runningJob{Job: Job{ID: "search-override", Kind: "chat"}, ctx: e.ctx}
	if _, err := e.tool(j, ToolCall{Name: "web_search", Arguments: `{"query":"fixture","model":"small-fixture","reasoning":"none"}`}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"query":"fixture","model":"hidden-fixture"}`, `{"query":"fixture","model":"small-fixture","reasoning":"medium"}`, `{"query":"fixture","provider":"brave","model":"gpt-6-astra"}`} {
		if _, err := e.tool(j, ToolCall{Name: "web_search", Arguments: raw}); err == nil {
			t.Fatalf("accepted invalid override %s", raw)
		}
	}
	j.Kind = "delegate"
	if _, err := e.tool(j, ToolCall{Name: "web_search", Arguments: `{"query":"fixture","model":"gpt-6-astra"}`}); err == nil {
		t.Fatal("delegate changed model")
	}
	if strings.Contains(jsonText(searchSpec(false)), `"model"`) {
		t.Fatal("delegate schema exposes overrides")
	}
	if count.Load() != 1 {
		t.Fatal("catalog fetched repeatedly", count.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*bodies) != 3 {
		t.Fatal(len(*bodies))
	}
	for n, b := range *bodies {
		model, effort := defaultSearchModel, defaultSearchEffort
		if n == 2 {
			model, effort = "small-fixture", "none"
		}
		if b["model"] != model || b["reasoning"].(map[string]any)["effort"] != effort {
			t.Fatal("selection mismatch", b)
		}
		if strings.Contains(jsonText(b["input"]), "soul") {
			t.Fatal("chat context leaked")
		}
	}
	if e.Config.Model != defaultModel || e.Search.openAIModel() != defaultSearchModel {
		t.Fatal("override changed defaults")
	}
}
