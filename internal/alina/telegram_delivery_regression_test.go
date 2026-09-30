package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelegramUnavailableFileDoesNotBlockReplies(t *testing.T) {
	for _, failure := range []string{"missing", "changed"} {
		t.Run(failure, func(t *testing.T) {
			e := newTestEngine(t, nil)
			e.Config.Telegram = TelegramConfig{Enabled: true, OwnerID: 42, Token: "fixture"}
			old := &runningJob{Job: Job{ID: "old", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "running", Created: time.Now().Add(-time.Hour), Output: "Old reply"}, ctx: e.ctx}
			path := filepath.Join(e.Workspace(), "report.txt")
			if err := os.WriteFile(path, []byte("original report"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := e.queueFile(old, jsonText(map[string]string{"path": path})); err != nil {
				t.Fatal(err)
			}
			if failure == "missing" {
				if err := os.Remove(old.OutputFiles[0].Path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(old.OutputFiles[0].Path, []byte("modified report"), 0600); err != nil {
				t.Fatal(err)
			}
			old.Status = "completed"
			next := &runningJob{Job: Job{ID: "new", Owner: old.Owner, Session: "chat", Kind: "chat", Status: "completed", Created: time.Now(), Output: "New reply"}}
			for _, j := range []*runningJob{old, next} {
				if err := e.persist(j); err != nil {
					t.Fatal(err)
				}
			}
			var messages []string
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
					t.Error("unavailable snapshot was uploaded")
				}
				var body struct{ Text string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return nil, err
				}
				messages = append(messages, body.Text)
				return telegramFixtureResponse(200), nil
			})}
			tg := NewTelegram(e.Dir, e.Config.Telegram, e, client)
			if _, err := tg.deliverPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			// A new adapter must reuse the durable failed-part outcome.
			tg = NewTelegram(e.Dir, e.Config.Telegram, e, client)
			if more, err := tg.deliverPending(e.ctx); err != nil || more {
				t.Fatal(more, err)
			}
			if len(messages) != 3 || messages[0] != old.Output || !strings.Contains(messages[1], "Non riesco a inviare «report.txt»") || messages[2] != next.Output {
				t.Fatal("missing notice, replay, or blocked newer reply", messages)
			}
			var outcome string
			if err := e.Memory.DB.QueryRow("SELECT value FROM memory_state WHERE key='telegram-part:old:completed:1'").Scan(&outcome); err != nil || outcome != "failed" {
				t.Fatal("failed file was recorded as sent", outcome, err)
			}
		})
	}
}

func TestTelegramUploadFailuresRemainRetryable(t *testing.T) {
	for _, failure := range []string{"network", "429", "503", "notice"} {
		t.Run(failure, func(t *testing.T) {
			e := newTestEngine(t, nil)
			e.Config.Telegram = TelegramConfig{Enabled: true, OwnerID: 42, Token: "fixture"}
			j := &runningJob{Job: Job{ID: "reply", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "running", Created: time.Now(), Output: "Reply"}, ctx: e.ctx}
			path := filepath.Join(e.Workspace(), "report.txt")
			if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := e.queueFile(j, jsonText(map[string]string{"path": path})); err != nil {
				t.Fatal(err)
			}
			if failure == "notice" {
				if err := os.Remove(j.OutputFiles[0].Path); err != nil {
					t.Fatal(err)
				}
			}
			j.Status = "completed"
			if err := e.persist(j); err != nil {
				t.Fatal(err)
			}
			attempts, textSends := 0, 0
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					var body struct{ Text string }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					if body.Text == j.Output {
						textSends++
					} else if failure == "notice" {
						attempts++
					}
				} else {
					attempts++
				}
				if attempts == 1 {
					switch failure {
					case "network", "notice":
						return nil, errors.New("temporarily offline")
					case "429":
						return telegramFixtureResponse(429), nil
					case "503":
						return telegramFixtureResponse(503), nil
					}
				}
				return telegramFixtureResponse(200), nil
			})}
			tg := NewTelegram(e.Dir, e.Config.Telegram, e, client)
			if _, err := tg.deliverPending(e.ctx); err == nil {
				t.Fatal("transient failure lost")
			}
			var parts int
			if err := e.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key='telegram-part:reply:completed:1'").Scan(&parts); err != nil || parts != 0 {
				t.Fatal("unconfirmed delivery checkpointed", parts, err)
			}
			if _, err := tg.deliverPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || textSends != 1 {
				t.Fatal("lost retry or replayed text", attempts, textSends)
			}
		})
	}
}

func telegramFixtureResponse(status int) *http.Response {
	body := `{"ok":true,"result":{"message_id":99}}`
	if status != 200 {
		body = fmt.Sprintf(`{"ok":false,"error_code":%d}`, status)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestTelegramApprovalFormsFollowLifecycle(t *testing.T) {
	for _, action := range []string{"stop", "expiry", "steering", "local approval", "restart", "completion"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			c := DefaultConfig()
			c.WorkDir = dir
			e, err := NewEngine(dir, c, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Close() })
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			j := &runningJob{Job: Job{ID: "approve-job", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "approval", Created: time.Now(), Approval: &Approval{ID: "approval-id", Expires: time.Now().Add(time.Minute)}}, ctx: ctx, cancel: cancel, decision: make(chan string, 1), accepting: true, steerSignal: make(chan struct{}, 1)}
			e.jobs[j.ID] = j
			if err := e.persist(j); err != nil {
				t.Fatal(err)
			}
			deletes := 0
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
					deletes++
					var body struct {
						Chat    int64 `json:"chat_id"`
						Message int64 `json:"message_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					if body.Chat != 42 || body.Message != 99 {
						t.Error("wrong form destination", body)
					}
				}
				return telegramFixtureResponse(200), nil
			})}
			tg := NewTelegram(dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
			if _, err := tg.deliverPending(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := tg.deliverPending(ctx); err != nil || deletes != 0 {
				t.Fatal("pending form was removed", deletes, err)
			}
			switch action {
			case "stop":
				if _, err := e.stopOwnedJobs(e.ctx, j.Owner, "stop-fixture"); err != nil {
					t.Fatal(err)
				}
				e.finish(j, "", j.ctx.Err())
			case "expiry":
				j.Approval.Expires = time.Now().Add(-time.Minute)
				if err := e.persist(j); err != nil {
					t.Fatal(err)
				}
			case "steering":
				if _, err := e.Receive(j.Session, j.Owner, "Correction", "steer-id"); err != nil {
					t.Fatal(err)
				}
			case "local approval":
				if err := e.Approve(j.ID, j.Approval.ID, "once", ""); err != nil {
					t.Fatal(err)
				}
			case "restart":
				e.Close()
				e, err = NewEngine(dir, c, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				tg = NewTelegram(dir, tg.Config, e, client)
			case "completion":
				e.finish(j, "Done.", nil)
			}
			if _, err := tg.deliverPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := tg.deliverPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			var forms int
			if err := e.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key LIKE 'telegram-approval:%'").Scan(&forms); err != nil || forms != 0 || deletes != 1 {
				t.Fatal("obsolete form retained or repeatedly deleted", forms, deletes, err)
			}
		})
	}
}

func TestTelegramApprovalCleanupRetriesWithoutBlockingReplies(t *testing.T) {
	e := newTestEngine(t, nil)
	form := telegramApprovalForm{Job: "old", Owner: "telegram:42", Approval: "old-approval", Message: 99}
	if _, err := e.Memory.DB.Exec("INSERT INTO memory_state VALUES(?,?)", "telegram-approval:"+form.Approval, jsonText(form)); err != nil {
		t.Fatal(err)
	}
	if err := e.persist(&runningJob{Job: Job{ID: "new", Owner: form.Owner, Session: "chat", Kind: "chat", Status: "completed", Output: "New reply", Created: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	deletes, sends := 0, 0
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deletes++
			if deletes == 1 {
				return telegramFixtureResponse(503), nil
			}
			return telegramFixtureResponse(400), nil // Already removed is success.
		}
		sends++
		return telegramFixtureResponse(200), nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	if _, err := tg.deliverPending(e.ctx); err == nil || sends != 1 {
		t.Fatal("cleanup failure blocked delivery or lost retry", sends, err)
	}
	tg = NewTelegram(e.Dir, tg.Config, e, client)
	if _, err := tg.deliverPending(e.ctx); err != nil || sends != 1 || deletes != 2 {
		t.Fatal("cleanup did not survive restart or replayed reply", sends, deletes, err)
	}
	var forms int
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key LIKE 'telegram-approval:%'").Scan(&forms); err != nil || forms != 0 {
		t.Fatal(forms, err)
	}
}

func TestTelegramTrackedApprovalDuplicateCallbacks(t *testing.T) {
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "approve-job", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "approval", Created: time.Now(), Approval: &Approval{ID: "approval-id", Expires: time.Now().Add(time.Minute)}}, ctx: e.ctx, decision: make(chan string, 2)}
	e.jobs[j.ID] = j
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deletes++
			if deletes > 1 {
				return telegramFixtureResponse(400), nil
			}
		}
		return telegramFixtureResponse(200), nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	if _, err := tg.deliverPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	var update tgUpdate
	if err := json.Unmarshal([]byte(`{"callback_query":{"id":"callback","data":"a:approval-id:once","from":{"id":42},"message":{"message_id":99,"chat":{"id":42,"type":"private"}}}}`), &update); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tg.process(e.ctx, update); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tg.deliverPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(j.decision) != 1 || j.Status != "running" || j.Approval != nil {
		t.Fatal("duplicate approval decision", j.Status, len(j.decision))
	}
	var forms int
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM memory_state WHERE key LIKE 'telegram-approval:%'").Scan(&forms); err != nil || forms != 0 {
		t.Fatal(forms, err)
	}
}

func TestTelegramSplitApprovalTracksButtonMessage(t *testing.T) {
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "large-approval", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "approval", Created: time.Now(), Approval: &Approval{ID: "approval-id", Expires: time.Now().Add(time.Minute), Action: Action{Command: strings.Repeat("x", 8000)}}}}
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	var last, buttonMessage, deleted int64
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Keyboard json.RawMessage `json:"reply_markup"`
			Message  int64           `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deleted = body.Message
		} else {
			last++
			if len(body.Keyboard) > 0 {
				buttonMessage = last
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, last)))}, nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	if _, err := tg.deliverPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if last < 2 || buttonMessage != last {
		t.Fatal("fixture did not split approval", last, buttonMessage)
	}
	j.Status, j.Approval = "cancelled", nil
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.deliverPending(e.ctx); err != nil || deleted != buttonMessage {
		t.Fatal("removed wrong part of split approval", deleted, buttonMessage, err)
	}
}

func TestTelegramApprovalReusesFormAfterCheckpointFailure(t *testing.T) {
	e := newTestEngine(t, nil)
	j := &runningJob{Job: Job{ID: "approve-job", Owner: "telegram:42", Session: "chat", Kind: "chat", Status: "approval", Created: time.Now(), Approval: &Approval{ID: "approval-id", Expires: time.Now().Add(time.Minute)}}}
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	// Telegram accepted the form, but the daemon stopped before the separate
	// telegram-delivered receipt. Preserve its original message ID on retry.
	form := telegramApprovalForm{Job: j.ID, Approval: j.Approval.ID, Owner: j.Owner, Message: 99}
	if _, err := e.Memory.DB.Exec("INSERT INTO memory_state VALUES(?,?)", "telegram-approval:"+form.Approval, jsonText(form)); err != nil {
		t.Fatal(err)
	}
	sends, deletes := 0, 0
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deletes++
			var body struct {
				Message int64 `json:"message_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if body.Message != form.Message {
				t.Error("lost original form ID", body.Message)
			}
		} else {
			sends++
		}
		return telegramFixtureResponse(200), nil
	})}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
	if _, err := tg.deliverPending(e.ctx); err != nil || sends != 0 || deletes != 0 {
		t.Fatal("persisted form replayed or removed", sends, deletes, err)
	}
	var stamp string
	if err := e.Memory.DB.QueryRow("SELECT value FROM memory_state WHERE key=?", "telegram-delivered:"+j.ID).Scan(&stamp); err != nil || stamp != form.Approval {
		t.Fatal("outer receipt not recovered", stamp, err)
	}
	j.Status, j.Approval = "cancelled", nil
	if err := e.persist(j); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.deliverPending(e.ctx); err != nil || deletes != 1 || sends != 1 {
		t.Fatal("original form retained or duplicate created", sends, deletes, err)
	}
}

func TestTelegramApprovalReceiptErrorsDoNotResend(t *testing.T) {
	for _, invalid := range []string{"database", "damaged", "wrong owner", "missing ID"} {
		t.Run(invalid, func(t *testing.T) {
			e := newTestEngine(t, nil)
			j := Job{ID: "approve-job", Owner: "telegram:42", Approval: &Approval{ID: "approval-id"}}
			form := telegramApprovalForm{Job: j.ID, Approval: j.Approval.ID, Owner: j.Owner, Message: 99}
			raw := jsonText(form)
			switch invalid {
			case "database":
				if err := e.Memory.DB.Close(); err != nil {
					t.Fatal(err)
				}
			case "damaged":
				raw = "invalid JSON"
			case "wrong owner":
				form.Owner = "telegram:84"
				raw = jsonText(form)
			case "missing ID":
				form.Message = 0
				raw = jsonText(form)
			}
			if invalid != "database" {
				if _, err := e.Memory.DB.Exec("INSERT INTO memory_state VALUES(?,?)", "telegram-approval:"+form.Approval, raw); err != nil {
					t.Fatal(err)
				}
			}
			client := &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
				t.Error("approval sent after failed receipt lookup")
				return telegramFixtureResponse(200), nil
			})}
			tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
			if err := tg.sendApproval(e.ctx, 42, e, j, "Approval", nil); err == nil {
				t.Fatal("receipt error ignored")
			}
		})
	}
}
