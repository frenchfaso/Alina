package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type telegramApprovalForm struct {
	Job, Approval, Owner string
	Message              int64
}

func (t *Telegram) sendApproval(ctx context.Context, chat int64, e *Engine, j Job, text string, keyboard any) error {
	key := "telegram-approval:" + j.Approval.ID
	var raw string
	err := e.Memory.DB.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key=?", key).Scan(&raw)
	if err == nil {
		var form telegramApprovalForm
		if json.Unmarshal([]byte(raw), &form) != nil || form.Job != j.ID || form.Approval != j.Approval.ID || form.Owner != j.Owner || form.Message <= 0 {
			return errors.New("invalid Telegram approval form receipt")
		}
		return nil // Sent before a crash or failed outer delivery checkpoint.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id, err := t.sendChunksID(ctx, chat, splitTelegramText(text, nil), keyboard)
	if err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("Telegram approval response has no message ID")
	}
	form := telegramApprovalForm{Job: j.ID, Approval: j.Approval.ID, Owner: j.Owner, Message: id}
	_, err = e.Memory.DB.ExecContext(ctx, "INSERT INTO memory_state VALUES(?,?)", key, jsonText(form))
	return err
}

// Reuse the notification worker and job wakeups. Persisted IDs also let the
// next daemon remove forms for interrupted jobs; cleanup never blocks replies.
func (t *Telegram) dismissApprovalForms(ctx context.Context) error {
	var result error
	for _, e := range t.Engine.engines() {
		rows, err := e.Memory.DB.QueryContext(ctx, "SELECT value FROM memory_state WHERE key LIKE 'telegram-approval:%'")
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		var forms []telegramApprovalForm
		for rows.Next() {
			var raw string
			var form telegramApprovalForm
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal([]byte(raw), &form)
			}
			if err != nil {
				break
			}
			forms = append(forms, form)
		}
		result = errors.Join(result, err, rows.Err(), rows.Close())
		for _, form := range forms {
			destination, chat, allowed := t.destination(form.Owner)
			if !allowed || destination != e {
				continue // Do not route an old bot/family's forms to a new one.
			}
			j, exists := e.Get(form.Job)
			if exists && j.Status == "approval" && j.PendingSteering == 0 && j.Approval != nil && j.Approval.ID == form.Approval && time.Now().Before(j.Approval.Expires) {
				continue
			}
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = t.deleteMessage(check, chat, form.Message)
			cancel()
			if err == nil {
				_, err = e.Memory.DB.ExecContext(ctx, "DELETE FROM memory_state WHERE key=?", "telegram-approval:"+form.Approval)
			}
			if err != nil {
				e.Events.emit("telegram.approval_cleanup_failed", err, "job_id", form.Job)
				result = errors.Join(result, err)
			}
		}
	}
	return result
}

func (t *Telegram) deleteMessage(ctx context.Context, chat, message int64) error {
	err := t.api(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": message}, nil)
	var rejected *telegramAPIError
	if errors.As(err, &rejected) && (rejected.code == 400 || rejected.code == 403) {
		return nil // Already removed, or Telegram no longer permits deletion.
	}
	return err
}
