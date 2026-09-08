package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramState struct {
	Binding   string            `json:"binding,omitempty"`
	Offset    int64             `json:"offset"`
	Sessions  map[string]string `json:"sessions"`
	Delivered map[string]string `json:"delivered,omitempty"` // Legacy receipts, migrated to SQLite.
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
	ID        int64    `json:"message_id"`
	Text      string   `json:"text"`
	Caption   string   `json:"caption"`
	Photo     []tgFile `json:"photo"`
	Document  *tgFile  `json:"document"`
	Audio     *tgFile  `json:"audio"`
	Video     *tgFile  `json:"video"`
	Voice     *tgFile  `json:"voice"`
	Animation *tgFile  `json:"animation"`
	VideoNote *tgFile  `json:"video_note"`
	From      struct {
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
	if client == nil {
		client = newHTTPClient()
	}
	t := &Telegram{Dir: dir, Config: c, Engine: e, Client: client, state: telegramState{Sessions: map[string]string{}, Delivered: map[string]string{}}}
	if dir == "" {
		return t
	}
	b, er := os.ReadFile(filepath.Join(dir, "telegram.json"))
	if er == nil {
		t.stateErr = json.Unmarshal(b, &t.state)
	} else if !os.IsNotExist(er) {
		t.stateErr = er
	}
	if t.state.Binding != c.Binding {
		t.state = telegramState{Binding: c.Binding}
		t.stateErr = nil
	}
	if t.state.Sessions == nil {
		t.state.Sessions = map[string]string{}
	}
	if t.state.Delivered == nil {
		t.state.Delivered = map[string]string{}
	}
	return t
}

// A new pairing isolates delivery receipts and update IDs from an old bot.
// Empty bindings retain compatibility with existing installations.
func (t *Telegram) owner() string {
	owner := fmt.Sprintf("telegram:%d", t.Config.OwnerID)
	if t.Config.Binding != "" {
		owner += ":" + t.Config.Binding
	}
	return owner
}
func (t *Telegram) updateKey(id int64) string {
	if t.Config.Binding == "" {
		return fmt.Sprintf("tg%x", id)
	}
	return "tg" + contentID(fmt.Sprintf("%s:%d", t.Config.Binding, id))[:16]
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
		var status *remoteHTTPError
		if errors.As(e, &status) {
			return &telegramAPIError{code: status.Status}
		}
		return e
	}
	if !envelope.OK {
		return &telegramAPIError{code: envelope.ErrorCode}
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
		t.Engine.Events.emit("telegram.state_invalid", t.stateErr)
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
		e := t.api(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if e != nil {
			if ctx.Err() != nil {
				return
			}
			t.Engine.Events.emit("telegram.poll_failed", e)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if e = t.process(ctx, u); e != nil {
				t.Engine.Events.emit("telegram.update_failed", e)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				break
			}
			t.mu.Lock()
			t.state.Offset = u.ID + 1
			e = t.saveLocked()
			t.mu.Unlock()
			if e != nil {
				t.Engine.Events.emit("telegram.checkpoint_failed", e)
				return
			}
		}
	}
}
func (t *Telegram) process(ctx context.Context, u tgUpdate) error {
	owner := t.owner()
	if u.Callback != nil {
		c := u.Callback
		if c.From.ID != t.Config.OwnerID || c.Message == nil || c.Message.Chat.Type != "private" || c.Message.Chat.ID != t.Config.OwnerID {
			return nil
		}
		parts := strings.Split(c.Data, ":")
		answer := "Richiesta non valida"
		if len(parts) == 3 && parts[0] == "a" {
			answer = "Consenso scaduto o non più in attesa"
			for _, job := range t.Engine.Jobs(owner) {
				if job.Approval == nil || job.Approval.ID != parts[1] {
					continue
				}
				if err := t.Engine.Approve(job.ID, parts[1], parts[2], owner); err == nil {
					answer = "Scelta registrata"
				} else {
					answer = err.Error()
				}
				break
			}
		}
		err := t.api(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": c.ID, "text": truncate(answer, 150)}, nil)
		var apiErr *telegramAPIError
		if errors.As(err, &apiErr) && apiErr.code == 400 {
			// An expired callback cannot be acknowledged. Retrying this update
			// indefinitely would block all subsequent owner messages.
			t.Engine.Events.emit("telegram.callback_rejected", err)
			return nil
		}
		return err
	}
	m := u.Message
	if m == nil || m.From.ID != t.Config.OwnerID || m.Chat.ID != t.Config.OwnerID || m.Chat.Type != "private" {
		return nil
	}
	file := m.file()
	if strings.TrimSpace(m.Text) == "" && file == nil {
		return t.send(ctx, "Invia testo, una foto o un file (massimo 20 MiB).", nil)
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		fields = []string{""}
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	switch fields[0] {
	case "/start", "/help":
		return t.send(ctx, "Alina · operatore personale\nScrivi una richiesta. Durante un lavoro, nuovi messaggi e allegati lo aggiornano al prossimo punto sicuro.\n/status · lavori\n/cancel ID · interrompi subito\n/resume ID · riprendi\n/intentions · intenzioni personali\n/new · nuova conversazione\n/permissions · consensi\n/revoke ID · revoca", nil)
	case "/new":
		t.mu.Lock()
		t.state.Sessions[chat] = "tg-" + t.updateKey(u.ID)
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
		j, err := t.Engine.resumeKey(fields[1], owner, t.updateKey(u.ID))
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
		if t.Config.Binding != "" {
			session += "-" + t.Config.Binding
		}
		t.state.Sessions[chat] = session
	}
	t.mu.Unlock()
	// Stable update IDs prevent a crash before offset persistence from replaying
	// a submitted command. Compact IDs also fit Telegram's callback_data limit.
	key := t.updateKey(u.ID)
	if received, err := t.Engine.steeringReceived(key, owner); err != nil {
		return err
	} else if received {
		return nil
	}
	if old, ok := t.Engine.Get(key); ok {
		if old.Owner != owner {
			return fmt.Errorf("Telegram update owner mismatch")
		}
		return nil
	}
	input := m.Text
	var attachments []Attachment
	if file != nil {
		a, err := t.receiveFile(ctx, u.ID, *file)
		if err != nil {
			var rejected *attachmentRejected
			if errors.As(err, &rejected) {
				return t.send(ctx, rejected.Error(), nil)
			}
			return err
		}
		attachments = []Attachment{a}
		input = m.Caption
		if strings.TrimSpace(input) == "" {
			input = "The user sent an attachment without a caption. Inspect it and respond in the user's language; ask what they would like to do if the intended task is unclear."
		}
	}
	j, e := t.Engine.Receive(session, owner, input, key, attachments...)
	if e != nil {
		return t.send(ctx, "Impossibile avviare: "+e.Error(), nil)
	}
	if j.ID != key {
		return t.send(ctx, "Messaggio aggiunto al lavoro · "+j.ID, nil)
	}
	return t.send(ctx, "Avviato · "+j.ID, nil)
}
func (t *Telegram) notify(ctx context.Context) {
	backoff := time.Duration(0)
	for ctx.Err() == nil {
		changed := t.Engine.jobChanged.watch()
		more, err := t.deliverPending(ctx)
		if err != nil {
			backoff = min(max(5*time.Second, 2*backoff), time.Minute)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		backoff = 0
		if more {
			continue // Drain the persisted backlog before waiting for new work.
		}
		select {
		case <-ctx.Done():
		case <-changed:
		}
	}
}

func (t *Telegram) deliverPending(ctx context.Context) (bool, error) {
	if err := t.importDeliveryReceipts(ctx); err != nil {
		t.Engine.Events.emit("telegram.delivery_checkpoint_failed", err)
		return false, err
	}
	jobs, err := t.pendingNotifications(ctx)
	if err != nil {
		t.Engine.Events.emit("telegram.delivery_query_failed", err)
		return false, err
	}
	for _, j := range jobs {
		stamp := ""
		text := ""
		var keyboard any
		if j.Status == "approval" && j.Approval != nil {
			a := j.Approval
			stamp = a.ID
			text = fmt.Sprintf("Consenso richiesto · %s\n%s\nDirectory: %s\nComando:\n%s\n\nIl permesso vale per questo comando e directory. Scade tra 15 minuti.", j.ID, a.Action.Reason, a.Action.Directory, a.Action.Command)
			rows := []any{}
			for _, opt := range []struct{ Label, Scope string }{{"Solo una volta", "once"}, {"Fino al riavvio", "restart"}, {"Fino a revoca", "always"}, {"Nega", "deny"}} {
				// The globally unique approval ID is sufficient. Including the
				// job ID exceeds Telegram's 64-byte limit for scheduled jobs.
				rows = append(rows, []any{map[string]string{"text": opt.Label, "callback_data": "a:" + a.ID + ":" + opt.Scope}})
			}
			keyboard = map[string]any{"inline_keyboard": rows}
		}
		if terminalStatus(j.Status) {
			stamp = j.Status
			text = j.ID + " · " + j.Status + "\n" + j.Output
			if j.Error != "" {
				text += "\n" + j.Error
			}
			if j.PendingSteering > 0 {
				text += fmt.Sprintf("\n%d messaggi salvati in attesa: /resume %s", j.PendingSteering, j.ID)
			}
		}
		if stamp == "" {
			continue
		}
		if er := t.send(ctx, text, keyboard); er != nil {
			t.Engine.Events.emit("telegram.delivery_failed", er, "job_id", j.ID)
			return false, er
		}
		_, er := t.Engine.Memory.DB.ExecContext(ctx, `INSERT INTO memory_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "telegram-delivered:"+j.ID, stamp)
		if er != nil {
			t.Engine.Events.emit("telegram.delivery_checkpoint_failed", er, "job_id", j.ID)
			return false, er
		}
	}
	return len(jobs) > 0, nil
}

// Delivery scans persisted jobs, not the 50-item interactive history window.
// Receipts use the existing SQLite state store; no additional queue or service.
func (t *Telegram) pendingNotifications(ctx context.Context) ([]Job, error) {
	rows, err := t.Engine.Memory.DB.QueryContext(ctx, `SELECT j.payload FROM jobs j
 LEFT JOIN memory_state d ON d.key='telegram-delivered:'||j.id
 WHERE j.owner=? AND j.status IN ('approval','completed','failed','cancelled','interrupted')
 AND COALESCE(d.value,'') != CASE WHEN j.status='approval' THEN json_extract(j.payload,'$.approval.id') ELSE j.status END
 ORDER BY (j.status='approval') DESC,j.created,j.id LIMIT 50`, t.owner())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var raw string
		var j Job
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (t *Telegram) importDeliveryReceipts(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.state.Delivered) == 0 {
		return nil
	}
	tx, err := t.Engine.Memory.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, stamp := range t.state.Delivered {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_state VALUES(?,?)", "telegram-delivered:"+id, stamp); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	previous := t.state.Delivered
	t.state.Delivered = nil
	if err = t.saveLocked(); err != nil {
		t.state.Delivered = previous
	}
	return err
}
func terminalStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "interrupted"
}
