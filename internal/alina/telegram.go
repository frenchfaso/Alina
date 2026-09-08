package alina

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramState struct {
	Offset    int64             `json:"offset"`
	Sessions  map[string]string `json:"sessions"`
	Delivered map[string]string `json:"delivered"`
}
type Telegram struct {
	Config   TelegramConfig
	Engine   *Engine
	Client   *http.Client
	BaseURL  string
	Dir      string
	mu       sync.Mutex
	state    telegramState
	stateErr error
}
type tgMessage struct {
	ID   int64  `json:"message_id"`
	Text string `json:"text"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}
type tgUpdate struct {
	ID       int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID, Data string
		From     struct {
			ID int64 `json:"id"`
		}
		Message *tgMessage
	} `json:"callback_query"`
}

func NewTelegram(dir string, c TelegramConfig, e *Engine, client *http.Client) *Telegram {
	t := &Telegram{Dir: dir, Config: c, Engine: e, Client: client, state: telegramState{Sessions: map[string]string{}, Delivered: map[string]string{}}}
	b, er := os.ReadFile(filepath.Join(dir, "telegram.json"))
	if er == nil {
		t.stateErr = json.Unmarshal(b, &t.state)
	} else if !os.IsNotExist(er) {
		t.stateErr = er
	}
	if t.state.Sessions == nil {
		t.state.Sessions = map[string]string{}
	}
	if t.state.Delivered == nil {
		t.state.Delivered = map[string]string{}
	}
	return t
}
func (t *Telegram) endpoint(method string) string {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	return base + "/bot" + t.Config.Token + "/" + method
}
func (t *Telegram) api(ctx context.Context, method string, body, out any) error {
	var envelope struct {
		OK        bool            `json:"ok"`
		Result    json.RawMessage `json:"result"`
		ErrorCode int             `json:"error_code"`
	}
	if e := requestJSON(ctx, t.Client, "POST", t.endpoint(method), body, nil, &envelope); e != nil {
		return e
	}
	if !envelope.OK {
		return fmt.Errorf("Telegram API error %d", envelope.ErrorCode)
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}
func (t *Telegram) send(ctx context.Context, text string, keyboard any) error {
	runes := []rune(text)
	if len(runes) == 0 {
		runes = []rune("(nessun testo)")
	}
	for len(runes) > 0 {
		n := len(runes)
		if n > 3500 {
			n = 3500
		}
		body := map[string]any{"chat_id": t.Config.OwnerID, "text": string(runes[:n]), "link_preview_options": map[string]bool{"is_disabled": true}}
		if keyboard != nil && n == len(runes) {
			body["reply_markup"] = keyboard
		}
		if e := t.api(ctx, "sendMessage", body, nil); e != nil {
			return e
		}
		runes = runes[n:]
	}
	return nil
}
func (t *Telegram) saveLocked() error {
	return writeJSON(filepath.Join(t.Dir, "telegram.json"), t.state)
}
func (t *Telegram) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if t.stateErr != nil {
		log.Print("Telegram stopped: invalid persisted state")
		return
	}
	done := make(chan struct{})
	go func() { defer close(done); t.notify(ctx) }()
	defer func() { cancel(); <-done }()
	for ctx.Err() == nil {
		t.mu.Lock()
		offset := t.state.Offset
		t.mu.Unlock()
		var updates []tgUpdate
		e := t.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if e != nil {
			if ctx.Err() != nil {
				return
			}
			log.Print("Telegram polling failed; retrying in 5 seconds")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if e = t.process(ctx, u); e != nil {
				log.Print("Telegram update failed; will retry")
				break
			}
			t.mu.Lock()
			t.state.Offset = u.ID + 1
			e = t.saveLocked()
			t.mu.Unlock()
			if e != nil {
				log.Print("Telegram state write failed")
				return
			}
		}
	}
}
func (t *Telegram) process(ctx context.Context, u tgUpdate) error {
	owner := fmt.Sprintf("telegram:%d", t.Config.OwnerID)
	if u.Callback != nil {
		c := u.Callback
		if c.From.ID != t.Config.OwnerID || c.Message == nil || c.Message.Chat.Type != "private" || c.Message.Chat.ID != t.Config.OwnerID {
			return nil
		}
		parts := strings.Split(c.Data, ":")
		answer := "Richiesta non valida"
		if len(parts) == 4 && parts[0] == "a" {
			if e := t.Engine.Approve(parts[1], parts[2], parts[3], owner); e == nil {
				answer = "Scelta registrata"
			} else {
				answer = e.Error()
			}
		}
		return t.api(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": c.ID, "text": answer}, nil)
	}
	m := u.Message
	if m == nil || m.From.ID != t.Config.OwnerID || m.Chat.ID != t.Config.OwnerID || m.Chat.Type != "private" {
		return nil
	}
	if m.Text == "" {
		return t.send(ctx, "Per ora accetto messaggi di testo. Gli allegati non vengono scaricati automaticamente.", nil)
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return nil
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	switch fields[0] {
	case "/start", "/help":
		return t.send(ctx, "Alina · operatore personale\nScrivi una richiesta.\n/status · lavori\n/cancel ID · interrompi\n/resume ID · riprendi\n/intentions · intenzioni personali\n/new · nuova sessione\n/permissions · consensi\n/revoke ID · revoca", nil)
	case "/new":
		t.mu.Lock()
		t.state.Sessions[chat] = "tg-" + randomID()
		er := t.saveLocked()
		t.mu.Unlock()
		if er != nil {
			return er
		}
		return t.send(ctx, "Nuova sessione avviata.", nil)
	case "/status":
		return t.send(ctx, formatJobs(t.Engine.Jobs(owner)), nil)
	case "/resume":
		if len(fields) != 2 {
			return t.send(ctx, "Uso: /resume ID", nil)
		}
		j, err := t.Engine.Resume(fields[1], owner)
		if err != nil {
			return t.send(ctx, err.Error(), nil)
		}
		t.mu.Lock()
		t.state.Sessions[chat] = j.Session
		err = t.saveLocked()
		t.mu.Unlock()
		if err != nil {
			return err
		}
		return t.send(ctx, "Ripresa avviata: "+j.ID, nil)
	case "/intentions":
		intentions, err := t.Engine.Memory.Intentions(ctx, false)
		if err != nil {
			return err
		}
		return t.send(ctx, jsonText(intentions), nil)
	case "/cancel":
		if len(fields) != 2 {
			return t.send(ctx, "Uso: /cancel ID", nil)
		}
		e := t.Engine.Cancel(fields[1], owner)
		if e != nil {
			return t.send(ctx, e.Error(), nil)
		}
		return t.send(ctx, "Interruzione richiesta.", nil)
	case "/permissions":
		return t.send(ctx, formatGrants(t.Engine.Permissions.List()), nil)
	case "/revoke":
		if len(fields) != 2 {
			return t.send(ctx, "Uso: /revoke ID", nil)
		}
		if e := t.Engine.Permissions.Revoke(fields[1]); e != nil {
			return t.send(ctx, e.Error(), nil)
		}
		return t.send(ctx, "Consenso revocato.", nil)
	}
	t.mu.Lock()
	session := t.state.Sessions[chat]
	if session == "" {
		session = "tg-" + chat
		t.state.Sessions[chat] = session
	}
	t.mu.Unlock()
	// Stable update IDs prevent a crash before offset persistence from replaying
	// a submitted command. Compact IDs also fit Telegram's callback_data limit.
	key := fmt.Sprintf("tg%x", u.ID)
	if old, ok := t.Engine.Get(key); ok {
		if old.Owner != owner {
			return fmt.Errorf("Telegram update owner mismatch")
		}
		return nil
	}
	j, e := t.Engine.SubmitKey(session, owner, m.Text, key)
	if e != nil {
		return t.send(ctx, "Impossibile avviare: "+e.Error(), nil)
	}
	return t.send(ctx, "Avviato · "+j.ID, nil)
}
func (t *Telegram) notify(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for _, j := range t.Engine.Jobs(fmt.Sprintf("telegram:%d", t.Config.OwnerID)) {
			stamp := ""
			text := ""
			var keyboard any
			if j.Status == "approval" && j.Approval != nil {
				a := j.Approval
				stamp = a.ID
				text = fmt.Sprintf("Consenso richiesto · %s\n%s\nDirectory: %s\nComando:\n%s\n\nIl permesso vale per questo comando e directory. Scade tra 15 minuti.", j.ID, a.Action.Reason, a.Action.Directory, a.Action.Command)
				rows := []any{}
				for _, opt := range []struct{ Label, Scope string }{{"Solo una volta", "once"}, {"Fino al riavvio", "restart"}, {"Fino a revoca", "always"}, {"Nega", "deny"}} {
					rows = append(rows, []any{map[string]string{"text": opt.Label, "callback_data": "a:" + j.ID + ":" + a.ID + ":" + opt.Scope}})
				}
				keyboard = map[string]any{"inline_keyboard": rows}
			}
			if terminalStatus(j.Status) {
				stamp = j.Status
				text = j.ID + " · " + j.Status + "\n" + j.Output
				if j.Error != "" {
					text += "\n" + j.Error
				}
			}
			if stamp == "" {
				continue
			}
			t.mu.Lock()
			sent := t.state.Delivered[j.ID] == stamp
			t.mu.Unlock()
			if sent {
				continue
			}
			if er := t.send(ctx, text, keyboard); er != nil {
				log.Print("Telegram delivery failed; retry pending")
				break
			}
			t.mu.Lock()
			t.state.Delivered[j.ID] = stamp
			er := t.saveLocked()
			t.mu.Unlock()
			if er != nil {
				log.Print("Telegram delivery checkpoint failed")
			}
		}
	}
}
func terminalStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "interrupted"
}
