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
	if latest <= last && (completed > 0 || !e.Config.Autonomy.Enabled || len(intents) == 0) {
		return "Nothing new to reflect on.", nil
	}
	// Keep the Job's immutable submitted input intact while using a runtime cue.
	cue := fmt.Sprintf(`Take a quiet moment to reflect as Alina. Consider recent experience, uncertainty, your methods and open personal intentions. There is no requirement to discover a lesson, write a memory, or change your soul. Leaving things unchanged is a valid outcome.
The shared archive is available through memory search/read. New events begin at memory read part="after-%d" (paged); the recent-context view is only an excerpt. Investigate evidence when it matters; do not mistake a fluent interpretation or a tool request for a verified result. Save a useful lesson or correct a note only when warranted. Read or focus a relevant note to bring it back into attention. A reflection is an interpretation, not a new observation.
Your soul is a short personal orientation, not a diary or a biography of the user. You may use the soul tool to revise it, supplying the exact previous text and a reason. Personal experiments belong in a one-shot initiative within the configured autonomy budget. Finish with a short, natural reflection.`, last)
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
	return result, tx.Commit()
}

func reflectionSpecs() []ToolSpec {
	return append(stateToolSpecs(), ToolSpec{Name: "soul", Description: "Revise your short personal orientation only when experience warrants it. Supply the exact previous text to preserve concurrent edits. No change is also valid.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"previous": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"previous", "text", "reason"},
	}})
}

func (m *Memory) reviseSoul(ctx context.Context, now time.Time, previous, next, reason string) error {
	if !validSoul(next) || strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return errors.New("soul requires at most 180 words / 1600 bytes and a short reason")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := filepath.Join(m.Dir, "soul.md")
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
	return writeText(path, next)
}

func (e *Engine) reflectionTool(j *runningJob, c ToolCall) (string, error) {
	if c.Name == "soul" {
		var a struct{ Previous, Text, Reason string }
		if err := json.Unmarshal([]byte(c.Arguments), &a); err != nil {
			return "", err
		}
		err := e.Memory.reviseSoul(j.ctx, time.Now(), a.Previous, a.Text, a.Reason)
		return "Soul updated.", err
	}
	if c.Name != "memory" && c.Name != "schedule" {
		return "", errors.New("reflection uses memory, soul and personal wake-ups only; experiments belong in an initiative")
	}
	return e.stateTool(j, c)
}
