package alina

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

func (w *wizard) telegram(c *TelegramConfig) error {
	c.Enabled = w.yes("Abilitare il bot Telegram", c.Enabled)
	if !c.Enabled {
		return w.err
	}
	fmt.Fprintln(w.out, "\n1. Apri https://t.me/BotFather in Telegram.\n2. Invia /newbot e scegli nome e username del bot.\n3. Copia qui il token ricevuto. Usa un bot dedicato ad Alina.\nAlina accetta soltanto il proprietario in chat privata.")
	c.Token = w.secret("Token Telegram", c.Token)
	if w.yes("Verificare il bot e rilevare il tuo ID con un codice di associazione", true) {
		if w.err != nil {
			return w.err
		}
		tg := NewTelegram("", *c, nil, newHTTPClient())
		var me struct {
			Username string `json:"username"`
		}
		if err := tg.api(w.ctx, "getMe", nil, &me); err != nil {
			return fmt.Errorf("Telegram token verification failed: %w", err)
		}
		code := "alina-" + randomID()
		fmt.Fprintf(w.out, "Bot verificato: @%s\nApri https://t.me/%s?start=%s e premi Avvia.\nAttendo fino a 2 minuti; nessun messaggio viene inviato dal setup.\n", me.Username, me.Username, code)
		ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
		defer cancel()
		owner, err := pairTelegram(ctx, tg, code)
		if err != nil {
			return err
		}
		c.OwnerID = owner
		fmt.Fprintln(w.out, "Proprietario associato:", owner)
	} else {
		id := w.ask("Il tuo Telegram user ID numerico (oppure rilancia setup per l'associazione guidata)", strconv.FormatInt(c.OwnerID, 10))
		var err error
		c.OwnerID, err = strconv.ParseInt(id, 10, 64)
		if err != nil {
			return errors.New("invalid Telegram user ID")
		}
	}
	return w.err
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
				return m.From.ID, nil
			}
		}
	}
	return 0, ctx.Err()
}
