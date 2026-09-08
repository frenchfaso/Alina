package alina

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (w *wizard) telegram(c *TelegramConfig) error {
	if !w.yes("Abilitare il bot Telegram", c.Enabled) {
		c.Enabled = false
		return w.err
	}
	return w.connectTelegram(c, newHTTPClient(), true)
}

func (w *wizard) connectTelegram(c *TelegramConfig, client *http.Client, edit bool) error {
	old := *c
	if edit {
		fmt.Fprintln(w.out, "Telegram · crea un bot dedicato: https://t.me/BotFather → /newbot.")
	}
	for {
		if edit || c.Token == "" {
			c.Token = w.secret("Token Telegram", c.Token)
		}
		if w.err != nil {
			return w.err
		}
		if c.Token == "" {
			c.Enabled, c.OwnerID = false, 0
			return nil
		}
		tg := NewTelegram("", *c, nil, client)
		var me struct {
			Username string `json:"username"`
		}
		err := tg.api(w.ctx, "getMe", nil, &me)
		if err == nil && me.Username == "" {
			err = errors.New("Telegram bot username missing")
		}
		if err == nil {
			var webhook struct {
				URL string `json:"url"`
			}
			err = tg.api(w.ctx, "getWebhookInfo", nil, &webhook)
			if err == nil && webhook.URL != "" {
				err = errors.New("questo bot usa un webhook; usa un bot dedicato ad Alina")
			}
		}
		if err == nil && (c.Token != old.Token || c.OwnerID <= 0 || !old.Enabled) {
			code := "alina-" + randomID()
			fmt.Fprintf(w.out, "Apri https://t.me/%s?start=%s e premi Avvia (entro 2 minuti).\n", me.Username, code)
			ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
			c.OwnerID, err = pairTelegram(ctx, tg, code)
			cancel()
			if err == nil {
				c.Binding = randomID()
			}
		}
		if err == nil {
			c.Enabled = true
			fmt.Fprintf(w.out, "Telegram collegato · @%s\n", me.Username)
			return nil
		}
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		fmt.Fprintln(w.out, "Telegram non collegato:", err)
		switch w.choice("1 riprova · 2 cambia token · 3 configura dopo", "1", "1", "2", "3") {
		case "1":
			edit = false
		case "2":
			edit = true
		case "3":
			*c = old
			// Keep the previous credentials for a later retry, but do not
			// claim that an unverified integration is ready.
			c.Enabled = false
			return nil
		}
		if w.err != nil {
			return w.err
		}
	}
}

func pairTelegram(ctx context.Context, tg *Telegram, code string) (int64, error) {
	var offset int64
	for ctx.Err() == nil {
		var updates []tgUpdate
		if err := tg.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 10, "allowed_updates": []string{"message"}}, &updates); err != nil {
			return 0, fmt.Errorf("Telegram pairing failed (use a dedicated bot without another poller or webhook): %w", err)
		}
		for _, u := range updates {
			offset = u.ID + 1
			m := u.Message
			if m != nil && m.From.ID > 0 && m.From.ID == m.Chat.ID && m.Chat.Type == "private" && m.Text == "/start "+code {
				// Confirm only through the pairing message, leaving later
				// messages available to the daemon on its first poll.
				if err := tg.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 0, "allowed_updates": []string{"message", "callback_query"}}, nil); err != nil {
					return 0, err
				}
				return m.From.ID, nil
			}
		}
	}
	return 0, ctx.Err()
}
