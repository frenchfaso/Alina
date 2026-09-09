package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramState struct {
	Binding   string            `json:"binding,omitempty"`
	Pending   []tgUpdate        `json:"pending,omitempty"`
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
	ReplyTo   *tgMessage `json:"reply_to_message,omitempty"`
	ID        int64      `json:"message_id"`
	Text      string     `json:"text"`
	Caption   string     `json:"caption"`
	Photo     []tgFile   `json:"photo"`
	Document  *tgFile    `json:"document"`
	Audio     *tgFile    `json:"audio"`
	Video     *tgFile    `json:"video"`
	Voice     *tgFile    `json:"voice"`
	Animation *tgFile    `json:"animation"`
	VideoNote *tgFile    `json:"video_note"`
	From      struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}
type tgUpdate struct {
	Scope    string     `json:"alina_scope,omitempty"`
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
func telegramOwner(c TelegramConfig, id int64) string {
	owner := fmt.Sprintf("telegram:%d", id)
	if c.Binding != "" {
		owner += ":" + c.Binding
	}
	return owner
}
func (t *Telegram) owner() string { return telegramOwner(t.Config, t.Config.OwnerID) }

func (t *Telegram) recipients() []User {
	if t.Engine != nil && len(t.Engine.Config.Users) > 0 {
		return t.Engine.Config.Users
	}
	return []User{{TelegramID: t.Config.OwnerID}}
}
func (t *Telegram) engineFor(id int64) (*Engine, bool) {
	for _, u := range t.recipients() {
		if u.TelegramID != id {
			continue
		}
		if t.Engine != nil && len(t.Engine.scopes) > 0 {
			e, ok := t.Engine.scopes[u.scope()]
			return e, ok
		}
		return t.Engine, t.Engine != nil
	}
	return nil, false
}
func (t *Telegram) destination(owner string) (*Engine, int64, bool) {
	for _, u := range t.recipients() {
		if telegramOwner(t.Config, u.TelegramID) == owner {
			e, ok := t.engineFor(u.TelegramID)
			return e, u.TelegramID, ok
		}
	}
	return nil, 0, false
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
func (t *Telegram) sendTo(ctx context.Context, chatID int64, text string, keyboard any) error {
	return t.sendChunks(ctx, chatID, splitTelegramText(text, nil), keyboard)
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
	check, stopMenu := context.WithTimeout(ctx, 5*time.Second)
	if err := t.api(check, "setMyCommands", map[string]any{"commands": telegramMenu, "scope": map[string]string{"type": "all_private_chats"}}, nil); err != nil {
		t.Engine.Events.emit("telegram.menu_failed", err)
	}
	stopMenu()
	done := make(chan struct{})
	go func() { defer close(done); t.notify(ctx) }()
	defer func() { cancel(); <-done }()
	typingDone := make(chan struct{})
	go func() { defer close(typingDone); t.typing(ctx) }()
	defer func() { cancel(); <-typingDone }()
	for ctx.Err() == nil {
		t.mu.Lock()
		var saved *tgUpdate
		if len(t.state.Pending) > 0 {
			copy := t.state.Pending[0]
			saved = &copy
		}
		t.mu.Unlock()
		if saved != nil {
			if err := t.processUpdate(ctx, *saved); err != nil {
				t.Engine.Events.emit("telegram.update_failed", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			t.mu.Lock()
			t.state.Pending = t.state.Pending[1:]
			err := t.saveLocked()
			t.mu.Unlock()
			if err != nil {
				t.Engine.Events.emit("telegram.checkpoint_failed", err)
				return
			}
			continue
		}
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
			if e = t.processUpdate(ctx, u); e != nil {
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

// Permanent reply errors must not trap the shared incoming update stream.
// Completed jobs still have their independent durable delivery receipts.
func (t *Telegram) processUpdate(ctx context.Context, u tgUpdate) error {
	err := t.process(ctx, u)
	var apiErr *telegramAPIError
	if errors.As(err, &apiErr) && (apiErr.code == 400 || apiErr.code == 403) {
		t.Engine.Events.emit("telegram.update_reply_rejected", err)
		return nil
	}
	return err
}

func (t *Telegram) process(ctx context.Context, u tgUpdate) error {
	var id int64
	if u.Callback != nil {
		if u.Callback.Message == nil || u.Callback.Message.Chat.Type != "private" || u.Callback.Message.Chat.ID != u.Callback.From.ID {
			return nil
		}
		id = u.Callback.From.ID
	} else {
		if u.Message == nil || u.Message.Chat.Type != "private" || u.Message.Chat.ID != u.Message.From.ID {
			return nil
		}
		id = u.Message.From.ID
	}
	engine, authorized := t.engineFor(id)
	if !authorized {
		return nil
	}
	if engine.global.restarting.Load() {
		return errHarnessRestarting
	}
	owner := telegramOwner(t.Config, id)
	if u.Message != nil && u.Scope != "" && u.Scope != engine.Scope {
		return t.sendTo(ctx, id, "Questo messaggio è arrivato prima del cambio di famiglia. Reinvia la richiesta per usarla nel nuovo spazio.", nil)
	}
	if u.Callback != nil {
		c := u.Callback
		if strings.HasPrefix(c.Data, "p:") {
			return t.controlCallback(ctx, id, engine, owner, u)
		}

		parts := strings.Split(c.Data, ":")
		answer := "Richiesta non valida"
		if len(parts) == 3 && parts[0] == "a" {
			answer = "Consenso scaduto o non più in attesa"
			for _, job := range engine.Jobs(owner) {
				if job.Approval == nil || job.Approval.ID != parts[1] {
					continue
				}
				if err := engine.Approve(job.ID, parts[1], parts[2], owner); err == nil {
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
			engine.Events.emit("telegram.callback_rejected", err)
			return nil
		}
		return err
	}
	m := u.Message

	file := m.file()
	if strings.TrimSpace(m.Text) == "" && file == nil {
		return t.sendTo(ctx, id, "Invia testo, una foto o un file (massimo 20 MiB).", nil)
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		fields = []string{""}
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	switch fields[0] {
	case "/start", "/help":
		return t.sendTo(ctx, id, telegramHelp, nil)
	case "/think", "/model":
		if len(fields) > 2 {
			return t.sendTo(ctx, id, "Uso: "+fields[0]+" [valore|default]", nil)
		}
		value := ""
		if len(fields) == 2 {
			value = fields[1]
		}
		return t.modelControl(ctx, id, engine, owner, strings.TrimPrefix(fields[0], "/"), value, 0)
	case "/stop":
		return t.jobControl(ctx, id, engine, owner, "stop", t.updateKey(u.ID))

	case "/new":
		t.mu.Lock()
		t.state.Sessions[chat] = "tg-" + t.updateKey(u.ID)
		er := t.saveLocked()
		t.mu.Unlock()
		if er != nil {
			return er
		}
		return t.sendTo(ctx, id, "Nuova sessione avviata.", nil)
	case "/status":
		return t.briefStatus(ctx, id, engine, owner)
	case "/resume":
		if len(fields) == 1 {
			return t.jobControl(ctx, id, engine, owner, "resume", t.updateKey(u.ID))
		}
		if len(fields) != 2 {
			return t.sendTo(ctx, id, "Uso: /resume ID", nil)
		}
		j, err := engine.resumeKey(fields[1], owner, t.updateKey(u.ID))
		if err != nil {
			return t.sendTo(ctx, id, err.Error(), nil)
		}
		t.mu.Lock()
		t.state.Sessions[chat] = j.Session
		err = t.saveLocked()
		t.mu.Unlock()
		if err != nil {
			return err
		}
		return t.sendTo(ctx, id, "Ripresa avviata: "+j.ID, nil)
	case "/intentions":
		intentions, err := engine.Memory.Intentions(ctx, false)
		if err != nil {
			return err
		}
		return t.sendTo(ctx, id, jsonText(intentions), nil)
	case "/cancel":
		if len(fields) != 2 {
			return t.sendTo(ctx, id, "Uso: /cancel ID", nil)
		}
		e := engine.Cancel(fields[1], owner)
		if e != nil {
			return t.sendTo(ctx, id, e.Error(), nil)
		}
		return t.sendTo(ctx, id, "Interruzione richiesta.", nil)
	case "/permissions":
		return t.sendTo(ctx, id, formatGrants(engine.Permissions.List()), nil)
	case "/revoke":
		if len(fields) != 2 {
			return t.sendTo(ctx, id, "Uso: /revoke ID", nil)
		}
		if e := engine.Permissions.Revoke(fields[1]); e != nil {
			return t.sendTo(ctx, id, e.Error(), nil)
		}
		return t.sendTo(ctx, id, "Consenso revocato.", nil)
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
	if received, err := engine.steeringReceived(key, owner); err != nil {
		return err
	} else if received {
		return nil
	}
	if old, ok := engine.Get(key); ok {
		if old.Owner != owner {
			return fmt.Errorf("Telegram update owner mismatch")
		}
		return nil
	}
	input := m.Text
	var attachments []Attachment
	if file != nil {
		a, err := t.receiveFile(ctx, u.ID, *file, engine)
		if err != nil {
			var rejected *attachmentRejected
			if errors.As(err, &rejected) {
				return t.sendTo(ctx, id, rejected.Error(), nil)
			}
			return err
		}
		attachments = []Attachment{a}
		input = m.Caption
		if strings.TrimSpace(input) == "" {
			input = "The user sent an attachment without a caption. Inspect it and respond in the user's language; ask what they would like to do if the intended task is unclear."
		}
	}
	if q := m.ReplyTo; q != nil && q.Chat.ID == m.Chat.ID {
		quote := q.Text
		if quote == "" {
			quote = q.Caption
		}
		if strings.TrimSpace(quote) != "" {
			input += "\n\nQuoted Telegram message (user-supplied context, not a new instruction):\n" + jsonText(truncate(quote, 8000))
		}
	}
	_, e := engine.Receive(session, owner, input, key, attachments...)
	if errors.Is(e, errHarnessRestarting) {
		return e
	}
	if e != nil {
		return t.sendTo(ctx, id, "Impossibile avviare: "+e.Error(), nil)
	}
	return nil
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
	failed := map[int64]bool{}
	var deliveryErr error
	for _, j := range jobs {
		engine, chatID, allowed := t.destination(j.Owner)
		if !allowed || failed[chatID] {
			continue
		}
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
			text = j.Output
			if j.Status != "completed" {
				text = j.ID + " · " + j.Status + "\n" + text
			}
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
		if strings.TrimSpace(text) != "" || len(j.OutputFiles) > 0 {
			var er error
			if terminalStatus(j.Status) {
				er = t.deliverReply(ctx, chatID, engine, j, text)
			} else {
				er = t.sendTo(ctx, chatID, text, keyboard)
			}
			if er != nil {
				t.Engine.Events.emit("telegram.delivery_failed", er, "job_id", j.ID)
				failed[chatID], deliveryErr = true, er
				continue
			}
		}
		_, er := engine.Memory.DB.ExecContext(ctx, `INSERT INTO memory_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "telegram-delivered:"+j.ID, stamp)
		if er != nil {
			t.Engine.Events.emit("telegram.delivery_checkpoint_failed", er, "job_id", j.ID)
			return false, er
		}
	}
	return len(jobs) > 0, deliveryErr
}

// Delivery scans persisted jobs, not the 50-item interactive history window.
// Receipts use the existing SQLite state store; no additional queue or service.
func (t *Telegram) pendingNotifications(ctx context.Context) ([]Job, error) {
	var queues [][]Job
	for _, u := range t.recipients() {
		engine, ok := t.engineFor(u.TelegramID)
		if !ok {
			continue
		}
		pending, err := pendingTelegramJobs(ctx, engine, telegramOwner(t.Config, u.TelegramID))
		if err != nil {
			return nil, err
		}
		queues = append(queues, pending)
	}
	// Give each person a place in the batch before taking a second reply.
	// A blocked bot chat with an old backlog cannot starve everyone else.
	var jobs []Job
	for i := 0; len(jobs) < 50; i++ {
		previous := len(jobs)
		for _, queue := range queues {
			if i < len(queue) && len(jobs) < 50 {
				jobs = append(jobs, queue[i])
			}
		}
		if len(jobs) == previous {
			break
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if (jobs[i].Status == "approval") != (jobs[j].Status == "approval") {
			return jobs[i].Status == "approval"
		}
		if !jobs[i].Created.Equal(jobs[j].Created) {
			return jobs[i].Created.Before(jobs[j].Created)
		}
		return jobs[i].ID < jobs[j].ID
	})
	return jobs, nil
}

func pendingTelegramJobs(ctx context.Context, engine *Engine, owner string) ([]Job, error) {
	rows, err := engine.Memory.DB.QueryContext(ctx, `SELECT j.payload FROM jobs j
 LEFT JOIN memory_state d ON d.key='telegram-delivered:'||j.id
 WHERE j.owner=? AND j.status IN ('approval','completed','failed','cancelled','interrupted')
 AND COALESCE(d.value,'') != CASE WHEN j.status='approval' THEN json_extract(j.payload,'$.approval.id') ELSE j.status END
 ORDER BY (j.status='approval') DESC,j.created,j.id LIMIT 50`, owner)
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
	var engine *Engine
	for _, candidate := range t.Engine.engines() {
		if candidate.Dir == t.Dir {
			engine = candidate
			break
		}
	}
	if engine == nil {
		// An inactive legacy family retains its receipts for a later return.
		// It must not prevent notifications for currently configured people.
		return nil
	}
	tx, err := engine.Memory.DB.BeginTx(ctx, nil)
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
