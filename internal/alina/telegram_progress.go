package alina

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

type telegramDraftKey struct {
	engine *Engine
	job    string
}
type telegramDraft struct {
	message    int64
	text, sent string
	next       time.Time
	retry      bool
}

// Share the delivery worker: a final reply cannot overtake an in-flight draft
// edit. SQLite keeps only the Telegram message ID for cleanup after restart.
// No polling or model calls while idle; changes wake the existing job signal.
func (t *Telegram) syncProgress(ctx context.Context, now time.Time) time.Duration {
	if t.drafts == nil {
		t.drafts = map[telegramDraftKey]*telegramDraft{}
		for _, e := range t.Engine.engines() {
			rows, err := e.Memory.DB.QueryContext(ctx, "SELECT key,value FROM memory_state WHERE key LIKE 'telegram-progress:%'")
			if err != nil {
				t.Engine.Events.emit("telegram.progress_failed", err)
				continue
			}
			for rows.Next() {
				var key, value string
				if err = rows.Scan(&key, &value); err != nil {
					break
				}
				id, er := strconv.ParseInt(value, 10, 64)
				if er == nil && id > 0 {
					t.drafts[telegramDraftKey{e, strings.TrimPrefix(key, "telegram-progress:")}] = &telegramDraft{message: id}
				}
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				t.Engine.Events.emit("telegram.progress_failed", err)
			}
		}
	}
	for _, e := range t.Engine.engines() {
		e.mu.Lock()
		for _, j := range e.jobs {
			if j.Status != "running" || j.commentary == "" {
				continue
			}
			key := telegramDraftKey{e, j.ID}
			if t.drafts[key] == nil {
				t.drafts[key] = &telegramDraft{next: now.Add(1500 * time.Millisecond)}
			}
			t.drafts[key].text = j.commentary
		}
		e.mu.Unlock()
	}
	var due time.Time
	for key, d := range t.drafts {
		j, exists := key.engine.Get(key.job)
		destination, chat, allowed := t.destination(j.Owner)
		if !exists || !allowed || destination != key.engine {
			delete(t.drafts, key)
			continue
		}
		active := j.Status == "running"
		if active && d.text == d.sent {
			continue
		}
		// Remove the draft on approval/termination even during the edit cooldown.
		if !active && !d.retry || !now.Before(d.next) {
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := t.updateProgress(check, key, d, chat, active)
			cancel()
			if err != nil {
				t.Engine.Events.emit("telegram.progress_failed", err, "job_id", key.job)
				d.next = now.Add(10 * time.Second)
				d.retry = true
			} else if !active {
				delete(t.drafts, key)
				continue
			} else {
				d.retry = false
				d.sent = d.text
				d.next = now.Add(3 * time.Second)
				continue
			}
		}
		if due.IsZero() || d.next.Before(due) {
			due = d.next
		}
	}
	if due.IsZero() {
		return 0
	}
	return max(time.Millisecond, time.Until(due))
}

func (t *Telegram) updateProgress(ctx context.Context, key telegramDraftKey, d *telegramDraft, chat int64, active bool) error {
	receipt := "telegram-progress:" + key.job
	if !active {
		if d.message > 0 {
			err := t.api(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": d.message}, nil)
			var rejected *telegramAPIError
			if err != nil && !(errors.As(err, &rejected) && (rejected.code == 400 || rejected.code == 403)) {
				return err
			}
		}
		_, err := key.engine.Memory.DB.ExecContext(ctx, "DELETE FROM memory_state WHERE key=?", receipt)
		return err
	}
	body := map[string]any{"chat_id": chat, "text": d.text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if d.message > 0 {
		body["message_id"] = d.message
		err := t.api(ctx, "editMessageText", body, nil)
		var rejected *telegramAPIError
		if errors.As(err, &rejected) && rejected.code == 400 {
			// Already edited after an ambiguous response, or removed by the
			// user. Do not recreate a draft they may have chosen to delete.
			err = nil
		}
		if err != nil {
			return err
		}
	} else {
		body["disable_notification"] = true
		var sent struct {
			ID int64 `json:"message_id"`
		}
		if err := t.api(ctx, "sendMessage", body, &sent); err != nil {
			return err
		}
		if sent.ID <= 0 {
			return errors.New("Telegram progress response has no message ID")
		}
		d.message = sent.ID
	}
	_, err := key.engine.Memory.DB.ExecContext(ctx, "INSERT OR REPLACE INTO memory_state VALUES(?,?)", receipt, strconv.FormatInt(d.message, 10))
	return err
}
