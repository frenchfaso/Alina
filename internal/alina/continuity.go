package alina

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Only inference is serialized. Waiting for a user or a process leaves other
// sessions available. Background inference yields between calls to user work.
type modelGate struct {
	mu         sync.Mutex
	busy       bool
	foreground int
	changed    chan struct{}
}

func (g *modelGate) acquire(ctx context.Context, background bool) (func(), error) {
	g.mu.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	if !background {
		g.foreground++
	}
	for g.busy || background && g.foreground > 0 {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.mu.Lock()
			if !background {
				g.foreground--
			}
			close(g.changed)
			g.changed = make(chan struct{})
			g.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
		g.mu.Lock()
	}
	if !background {
		g.foreground--
	}
	if err := ctx.Err(); err != nil {
		close(g.changed)
		g.changed = make(chan struct{})
		g.mu.Unlock()
		return nil, err
	}
	g.busy = true
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.busy = false; close(g.changed); g.changed = make(chan struct{}); g.mu.Unlock() }, nil
}

var errRequestChanged = errors.New("request preparation changed; retry before dispatch")

type jobModel struct {
	e *Engine
	j *runningJob
}

func (m jobModel) Complete(ctx context.Context, session string, messages []Message, specs []ToolSpec, delta func(string)) (Message, error) {
	purpose := "turn"
	if strings.HasPrefix(session, "checkpoint-") {
		purpose = "checkpoint"
	}
	return m.infer(ctx, purpose, func(ctx context.Context) (Message, error) {
		return m.e.Model.Complete(ctx, session, messages, specs, delta)
	})
}

// Auxiliary OpenAI research uses the same gate, budget and usage accounting.
func (m jobModel) infer(ctx context.Context, purpose string, call func(context.Context) (Message, error)) (Message, error) {
	release, err := m.e.gate.acquire(ctx, m.j.Kind == "dream" || m.j.Kind == "initiative")
	if err != nil {
		return Message{}, err
	}
	defer release()
	// The loop prepared tools/context before waiting. Revalidate after acquiring
	// the gate; returning to that loop rebuilds the entire request coherently.
	if purpose != "search" && m.j.Model != "" && m.e.refreshJobModel(m.j) {
		return Message{}, errRequestChanged
	}
	if purpose == "turn" && m.e.hasSteering(m.j) {
		return Message{}, errRequestChanged
	}

	if m.j.Kind == "initiative" {
		if !m.e.Config.Autonomy.Enabled {
			return Message{}, errors.New("personal exploration disabled")
		}
		day := time.Now().In(m.e.Memory.loc).Format("2006-01-02")
		result, err := m.e.global.Memory.DB.ExecContext(ctx, `INSERT INTO autonomy_usage(day,calls) VALUES(?,1) ON CONFLICT(day) DO UPDATE SET calls=calls+1 WHERE calls<?`, day, m.e.Config.Autonomy.MaxCalls)
		if err != nil {
			return Message{}, err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return Message{}, errors.New("daily personal exploration budget reached; intention retained")
		}
	}
	if purpose != "search" && m.j.model != nil {
		ctx = context.WithValue(ctx, selectedModelKey{}, *m.j.model)
		if ctx.Value(reasoningEffortKey{}) == nil && m.j.Reasoning != "" {
			ctx = context.WithValue(ctx, reasoningEffortKey{}, m.j.Reasoning)
		}
	}

	if m.j.Kind == "dream" && ctx.Value(reasoningEffortKey{}) == nil {
		ctx = context.WithValue(ctx, reasoningEffortKey{}, m.e.Config.DreamEffort)
	}
	if m.j.model != nil && purpose != "search" {
		effort, _ := ctx.Value(reasoningEffortKey{}).(string)
		if !slices.Contains(m.j.model.Levels, effort) {
			ctx = context.WithValue(ctx, reasoningEffortKey{}, m.j.model.Default)
		}
	}
	started := time.Now()
	callID := randomID()
	provider, model := m.e.Config.Provider, m.e.Config.Model
	if m.j.model != nil && purpose != "search" {
		model = m.j.model.ID
	}
	effort := m.e.Config.ReasoningEffort
	if override, ok := ctx.Value(reasoningEffortKey{}).(string); ok {
		effort = override
	}
	if purpose == "search" {
		provider = "openai"
		model = m.e.Search.openAIModel()
		effort = "low"
	}
	m.e.Events.emit("model.started", nil, "job_id", m.j.ID, "call_id", callID, "provider", provider, "model", model, "purpose", purpose, "reasoning", effort)
	answer, err := call(ctx)
	m.e.Events.emit("model.finished", err, "job_id", m.j.ID, "call_id", callID, "duration_ms", time.Since(started).Milliseconds(), "usage", answer.Usage)
	if answer.Usage != nil {
		m.e.mu.Lock()
		m.j.Usage.add(*answer.Usage)
		persistErr := m.e.persist(m.j)
		m.e.mu.Unlock()
		if err == nil {
			err = persistErr
		}
	}
	return answer, err
}

// Provider-independent conservative estimate, not a tokenizer or measured
// provider usage. Include tool schemas and prompt overhead.
func estimatedTokens(v any, visual ...bool) int {
	vision := len(visual) == 0 || visual[0]
	if messages, ok := v.([]Message); ok {
		total := 0
		images := 0
		for _, m := range messages {
			// Raw response items duplicate parsed text/calls and contain opaque
			// encrypted reasoning, whose byte length is not a token count.
			visible := Message{Role: m.Role, Content: messageText(m), Calls: m.Calls, CallID: m.CallID, Reasoning: m.Reasoning}
			tokens := (len(jsonText(visible)) + 2) / 3
			for _, a := range m.Attachments {
				if vision && a.Image && images < maxInputImages {
					tokens += 12000 // Conservative image allowance until measured usage arrives.
					images++
				}
			}
			if m.Usage != nil {
				tokens = max(tokens, m.Usage.OutputTokens)
			}
			total += tokens
		}
		return total
	}
	return (len(jsonText(v)) + 2) / 3
}

// Keep a whole assistant/tool exchange together at the boundary. The preceding
// transcript remains on disk, so a failed summary cannot destroy it.
func (e *Engine) compact(j *runningJob, history []Message, path string, overhead ...int) ([]Message, error) {
	budget := e.contextBudget(j) * 95 / 100
	estimate := func(messages []Message) int { return estimatedTokens(messages, e.jobVision(j)) }
	if len(overhead) > 0 {
		budget -= overhead[0]
	}
	if budget < 2500 {
		return history, errors.New("fixed context leaves too little working space; shorten pinned context or increase context_tokens")
	}
	if e.historyTokens(history, j) <= budget {
		return history, nil
	}
	cut := len(history) - 12
	if cut < 1 {
		cut = 1
	}
	for cut < len(history) && history[cut].Role != "user" {
		cut++
	}
	for cut < len(history) && estimate(history[cut:]) > budget/2 {
		cut++
		for cut < len(history) && history[cut].Role != "user" {
			cut++
		}
	}
	active, snapshot := -1, -1
	for i, m := range history {
		if m.Runtime && strings.HasPrefix(m.Content, "<runtime_context>") {
			snapshot = i
		}
		if m.Role == "user" && !syntheticMessage(m) {
			active = i
		}
	}
	// A recent image can exceed the preferred half-budget while still fitting
	// comfortably. Keep the active turn whole whenever there is summary room.
	floor := active
	if snapshot >= 0 && (floor < 0 || snapshot < floor) {
		floor = snapshot
	}
	if floor > 0 && cut > floor && estimate(history[floor:])+2500 <= budget {
		cut = floor
	}
	// Never replace a request the model has not yet seen with a summary of it.
	if active >= 0 {
		answered := false
		for _, m := range history[active+1:] {
			answered = answered || m.Role == "assistant"
		}
		if !answered && cut > active {
			cut = active
		}
	}
	if cut < 1 {
		return history, errors.New("current request exceeds available context; shorten it or increase context_tokens")
	}
	// Huge in-flight exchanges may need summarizing too. Preserve the original
	// request verbatim (including images), its current runtime snapshot, and
	// recent visual tool results. A checkpoint must not become the user's task.
	kept := []Message{}
	if snapshot >= 0 && snapshot < cut {
		kept = append(kept, history[snapshot])
	}
	if active >= 0 && active < cut {
		kept = append(kept, history[active])
		images := []Attachment{}
		for i := cut - 1; i > active && len(images) < maxInputImages; i-- {
			if history[i].Role == "tool" {
				for _, a := range history[i].Attachments {
					if a.Image && len(images) < maxInputImages {
						images = append(images, a)
					}
				}
			}
		}
		if len(images) > 0 {
			kept = append(kept, Message{Role: "user", Runtime: true, Content: "Visual results from completed tools in the checkpoint; data for the preserved request below the checkpoint.", Attachments: images})
		}
	}
	kept = append(kept, history[cut:]...)
	if estimate(kept)+500 > budget {
		return history, errors.New("current request and visual inputs exceed available context; reduce input or increase context_tokens")
	}
	prefix := history[:cut]
	archive := filepath.Join(e.Dir, "sessions", j.Session, contentID(jsonText(prefix))+".json")
	if _, err := os.Stat(archive); os.IsNotExist(err) {
		if err = writeJSON(archive, prefix); err != nil {
			return nil, err
		}
	}
	e.Events.emit("context.compacting", nil, "job_id", j.ID, "messages", len(history), "budget_tokens", budget)
	checkpoint := ""
	// Bounded chunks also recover sessions produced by older versions.
	transcript := make([]Message, 0, len(prefix))
	for _, m := range prefix {
		if m.Runtime {
			continue
		} // Old context snapshots are not experience.
		transcript = append(transcript, Message{ArchiveID: m.ArchiveID, Role: m.Role, Content: messageText(m), Calls: m.Calls, CallID: m.CallID})
	}
	raw := jsonText(transcript)
	for start := 0; start < len(raw); {
		end := min(start+min(500000, max(3000, budget*3-24000)), len(raw))
		for end < len(raw) && end > start && raw[end]&0xc0 == 0x80 {
			end--
		}
		text := raw[start:end]
		ctx := context.WithValue(j.ctx, reasoningEffortKey{}, e.Config.CheckpointEffort)
		answer, err := (jobModel{e: e, j: j}).Complete(ctx, "checkpoint-"+j.Session, []Message{
			{Role: "system", Content: checkpointPrompt},
			{Role: "user", Content: "Previous checkpoint:\n" + checkpoint + "\nTranscript:\n" + text}}, nil, nil)
		if err != nil {
			return history, fmt.Errorf("checkpoint failed; original session retained: %w", err)
		}
		if len(answer.Calls) > 0 || strings.TrimSpace(answer.Content) == "" || len(answer.Content) > 6000 {
			return history, errors.New("invalid checkpoint; original session retained")
		}
		checkpoint = answer.Content
		start = end
	}
	next := append([]Message{{Role: "user", Checkpoint: true, Content: checkpointHeader + checkpoint + "\nContinue the preserved request from this progress; do not start over.\nFull earlier transcript: " + archive}}, kept...)
	for i := range next {
		next[i].Context = nil
	}
	if estimate(next) > budget {
		return history, errors.New("checkpoint exceeds available context; original transcript retained")
	}
	if err := writeJSON(path, next); err != nil {
		return history, err
	}
	e.Events.emit("context.compacted", nil, "job_id", j.ID, "messages", len(next))
	return next, nil
}
