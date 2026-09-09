package alina

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Chat, scheduled work, personal initiatives and reflection all use this loop.
// Their differences are the initiating message, tools and execution budget.
func (e *Engine) turn(j *runningJob, cue ...string) (string, error) {
	e.refreshJobModel(j)
	path := filepath.Join(e.Dir, "sessions", j.Session+".json")
	history := []Message{}
	if b, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(b, &history); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	occurrences := map[string]int{}
	for i := range history {
		role, text := messageContent(history[i])
		text = e.Memory.redact(text)
		fingerprint := contentID(role + "\n" + text)
		n := occurrences[fingerprint]
		occurrences[fingerprint]++
		if syntheticMessage(history[i]) || history[i].ArchiveID != "" {
			continue
		}
		err := e.Memory.DB.QueryRowContext(j.ctx, `SELECT id FROM journal WHERE session=? AND content=? AND (role=? OR (?='tool:result' AND role LIKE 'tool:%')) ORDER BY rowid LIMIT 1 OFFSET ?`, j.Session, text, role, role, n).Scan(&history[i].ArchiveID)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		history[i].ArchiveID = "legacy-" + contentID(j.Session+fingerprint+fmt.Sprint(n))
		if err = e.Memory.recordMessage(j.ctx, time.Time{}, Job{Session: j.Session, ID: "legacy-import"}, &history[i]); err != nil {
			return "", err
		}
	}

	answered := map[string]bool{}
	for _, m := range history {
		if m.Role == "tool" {
			answered[m.CallID] = true
		}
	}
	var repairs []Message
	for _, m := range history {
		for _, c := range m.Calls {
			if !answered[c.ID] {
				repairs = append(repairs, Message{Role: "tool", CallID: c.ID, Content: "Interrupted before result was recorded; do not assume execution succeeded."})
				answered[c.ID] = true
			}
		}
	}
	appendMessage := func(msg Message) error {
		if !syntheticMessage(msg) {
			if err := e.Memory.recordMessage(j.ctx, time.Now(), Job{Session: j.Session, ID: j.ID, Kind: j.Kind, Owner: j.Owner}, &msg); err != nil {
				return err
			}
		}
		if msg.Role == "tool" && len(msg.Content) > 48<<10 {
			msg.Content = truncate(msg.Content, 48<<10) + "\nFull recorded result: memory read part=" + msg.ArchiveID
		}
		history = append(history, msg)
		return writeJSON(path, history)
	}
	for _, msg := range repairs {
		if err := appendMessage(msg); err != nil {
			return "", err
		}
	}
	input := j.Input
	if len(cue) > 0 {
		input = cue[0]
	}
	specs, maxSteps := e.toolsFor(j), e.Config.MaxSteps
	if j.Kind == "dream" {
		maxSteps = 6
	}
	prompt := e.prompt(specs)
	context, err := e.runtimeContext(j)
	if err != nil {
		return "", err
	}
	prefix := []Message{{Role: "system", Content: prompt}}
	// Append changing context instead of rewriting the prefix on every turn.
	// Runtime snapshots are working context, not new observations in memory.
	if err = appendMessage(Message{Role: "user", Content: context, Runtime: true}); err != nil {
		return "", err
	}
	if err = appendMessage(Message{Role: "user", Content: input, Attachments: j.Attachments}); err != nil {
		return "", err
	}
	vision := e.jobVision(j)
	for step := 0; step < maxSteps; step++ {
		if err = j.ctx.Err(); err != nil {
			return "", err
		}
		if err = e.drainSteering(j, &history, appendMessage); err != nil {
			return "", err
		}
		e.refreshJobModel(j)
		if vision != e.jobVision(j) {
			vision = e.jobVision(j)
			specs = e.toolsFor(j)
			prefix[0].Content = e.prompt(specs)
		}
		e.activity(j, fmt.Sprintf("Model · step %d", step+1))
		history, err = e.compact(j, history, path, estimatedTokens(prefix)+estimatedTokens(specs))
		if errors.Is(err, errRequestChanged) {
			step--
			continue
		}
		if err != nil {
			return "", err
		}
		// Compaction can take time. Incorporate newly arrived corrections and
		// recheck the budget before issuing a model request with stale intent.
		if e.hasSteering(j) {
			step--
			continue
		}
		msg, err := (jobModel{e: e, j: j}).Complete(j.ctx, j.Session, append(append([]Message{}, prefix...), history...), specs, nil)
		if errors.Is(err, errRequestChanged) {
			step--
			continue
		}
		if err != nil {
			return "", err
		}
		if err = validateAssistant(msg); err != nil {
			return "", err
		}
		if msg.Usage != nil {
			msg.Context = &contextSample{Model: func() string {
				if j.Model != "" {
					return j.Model
				}
				return e.Config.Model
			}(), InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens, PrefixTokens: estimatedTokens(prefix) + estimatedTokens(specs), VisionDisabled: !e.jobVision(j)}
		}
		if len(msg.Calls) > 8 {
			return "", errors.New("model requested more than eight tools in one step")
		}
		if err = appendMessage(msg); err != nil {
			return "", err
		}
		if len(msg.Calls) == 0 {
			if e.closeMailbox(j) {
				return msg.Content, nil
			}
			continue
		}
		for _, call := range msg.Calls {
			if err = j.ctx.Err(); err != nil {
				return "", err
			}
			started := time.Now()
			toolName := call.Name
			if !hasTool(specs, toolName) {
				toolName = "unknown"
			}
			traceID := randomID()
			e.Events.emit("tool.started", nil, "job_id", j.ID, "call_id", traceID, "tool", toolName)
			e.activity(j, toolName)
			var result string
			var toolErr error
			if e.hasSteering(j) {
				toolErr = errors.New("not executed: new user steering is pending; reconsider after reading it")
			} else if !hasTool(specs, call.Name) {
				toolErr = fmt.Errorf("tool %q is not available in this turn", call.Name)
			} else if j.Kind == "dream" {
				result, toolErr = e.reflectionTool(j, call)
			} else {
				result, toolErr = e.tool(j, call)
			}
			attrs := []any{"job_id", j.ID, "call_id", traceID, "tool", toolName, "duration_ms", time.Since(started).Milliseconds()}
			logErr := toolErr
			if call.Name == "shell" {
				var exitCode int
				if _, err := fmt.Sscanf(result, "exit_code: %d", &exitCode); err == nil {
					attrs = append(attrs, "exit_code", exitCode)
					if exitCode != 0 && logErr == nil {
						logErr = &shellExitError{code: exitCode}
					}
				}
			}
			e.Events.emit("tool.finished", logErr, attrs...)
			if toolErr != nil {
				result = "ERROR: " + toolErr.Error()
			}
			response := Message{Role: "tool", CallID: call.ID, Content: result}
			if call.Name == "view_image" && toolErr == nil {
				var a Attachment
				if err = json.Unmarshal([]byte(result), &a); err != nil {
					return "", err
				}
				response.Content = "Image loaded for visual inspection."
				response.Attachments = []Attachment{a}
			}
			if err = appendMessage(response); err != nil {
				return "", err
			}
		}
	}
	return "", errors.New("step budget reached; progress saved; use POST /v1/jobs/{id}/resume through alina api to continue")
}
