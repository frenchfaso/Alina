package alina

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (w *wizard) telegram(config *Config) error {
	c := &config.Telegram
	if !w.yes("Abilitare il bot Telegram", c.Enabled) {
		c.Enabled = false
		return w.err
	}
	client := newHTTPClient()
	if err := w.connectTelegram(config, client, true); err != nil {
		return err
	}
	if c.Enabled {
		return w.people(config, client, true)
	}
	return nil
}

func (w *wizard) connectTelegram(config *Config, client *http.Client, edit bool) error {
	c := &config.Telegram
	if c.OwnerID <= 0 && len(config.Users) > 0 {
		c.OwnerID = config.localUser().TelegramID
	}
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
		if c.Token != old.Token || c.Binding == "" && c.OwnerID <= 0 {
			c.Binding = randomID()
		}
		tg := NewTelegram(w.dir, *c, nil, client)
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
		if err == nil && (c.Token != old.Token || c.OwnerID <= 0) {
			code := "alina-" + randomID()
			fmt.Fprintf(w.out, "Apri https://t.me/%s?start=%s e premi Avvia (entro 2 minuti).\n", me.Username, code)
			ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
			c.OwnerID, err = pairTelegram(ctx, tg, code, config.Users)
			cancel()

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
			// A failed health check must not disable an existing integration.
			if old.Enabled {
				fmt.Fprintln(w.out, "Configurazione Telegram mantenuta; connessione non verificata.")
			}
			return nil
		}
		if w.err != nil {
			return w.err
		}
	}
}

// Pairing also consumes the bot's update stream. Preserve messages from already
// authorized people before acknowledging them, so adding a person cannot lose work.
func pairTelegram(ctx context.Context, tg *Telegram, code string, people ...[]User) (int64, error) {
	offset := tg.state.Offset
	known := map[int64]bool{tg.Config.OwnerID: tg.Config.OwnerID > 0}
	if len(people) > 0 {
		for _, u := range people[0] {
			known[u.TelegramID] = true
		}
	}
	for ctx.Err() == nil {
		var updates []tgUpdate
		if err := tg.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 10, "allowed_updates": telegramUpdates}, &updates); err != nil {
			return 0, fmt.Errorf("Telegram pairing failed (use a dedicated bot without another poller or webhook): %w", err)
		}
		var paired int64
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			offset = u.ID + 1
			m := u.Message
			if paired == 0 && m != nil && m.From.ID > 0 && m.From.ID == m.Chat.ID && m.Chat.Type == "private" && m.Text == "/start "+code {
				paired = m.From.ID
				known[paired] = true
				continue
			}
			if actor := u.privateActor(); actor > 0 && known[actor] {
				if len(tg.state.Pending) >= 1000 {
					return 0, errors.New("pairing inbox full; start Alina to process saved messages first")
				}
				if len(people) > 0 {
					for _, person := range people[0] {
						if person.TelegramID == actor {
							u.Scope = person.scope()
							break
						}
					}
				}
				tg.state.Pending = append(tg.state.Pending, u)
			}
		}
		tg.state.Offset = offset
		if tg.Dir != "" {
			if err := tg.saveLocked(); err != nil {
				return 0, err
			}
		}
		if paired != 0 {
			if err := tg.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 0, "allowed_updates": telegramUpdates}, nil); err != nil {
				return 0, err
			}
			return paired, nil
		}
	}
	return 0, ctx.Err()
}
