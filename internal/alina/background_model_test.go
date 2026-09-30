package alina

import (
	"context"
	"testing"
)

func TestBackgroundModelsUseCachedCapabilitiesWithoutPersonalPreferences(t *testing.T) {
	for _, tc := range []struct {
		id, dream string
		levels    []string
	}{
		{"gpt-6-sol", "xhigh", []string{"low", "medium", "high", "xhigh"}},
		{"background-fixture", "high", []string{"low", "medium", "high"}},
		{"text-fixture", "", []string{}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			e, requests, _, bodies, mu := controlEngine(t)
			c, err := e.models(e.ctx, true)
			if err != nil {
				t.Fatal(err)
			}
			fallback := "high"
			if len(tc.levels) == 0 {
				fallback = ""
			}
			c.Models = append(c.Models, catalogModel{ID: tc.id, Levels: tc.levels, Default: fallback, Context: 32768})
			e.catalog.mu.Lock()
			e.catalog.cached = c
			e.catalog.mu.Unlock()
			if err = e.setModelPreference(e.ctx, "telegram:1", "model", "small-fixture", c); err != nil {
				t.Fatal(err)
			}
			if err = e.setModelPreference(e.ctx, "telegram:1", "think", "none", c); err != nil {
				t.Fatal(err)
			}
			e.Config.Model = tc.id
			e.Config.ReasoningEffort = "low"
			e.Model.(*Provider).Config = e.Config
			j := &runningJob{Job: Job{ID: "background-dream", Kind: "dream", Owner: "telegram:1"}, ctx: e.ctx}
			if !e.refreshJobModel(j) || j.Model != tc.id || j.Reasoning != tc.dream || j.model == nil || e.jobVision(j) {
				t.Fatal("background selection used personal preferences or lost metadata", j.Job)
			}
			if e.refreshJobModel(j) {
				t.Fatal("unchanged selection invalidated the request")
			}
			model := jobModel{e: e, j: j}
			messages := []Message{{Role: "user", Content: "Reflect briefly."}}
			if _, err = model.Complete(j.ctx, "dream", messages, nil, nil); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(j.ctx, reasoningEffortKey{}, e.Config.CheckpointEffort)
			if _, err = model.Complete(ctx, "checkpoint-dream", messages, nil, nil); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatal("background calls refreshed the catalog", requests.Load())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(*bodies) != 2 {
				t.Fatal("unexpected request count", len(*bodies))
			}
			for i, body := range *bodies {
				want := tc.dream
				if i == 1 && len(tc.levels) > 0 {
					want = "medium"
				}
				reasoning, _ := body["reasoning"].(map[string]any)
				if body["model"] != tc.id || want != "" && reasoning["effort"] != want || want == "" && body["reasoning"] != nil {
					t.Fatal("wrong model/effort on the wire", body["model"], body["reasoning"])
				}
			}
			initiative := &runningJob{Job: Job{Kind: "initiative", Owner: "telegram:1"}, ctx: e.ctx}
			e.refreshJobModel(initiative)
			if initiative.Model != tc.id || len(tc.levels) > 0 && initiative.Reasoning != "low" {
				t.Fatal("initiative inherited personal selection", initiative.Job)
			}
		})
	}
}
