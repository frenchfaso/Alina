package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dream is an ordinary bounded reflection turn, not an archival pipeline.
// New concurrent events remain eligible for the next reflection.
func (e *Engine) dream(j *runningJob, now time.Time) (string, error) {
	if e != e.global {
		return "", errors.New("reflection belongs to the global mind; request dream without a user selector")
	}
	m := e.Memory
	if !m.Config.Memory.Enabled {
		return "", errors.New("memory is disabled")
	}
	var last, latest int64
	var value string
	err := m.DB.QueryRowContext(j.ctx, "SELECT value FROM memory_state WHERE key='reflected-through'").Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil {
		last, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return "", err
		}
	}
	overview, latest, err := m.dreamOverview(j.ctx, last, 4000)
	if err != nil {
		return "", err
	}
	intents, err := m.Intentions(j.ctx, true)
	if err != nil {
		return "", err
	}
	day := now.In(m.loc).Format("2006-01-02")
	var completed int
	if err = m.DB.QueryRowContext(j.ctx, "SELECT count(*) FROM dreams WHERE day=?", day).Scan(&completed); err != nil {
		return "", err
	}
	scopeCue, scopeCursors, scopeChanged, err := e.dreamScopes(j.ctx)
	if err != nil {
		return "", err
	}
	if latest <= last && !scopeChanged && (completed > 0 || !e.Config.Autonomy.Enabled || len(intents) == 0) {
		e.mu.Lock()
		j.Skipped = true
		e.mu.Unlock()
		return "Nothing new to reflect on.", nil
	}
	// Keep the Job's immutable submitted input intact while using a runtime cue.
	cue := dreamPrompt + "\n\n" + overview + scopeCue
	result, err := e.turn(j, cue)
	if err != nil {
		return result, err
	}
	tx, err := m.DB.BeginTx(j.ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(j.ctx, `INSERT INTO memory_state VALUES('reflected-through',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(latest, 10)); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(j.ctx, `INSERT INTO dreams VALUES(?,?) ON CONFLICT(day) DO UPDATE SET completed=excluded.completed`, day, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	if scopeCursors != nil {
		if _, err = tx.ExecContext(j.ctx, `INSERT INTO memory_state VALUES('reflected-scopes',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, jsonText(scopeCursors)); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

// A deterministic view, not another model pass. Each represented event has a
// source/job reference; verbose tool arguments/results stay in the archive.
// The existing cursor advances only through the represented batch, on success.
func (m *Memory) dreamOverview(ctx context.Context, after int64, budget int) (string, int64, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT rowid,id,stamp,session,job,role,content FROM journal
 WHERE rowid>? AND role IN ('user','assistant') ORDER BY rowid`, after)
	if err != nil {
		return "", after, err
	}
	defer rows.Close()
	var b strings.Builder
	through := after
	for rows.Next() {
		var row int64
		var entry MemoryEntry
		if err = rows.Scan(&row, &entry.ID, &entry.Time, &entry.Session, &entry.Job, &entry.Role, &entry.Content); err != nil {
			return "", after, err
		}
		text, calls, _ := strings.Cut(entry.Content, "\nTool request: ")
		entry.Content = truncate(text, 700)
		if len(text) > len(entry.Content) {
			entry.Content += " [excerpt; read source for full text]"
		}
		if calls != "" {
			for _, call := range strings.Split(calls, "\nTool request: ") {
				name, _, _ := strings.Cut(call, " ")
				entry.Content += "\nTool: " + truncate(name, 60) + " (details: read job)"
			}
		}
		line := entryText(entry)
		if b.Len()+len(line) > budget {
			b.WriteString("\nMore experiences remain for the next dream.\n")
			break
		}
		b.WriteString(line)
		through = row
	}
	return b.String(), through, rows.Err()
}

func reflectionSpecs() []ToolSpec {
	return append(stateToolSpecs(), imageToolSpec(), harnessSpec(), ToolSpec{Name: "soul", Description: "Revise your short personal orientation in English only when experience warrants it. Supply the exact previous text to preserve concurrent edits. No change is also valid.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"previous": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"previous", "text", "reason"},
	}})
}

func (m *Memory) reviseSoul(ctx context.Context, now time.Time, previous, next, reason string) error {
	if m.soulOwner != nil {
		return errors.New("only global reflection can revise the soul")
	}
	if !validSoul(next) || strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return errors.New("soul requires at most 180 words / 1600 bytes and a short reason")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := filepath.Join(m.SoulDir, "soul.md")
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(old) != previous {
		return errors.New("soul changed; read it again before revising")
	}
	next = m.redact(next)
	if next == previous {
		return nil
	}
	if _, err = m.DB.ExecContext(ctx, "INSERT INTO soul_versions VALUES(?,?,?,?,?)", randomID(), now.UTC().Format(time.RFC3339Nano), previous, next, m.redact(reason)); err != nil {
		return err
	}
	if err = writeText(path, next); err != nil {
		return err
	}
	// The revision has committed. Remember it immediately, including before
	// another prompt reads the file; backup failure does not undo that change.
	m.goodSoul = next
	if err = writeText(filepath.Join(m.SoulDir, "soul.last.md"), next); err != nil {
		m.Events.emit("soul.checkpoint_failed", err)
	}
	return nil
}

func (e *Engine) reflectionTool(j *runningJob, c ToolCall) (string, error) {
	if c.Name == "harness" {
		return e.harnessTool(j, c.Arguments)
	}
	if c.Name == "view_image" {
		if e == e.global && len(e.scopes) > 0 {
			var a struct{ Path string }
			if err := json.Unmarshal([]byte(c.Arguments), &a); err != nil {
				return "", err
			}
			path, err := canonicalFilePath(a.Path)
			if err != nil {
				return "", err
			}
			for _, scope := range e.engines()[1:] {
				workspace, err := canonicalFilePath(scope.Workspace())
				if err == nil && within(workspace, path) {
					return scope.tool(j, c)
				}
			}
		}
		return e.tool(j, c)
	}
	if c.Name == "soul" {
		if e != e.global {
			return "", errors.New("only global reflection can revise the soul")
		}
		var a struct{ Previous, Text, Reason string }
		if err := json.Unmarshal([]byte(c.Arguments), &a); err != nil {
			return "", err
		}
		err := e.Memory.reviseSoul(j.ctx, time.Now(), a.Previous, a.Text, a.Reason)
		return "Soul updated.", err
	}
	if c.Name != "memory" && c.Name != "schedule" {
		return "", errors.New("reflection uses memory, image inspection, soul and personal wake-ups only; experiments belong in an initiative")
	}
	return e.stateTool(j, c)
}

// Snapshot each archive independently. A concurrent message remains beyond its
// captured cursor and will be eligible for the next global dream.
func (e *Engine) dreamScopes(ctx context.Context) (string, map[string]int64, bool, error) {
	if len(e.scopes) == 0 {
		return "", nil, false, nil
	}
	previous := map[string]int64{}
	var raw string
	err := e.Memory.DB.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key='reflected-scopes'").Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &previous)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, err
	}
	current := map[string]int64{}
	cue := "\nNew family experiences (source IDs retrieve full entries; job IDs retrieve tool details):\n"
	changed := false
	for _, engine := range e.engines()[1:] {
		text, latest, err := engine.Memory.dreamOverview(ctx, previous[engine.Scope], max(1600, 16000/len(e.scopes)))
		if err != nil {
			return "", nil, false, err
		}
		current[engine.Scope] = latest
		if latest > previous[engine.Scope] {
			changed = true
			cue += fmt.Sprintf("\nscope=%s\n%s", jsonText(engine.Scope), text)
		}
	}
	if len(e.scopes) > 1 {
		cue += "Legacy multiple-family installation: use memory scope for each family's notes; keep confidences within their source scope."
	}
	return cue, current, changed, nil
}

// Operational evidence only; no prompt, transcript or private error details.
func (e *Engine) dreamStatus() map[string]any {
	out := map[string]any{"enabled": e.Config.Memory.Enabled && e.Config.Memory.Dream}
	for _, task := range e.Scheduler.List() {
		if task.Kind == "dream" && task.Enabled {
			out["next"] = task.Next
		}
	}
	var raw string
	err := e.Memory.DB.QueryRow("SELECT payload FROM jobs WHERE json_extract(payload,'$.kind')='dream' ORDER BY created DESC,id LIMIT 1").Scan(&raw)
	if err == nil {
		var j Job
		if err = json.Unmarshal([]byte(raw), &j); err == nil {
			outcome := j.Status
			if outcome == "completed" && j.Skipped {
				outcome = "skipped"
			}
			attempt := map[string]any{"job_id": j.ID, "started": j.Created, "outcome": outcome}
			if j.Error != "" {
				attempt["error"] = errorInfo(errors.New(j.Error))
			}
			out["last_attempt"] = attempt
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		out["read_error"] = errorInfo(err)
	}
	var completed string
	err = e.Memory.DB.QueryRow("SELECT completed FROM dreams ORDER BY completed DESC LIMIT 1").Scan(&completed)
	if err == nil {
		out["last_completed"] = completed
	} else if !errors.Is(err, sql.ErrNoRows) {
		out["read_error"] = errorInfo(err)
	}
	return out
}
