package alina

import (
	"encoding/json"
	"errors"
	"time"
)

func stateToolSpecs() []ToolSpec {
	s := func() map[string]any { return map[string]any{"type": "string"} }
	return []ToolSpec{
		{Name: "memory", Description: "Search all ages of memory; read a date/today/week/soul or a returned source ID, with next_offset pagination. Note facts, preferences, lessons or hypotheses; supersedes explicitly corrects a prior ID. Keep personal intentions distinct from user commitments: intentions lists them, intend creates/updates with a reason, next step and stopping condition. No secrets.", Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"search", "read", "note", "intentions", "intend"}}, "query": s(), "part": s(), "text": s(), "kind": s(), "supersedes": s(), "offset": map[string]any{"type": "integer", "minimum": 0}, "id": s(), "title": s(), "why": s(), "next": s(), "stop": s(), "status": map[string]any{"type": "string", "enum": []string{"active", "done", "dropped"}}}, "required": []string{"action"}}},
		{Name: "schedule", Description: "Manage agent tasks: list/add/pause/resume/remove. Use cron for user-requested recurring work or at (RFC3339) for one wake-up. Personal exploration MUST specify origin=self and an active intention_id; it is one-shot, runs quietly within configured scope and daily budget, and never changes permissions.", Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "add", "pause", "resume", "remove"}}, "id": s(), "name": s(), "cron": s(), "at": s(), "prompt": s(), "origin": map[string]any{"type": "string", "enum": []string{"user", "self"}}, "intention_id": s(), "catch_up": map[string]any{"type": "boolean"}}, "required": []string{"action"}}},
	}
}
func (e *Engine) stateTool(j *runningJob, c ToolCall) (string, error) {
	if c.Name == "memory" {
		if !e.Config.Memory.Enabled {
			return "", errors.New("memory disabled")
		}
		var a struct {
			Action, Query, Part, Text, Kind, Supersedes, ID, Title, Why, Next, Stop, Status string
			Offset                                                                          int
		}
		if err := json.Unmarshal([]byte(c.Arguments), &a); err != nil {
			return "", err
		}
		switch a.Action {
		case "search":
			r, err := e.Memory.Recall(j.ctx, a.Query)
			return jsonText(r), err
		case "read":
			r, err := e.Memory.ReadPage(j.ctx, a.Part, a.Offset, time.Now())
			return jsonText(r), err
		case "note":
			id, err := e.Memory.Note(j.ctx, time.Now(), j.Session, j.ID, a.Kind, a.Text, a.Supersedes)
			return jsonText(map[string]string{"id": id}), err
		case "intentions":
			intents, err := e.Memory.Intentions(j.ctx, false)
			return jsonText(intents), err
		case "intend":
			i, err := e.Memory.Intend(j.ctx, Intention{ID: a.ID, Title: a.Title, Why: a.Why, Next: a.Next, Stop: a.Stop, Status: a.Status})
			return jsonText(i), err

		}
		return "", errors.New("unknown memory action")
	}
	var a struct {
		Action, ID, Name, Cron, Prompt, At, Origin string
		IntentionID                                string `json:"intention_id"`
		CatchUp                                    bool   `json:"catch_up"`
	}
	if err := json.Unmarshal([]byte(c.Arguments), &a); err != nil {
		return "", err
	}
	switch a.Action {
	case "list":
		return formatTasks(e.Scheduler.List()), nil
	case "add":
		if (j.Kind == "initiative" || j.Kind == "dream") && a.Origin != "self" {
			return "", errors.New("personal exploration must retain origin=self")
		}
		var t ScheduledTask
		var err error
		if a.Origin == "self" || a.At != "" {
			t, err = e.Scheduler.AddOnce(a.Name, a.At, a.Prompt, j.Owner, a.Origin, a.IntentionID)
		} else {
			t, err = e.Scheduler.Add(a.Name, a.Cron, a.Prompt, j.Owner, a.CatchUp)
		}
		return jsonText(t), err
	case "pause", "resume", "remove":
		owner := j.Owner
		if a.Origin == "self" {
			owner = "alina"
		}
		if j.Kind == "initiative" || j.Kind == "dream" {
			owner = "alina"
		}
		err := e.Scheduler.Change(a.ID, a.Action, owner)
		return "Schedule updated.", err
	}
	return "", errors.New("unknown schedule action")
}

func (e *Engine) reflectionTool(j *runningJob, c ToolCall) (string, error) {
	if c.Name != "memory" && c.Name != "schedule" {
		return "", errors.New("reflection uses memory and personal wake-ups only; experiments belong in an initiative")
	}
	return e.stateTool(j, c)
}
