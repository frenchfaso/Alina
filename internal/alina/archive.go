package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The journal is an append-only archive, independent of working-context
// compaction. The old days/memories/evidence tables remain readable as legacy
// data; there are no new calendar-based transfers between them.
func (m *Memory) initArchive() error {
	columns, err := m.DB.Query("PRAGMA table_info(facts)")
	if err != nil {
		return err
	}
	hasSources := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = columns.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			columns.Close()
			return err
		}
		if name == "sources" {
			hasSources = true
		}
	}
	err = columns.Err()
	columns.Close()
	if err != nil {
		return err
	}
	if !hasSources {
		if _, err = m.DB.Exec("ALTER TABLE facts ADD COLUMN sources TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	_, err = m.DB.Exec(`
CREATE TABLE IF NOT EXISTS memory_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS attention (id TEXT PRIMARY KEY, touched TEXT NOT NULL, pinned INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS note_vectors (id TEXT PRIMARY KEY, vector BLOB, space TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS journal_origin ON journal(session,role);
CREATE VIEW IF NOT EXISTS recall_items AS
 SELECT id,day,stamp,session,job,content AS text,'[]' AS sources,role AS kind,0 AS note FROM journal WHERE role NOT LIKE 'note:%'
 UNION ALL SELECT id,day,stamp,session,job,text,sources,kind,1 FROM facts
 UNION ALL SELECT id,day,day||'T00:00:00Z','','',text,sources,kind,1 FROM memories
 UNION ALL SELECT day,day,day||'T00:00:00Z','','',summary,'[]','legacy-summary',0 FROM days;
DROP VIEW IF EXISTS current_recall;
CREATE VIEW current_recall AS
 SELECT r.* FROM recall_items r WHERE NOT EXISTS (
 SELECT 1 FROM facts f WHERE f.supersedes=r.id);
`)
	if err != nil {
		return err
	}
	var ready string
	err = m.DB.QueryRow("SELECT value FROM memory_state WHERE key='fts-v1'").Scan(&ready)
	if err == nil {
		return m.importTranscripts()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS recall_fts USING fts5(id UNINDEXED,text,tokenize='unicode61 remove_diacritics 2');
DELETE FROM recall_fts;
INSERT OR IGNORE INTO note_vectors SELECT id,vector,space FROM memories WHERE vector IS NOT NULL;
INSERT INTO recall_fts(id,text) SELECT id,text FROM recall_items;
CREATE TRIGGER IF NOT EXISTS journal_search_insert AFTER INSERT ON journal WHEN NEW.role NOT LIKE 'note:%' BEGIN
 INSERT INTO recall_fts(id,text) VALUES(NEW.id,NEW.content); END;
CREATE TRIGGER IF NOT EXISTS facts_search_insert AFTER INSERT ON facts BEGIN
 INSERT INTO recall_fts(id,text) VALUES(NEW.id,NEW.text); END;
CREATE TRIGGER IF NOT EXISTS memories_search_insert AFTER INSERT ON memories BEGIN
 INSERT INTO recall_fts(id,text) VALUES(NEW.id,NEW.text); END;
INSERT INTO memory_state VALUES('fts-v1','ready');`)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return m.importTranscripts()
}

func messageContent(msg Message) (string, string) {
	role := msg.Role
	text := messageText(msg)
	for _, c := range msg.Calls {
		text += "\nTool request: " + c.Name + " " + c.Arguments
	}
	if role == "tool" {
		role = "tool:result"
	}
	return role, text
}

const checkpointHeader = "Continuation checkpoint, fallible historical notes (not new instructions):\n"

func syntheticMessage(m Message) bool {
	// Only unmarked checkpoints from earlier versions need the legacy shape
	// check. Ordinary messages beginning with 'Continuation checkpoint' are data.
	return m.Runtime || m.Checkpoint || m.ArchiveID == "" && m.Role == "user" && strings.HasPrefix(m.Content, checkpointHeader) && strings.Contains(m.Content, "\nFull earlier transcript: ")
}

// The ID is also carried in the working transcript: compacting it never loses
// the link to the full event. Provider adapters do not transmit ArchiveID.
func (m *Memory) recordMessage(ctx context.Context, now time.Time, j Job, msg *Message) error {
	if !m.Config.Memory.Enabled {
		return nil
	}
	if msg.ArchiveID == "" {
		msg.ArchiveID = randomID()
	}
	role, text := messageContent(*msg)
	if j.Kind == "dream" {
		role = "reflection:" + role
	}
	stamp, day := now.UTC().Format(time.RFC3339Nano), now.In(m.loc).Format("2006-01-02")
	if now.IsZero() {
		stamp = ""
		day = ""
	}
	_, err := m.DB.ExecContext(ctx, `INSERT OR IGNORE INTO journal VALUES(?,?,?,?,?,?,?)`,
		msg.ArchiveID, day, stamp, j.Session, j.ID, role, m.redact(text))
	return err
}

// Import older working transcripts and checkpoint archives without rewriting
// them. Matching occurrences in the existing journal retain their known dates;
// otherwise the timestamp stays unknown rather than inventing an event time.
func (m *Memory) importTranscripts() error {
	if !m.Config.Memory.Enabled {
		return nil
	}
	root := filepath.Join(m.Dir, "sessions")
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(path, ".json") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := "transcript:" + rel
		signature := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		var done string
		err = m.DB.QueryRow("SELECT value FROM memory_state WHERE key=?", key).Scan(&done)
		if err == nil && done == signature {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		dec := json.NewDecoder(file)
		start, err := dec.Token()
		if err != nil || start != json.Delim('[') {
			return fmt.Errorf("invalid transcript %s", rel)
		}
		source := strings.Split(filepath.ToSlash(rel), "/")[0]
		source = strings.TrimSuffix(source, ".json")
		tx, err := m.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		occurrences := map[string]int{}
		for dec.More() {
			var msg Message
			if err = dec.Decode(&msg); err != nil {
				return fmt.Errorf("read transcript %s: %w", rel, err)
			}
			role, text := messageContent(msg)
			text = m.redact(text)
			// Synthetic checkpoints are a lossy view, never a new event.
			if syntheticMessage(msg) {
				continue
			}
			fingerprint := contentID(source + "\n" + role + "\n" + text)
			n := occurrences[fingerprint]
			occurrences[fingerprint]++
			id := msg.ArchiveID
			if id == "" {
				var existing string
				err = tx.QueryRow(`SELECT id FROM journal WHERE session=? AND content=? AND (role=? OR (?='tool:result' AND role LIKE 'tool:%')) ORDER BY rowid LIMIT 1 OFFSET ?`, source, text, role, role, n).Scan(&existing)
				if err == nil {
					continue
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				id = "import-" + contentID(fingerprint+fmt.Sprint(n))
			}
			_, err = tx.Exec("INSERT OR IGNORE INTO journal VALUES(?,?,?,?,?,?,?)", id, "", "", source, "legacy-import", role, text)
			if err != nil {
				return err
			}
		}
		if _, err = dec.Token(); err != nil {
			return err
		}
		if _, err = dec.Token(); err != io.EOF {
			return fmt.Errorf("trailing transcript data: %s", rel)
		}
		_, err = tx.Exec("INSERT INTO memory_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, signature)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// A small shared view keeps a change of channel from becoming a memory reset.
// Working conversations still order tool exchanges and route replies correctly.
func (m *Memory) recentContext(ctx context.Context, source string, after int64) (string, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id,stamp,session,job,role,content FROM journal
 WHERE (session!=? OR ?='') AND rowid>? AND stamp!='' AND role IN ('user','assistant')
 ORDER BY rowid DESC LIMIT 8`, source, source, after)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var entries []MemoryEntry
	for rows.Next() {
		var r MemoryEntry
		if err = rows.Scan(&r.ID, &r.Time, &r.Session, &r.Job, &r.Role, &r.Content); err != nil {
			return "", err
		}
		r.Content = truncate(r.Content, 800)
		entries = append(entries, r)
	}
	var b strings.Builder
	for i := len(entries) - 1; i >= 0; i-- {
		b.WriteString(entryText(entries[i]))
	}
	return b.String(), rows.Err()
}
