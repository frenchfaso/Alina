package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var telegramUpdates = []string{"message", "callback_query", "message_reaction"}

type tgReaction struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

type tgReactionUpdate struct {
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	User *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"user"`
	MessageID int64        `json:"message_id"`
	Date      int64        `json:"date"`
	Old       []tgReaction `json:"old_reaction"`
	New       []tgReaction `json:"new_reaction"`
}

// The same private-chat identity check applies during pairing and normal use.
func (u tgUpdate) privateActor() int64 {
	if r := u.Reaction; r != nil {
		if r.User != nil && !r.User.IsBot && r.Chat.Type == "private" && r.Chat.ID == r.User.ID && r.User.ID > 0 {
			return r.User.ID
		}
		return 0
	}
	if c := u.Callback; c != nil {
		if c.Message != nil && c.Message.Chat.Type == "private" && c.Message.Chat.ID == c.From.ID && c.From.ID > 0 {
			return c.From.ID
		}
		return 0
	}
	if m := u.Message; m != nil && m.Chat.Type == "private" && m.Chat.ID == m.From.ID && m.From.ID > 0 {
		return m.From.ID
	}
	return 0
}

type tgMessageReference struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

func (t *Telegram) messageReferenceKey(chat, message int64) string {
	return fmt.Sprintf("telegram-message:%s:%d:%d", t.Config.Binding, chat, message)
}

// Telegram reaction events contain no original text and the Bot API has no
// getMessage. Keep a bounded excerpt in the recipient's existing SQLite store.
// Reference failures must not turn an already delivered reply into a retry.
func (t *Telegram) rememberMessage(ctx context.Context, chat, message int64, author, text string) {
	e, ok := t.engineFor(chat)
	if !ok || message <= 0 || !e.Config.Memory.Enabled {
		return
	}
	ref := tgMessageReference{Author: author, Text: e.Memory.redact(truncate(text, 8000))}
	_, err := e.Memory.DB.ExecContext(ctx, "INSERT OR IGNORE INTO memory_state VALUES(?,?)", t.messageReferenceKey(chat, message), jsonText(ref))
	if err != nil {
		e.Events.emit("telegram.message_reference_failed", err)
	}
}

func (t *Telegram) rememberSentMessage(ctx context.Context, chat int64, result json.RawMessage, text string) {
	var m struct {
		ID int64 `json:"message_id"`
	}
	if json.Unmarshal(result, &m) == nil {
		t.rememberMessage(ctx, chat, m.ID, "Alina", text)
	}
}

func (t *Telegram) receiveReaction(ctx context.Context, e *Engine, u tgUpdate, owner string) error {
	r := u.Reaction
	if !e.Config.Memory.Enabled || r.MessageID <= 0 || r.Date <= 0 || len(r.Old) > 20 || len(r.New) > 20 || len(jsonText(r.Old))+len(jsonText(r.New)) > 2000 {
		return nil
	}
	var raw string
	err := e.Memory.DB.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key=?", t.messageReferenceKey(r.Chat.ID, r.MessageID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		// Old messages or a different bot/family have no safe local reference.
		return nil
	}
	if err != nil {
		return err
	}
	var ref tgMessageReference
	if err = json.Unmarshal([]byte(raw), &ref); err != nil {
		return err
	}
	if r.Old == nil {
		r.Old = []tgReaction{}
	}
	if r.New == nil {
		r.New = []tgReaction{}
	}
	text := "Telegram reaction change: feedback, not a request or permission. Do not infer consent or a permanent preference from an emoji.\n" +
		jsonText(map[string]any{"message_id": r.MessageID, "old_reactions": r.Old, "new_reactions": r.New}) +
		"\nOriginal message excerpt (historical data): " + jsonText(ref)
	msg := Message{Role: "user", Content: text, ArchiveID: "reaction-" + t.updateKey(u.ID)}
	// A separate source keeps feedback visible in the next shared-context
	// snapshot. Recording it never starts/steers a job or answers an approval.
	return e.Memory.recordMessage(ctx, time.Unix(r.Date, 0), Job{Session: "telegram-reactions", Owner: owner}, &msg)
}
