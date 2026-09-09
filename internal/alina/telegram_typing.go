package alina

import (
	"context"
	"errors"
	"time"
)

// One idle-free worker; transient typing failures never fail a user's turn.
func (t *Telegram) typing(ctx context.Context) {
	next := map[int64]time.Time{}
	for ctx.Err() == nil {
		changed := t.Engine.jobChanged.watch()
		active := map[int64]bool{}
		for _, u := range t.recipients() {
			e, ok := t.engineFor(u.TelegramID)
			if !ok {
				continue
			}
			e.mu.Lock()
			for _, j := range e.jobs {
				if (j.Status == "queued" || j.Status == "running") && (j.Kind == "chat" || j.Kind == "") && j.Owner == telegramOwner(t.Config, u.TelegramID) {
					active[u.TelegramID] = true
					break
				}
			}
			e.mu.Unlock()
		}
		for id := range next {
			if !active[id] {
				delete(next, id)
			}
		}
		for id := range active {
			if time.Now().Before(next[id]) {
				continue
			}
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := t.api(check, "sendChatAction", map[string]any{"chat_id": id, "action": "typing"}, nil)
			cancel()
			if ctx.Err() != nil {
				return
			}
			delay := 4 * time.Second
			if err != nil {
				// A transient mobile-network failure should not hide a whole
				// turn. Keep the longer cooldown for API rejection/rate limits.
				var apiErr *telegramAPIError
				if errors.As(err, &apiErr) && apiErr.code >= 400 && apiErr.code < 500 {
					delay = 30 * time.Second
				}
				t.Engine.Events.emit("telegram.typing_failed", err)
			}
			next[id] = time.Now().Add(delay)
		}
		var wake <-chan time.Time
		var timer *time.Timer
		if len(next) > 0 {
			var earliest time.Time
			for _, at := range next {
				if earliest.IsZero() || at.Before(earliest) {
					earliest = at
				}
			}
			timer = time.NewTimer(max(0, time.Until(earliest)))
			wake = timer.C
		}
		select {
		case <-ctx.Done():
		case <-changed:
		case <-wake:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
