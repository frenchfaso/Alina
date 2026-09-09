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
	if err = m.DB.QueryRowContext(j.ctx, `SELECT COALESCE(max(rowid),0) FROM journal WHERE role IN ('user','assistant')`).Scan(&latest); err != nil {
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
		return "Nothing new to reflect on.", nil
	}
	// Keep the Job's immutable submitted input intact while using a runtime cue.
	cue := fmt.Sprintf(dreamPrompt, last) + scopeCue
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
	if _, err = tx.ExecContext(j.ctx, `INSERT INTO dreams VALUES(?,?) ON CONFLICT(day) DO UPDATE SET completed=excluded.completed`, day, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	if scopeCursors != nil {
		if _, err = tx.ExecContext(j.ctx, `INSERT INTO memory_state VALUES('reflected-scopes',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, jsonText(scopeCursors)); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

func reflectionSpecs() []ToolSpec {
	return append(stateToolSpecs(), imageToolSpec(), ToolSpec{Name: "soul", Description: "Revise your short personal orientation in English only when experience warrants it. Supply the exact previous text to preserve concurrent edits. No change is also valid.", Parameters: map[string]any{
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
	cue := "\nExperiences across people and families (scope is a native sharing boundary, not a Telegram group):\n"
	changed := false
	for _, engine := range e.engines()[1:] {
		var latest int64
		if err := engine.Memory.DB.QueryRowContext(ctx, `SELECT COALESCE(max(rowid),0) FROM journal WHERE role IN ('user','assistant')`).Scan(&latest); err != nil {
			return "", nil, false, err
		}
		current[engine.Scope] = latest
		if latest > previous[engine.Scope] {
			changed = true
			cue += fmt.Sprintf("scope=%s; read part=after-%d; snapshot through row %d.\n", jsonText(engine.Scope), previous[engine.Scope], latest)
		}
	}
	cue += "Use memory scope to read or update that family's notes. Keep personal details in their original scope. Global reflection is private; the shared soul expresses general values and methods, never private facts or identifiable stories. Learn across experiences without transferring confidences between families."
	return cue, current, changed, nil
}
