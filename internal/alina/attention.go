package alina

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const focusBytes = 6000
const pinnedBytes = 3000

// This is attention, not truth or retention. A month halves its weight; a
// deliberate recall renews it. No timer rewrites or deletes an old memory.
func attentionWeight(stamp string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return 0
	}
	return math.Exp2(-math.Max(0, now.Sub(t).Hours()) / (30 * 24))
}

func (m *Memory) FocusContext(ctx context.Context, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	text, err := m.focusText(ctx, now)
	if err == nil {
		if er := m.writeFocus(text); er != nil {
			m.Events.emit("memory.render_failed", er)
		}
	}
	return text, err
}

func (m *Memory) focusText(ctx context.Context, now time.Time) (string, error) {
	if !m.Config.Memory.Enabled {
		return "", nil
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT r.id,r.kind,r.text,COALESCE(a.touched,r.stamp),COALESCE(a.pinned,0)
 FROM current_recall r LEFT JOIN attention a ON a.id=r.id WHERE r.note=1
 ORDER BY COALESCE(a.pinned,0) DESC,julianday(COALESCE(a.touched,r.stamp)) DESC,r.id LIMIT 32`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, kind, text, touched string
		var pinned bool
		if err = rows.Scan(&id, &kind, &text, &touched, &pinned); err != nil {
			return "", err
		}
		label := fmt.Sprintf("attention %.2f", attentionWeight(touched, now))
		if pinned {
			label = "pinned"
		} else {
			text = truncate(text, 1000)
		}
		line := fmt.Sprintf("\n%s [%s; %s]: %s\n", id, kind, label, text)
		if b.Len()+len(line) > focusBytes {
			continue
		}
		b.WriteString(line)
	}
	return b.String(), rows.Err()
}

// Only deliberate operations focus notes. Search results, prompt assembly and
// scheduled reads of the focus view never reinforce their own selection.
func (m *Memory) Focus(ctx context.Context, id string, pin *bool, now time.Time) (err error) {
	defer func() {
		if err == nil {
			m.refreshFocus(now)
		}
	}()
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var text string
	err = tx.QueryRowContext(ctx, "SELECT text FROM current_recall WHERE id=? AND note=1", id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("focus requires a current note ID; save an observation as a note first")
	}
	if err != nil {
		return err
	}
	var pinned bool
	err = tx.QueryRowContext(ctx, "SELECT pinned FROM attention WHERE id=?", id).Scan(&pinned)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pin != nil {
		pinned = *pin
	}
	if pinned {
		var size int
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(r.text AS BLOB))+160),0) FROM current_recall r JOIN attention a ON a.id=r.id WHERE a.pinned=1 AND r.id!=?`, id).Scan(&size)
		if err != nil {
			return err
		}
		if size+len(text)+160 > pinnedBytes {
			return errors.New("pinned notes exceed 3000 bytes; unpin or shorten a note first")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attention VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET touched=excluded.touched,pinned=excluded.pinned`, id, now.UTC().Format(time.RFC3339Nano), pinned)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Memory) touchRead(ctx context.Context, id string, now time.Time) error {
	result, err := m.DB.ExecContext(ctx, `INSERT INTO attention(id,touched,pinned)
 SELECT id,?,0 FROM current_recall WHERE id=? AND note=1
 ON CONFLICT(id) DO UPDATE SET touched=excluded.touched`, now.UTC().Format(time.RFC3339Nano), id)
	if err == nil {
		if n, _ := result.RowsAffected(); n > 0 {
			m.refreshFocus(now)
		}
	}
	return err
}
