package alina

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (e *Engine) Workspace() string { return filepath.Join(e.Dir, "workspace") }
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func (e *Engine) initWorkspace() error {
	for _, dir := range []string{"notes", "experiments", "procedures"} {
		if err := os.MkdirAll(filepath.Join(e.Workspace(), dir), 0700); err != nil {
			return err
		}
	}
	path := filepath.Join(e.Workspace(), "procedures", "index.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return writeText(path, "# Procedures\n\nKeep reusable scripts and short instructions here. For each capability, record its purpose, path, inputs, last actual verification and known limitations. Mark experiments as unverified until tested. Update this index when a procedure changes.\n")
	}
	return nil
}

type Intention struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Why     string `json:"why"`
	Next    string `json:"next"`
	Stop    string `json:"stop"`
	Status  string `json:"status"`
	Updated string `json:"updated"`
}

func (m *Memory) Intentions(ctx context.Context, active bool) ([]Intention, error) {
	query := "SELECT id,title,why,next,stop,status,updated FROM intentions"
	if active {
		query += " WHERE status='active'"
	}
	query += " ORDER BY updated DESC LIMIT 50"
	rows, err := m.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Intention{}
	for rows.Next() {
		var i Intention
		if err := rows.Scan(&i.ID, &i.Title, &i.Why, &i.Next, &i.Stop, &i.Status, &i.Updated); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (m *Memory) Intend(ctx context.Context, i Intention) (Intention, error) {
	if i.ID == "" {
		i.ID = randomID()
	}
	if i.Status == "" {
		i.Status = "active"
	}
	if !safeID(i.ID) || strings.TrimSpace(i.Title) == "" || strings.TrimSpace(i.Why) == "" || strings.TrimSpace(i.Next) == "" || strings.TrimSpace(i.Stop) == "" || len(i.Title) > 160 || len(i.Why) > 800 || len(i.Next) > 800 || len(i.Stop) > 800 {
		return i, errors.New("intention requires title (160 bytes), why/next/stop (800 bytes each), and a valid ID")
	}
	if i.Status != "active" && i.Status != "done" && i.Status != "dropped" {
		return i, errors.New("intention status must be active, done or dropped")
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return i, err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM intentions WHERE status='active' AND id!=?", i.ID).Scan(&active); err != nil {
		return i, err
	}
	if i.Status == "active" && active >= 8 {
		return i, errors.New("keep at most eight active intentions; resolve or drop one first")
	}
	i.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	i.Title = m.redact(i.Title)
	i.Why = m.redact(i.Why)
	i.Next = m.redact(i.Next)
	i.Stop = m.redact(i.Stop)
	_, err = tx.ExecContext(ctx, `INSERT INTO intentions VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,why=excluded.why,next=excluded.next,stop=excluded.stop,status=excluded.status,updated=excluded.updated`, i.ID, i.Title, i.Why, i.Next, i.Stop, i.Status, i.Updated)
	if err != nil {
		return i, err
	}
	return i, tx.Commit()
}
func (m *Memory) Intention(ctx context.Context, id string) (Intention, error) {
	var i Intention
	err := m.DB.QueryRowContext(ctx, "SELECT id,title,why,next,stop,status,updated FROM intentions WHERE id=?", id).Scan(&i.ID, &i.Title, &i.Why, &i.Next, &i.Stop, &i.Status, &i.Updated)
	return i, err
}
func (m *Memory) Note(ctx context.Context, now time.Time, session, job, kind, text, supersedes string) (string, error) {
	if kind == "" {
		kind = "fact"
	}
	if kind != "fact" && kind != "preference" && kind != "lesson" && kind != "hypothesis" {
		return "", errors.New("note kind must be fact/preference/lesson/hypothesis")
	}
	if strings.TrimSpace(text) == "" || len(text) > 4000 {
		return "", errors.New("note requires 1-4000 bytes")
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if supersedes != "" {
		var id string
		err = tx.QueryRowContext(ctx, `SELECT id FROM (SELECT id FROM journal UNION ALL SELECT id FROM facts UNION ALL SELECT id FROM memories UNION ALL SELECT id FROM sources) WHERE id=? LIMIT 1`, supersedes).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("correction target does not exist")
		}
		if err != nil {
			return "", err
		}
	}
	id := randomID()
	day := now.In(m.loc).Format("2006-01-02")
	stamp := now.Format(time.RFC3339Nano)
	text = m.redact(text)
	_, err = tx.ExecContext(ctx, "INSERT INTO facts VALUES(?,?,?,?,?,?,?,?)", id, day, stamp, kind, text, supersedes, session, job)
	if err != nil {
		return "", err
	}
	content := text
	if supersedes != "" {
		content = "Correction superseding " + supersedes + ": " + text
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO journal VALUES(?,?,?,?,?,?,?)", id, day, stamp, session, job, "note:"+kind, content)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.dirty = true
	m.mu.Unlock()
	return id, nil
}
