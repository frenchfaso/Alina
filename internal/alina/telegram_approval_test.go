package alina

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTelegramApprovalFormDisappears(t *testing.T) {
	for _, choice := range []string{"once", "restart", "always", "deny", "expired", "stale", "invalid", "other-user", "retry"} {
		t.Run(choice, func(t *testing.T) {
			e := newTestEngine(t, nil)
			j := &runningJob{Job: Job{ID: "approve-job", Owner: "telegram:42", Session: "chat", Status: "approval", Approval: &Approval{ID: "approval-id", Expires: time.Now().Add(time.Minute), Action: Action{Tool: "shell", Command: "fixture", Directory: e.Dir}}}, ctx: e.ctx, decision: make(chan string, 2)}
			e.jobs[j.ID] = j
			if choice == "expired" {
				j.Approval.Expires = time.Now().Add(-time.Minute)
			}
			if choice == "stale" {
				j.Status = "completed"
				j.Approval = nil
			}
			if err := e.persist(j); err != nil {
				t.Fatal(err)
			}
			deletes, acks := 0, 0
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, `{"ok":true,"result":true}`
				if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
					var b struct {
						Chat    int64 `json:"chat_id"`
						Message int64 `json:"message_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
						return nil, err
					}
					if b.Chat != 42 || b.Message != 99 {
						t.Error("wrong message", b)
					}
					deletes++
					if choice == "retry" && deletes == 1 {
						status = 500
						body = `{"ok":false,"error_code":500}`
					}
				} else if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
					acks++
					if choice == "expired" {
						status = 400
						body = `{"ok":false,"error_code":400}`
					}
				} else {
					t.Error("unexpected API call", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			tg := NewTelegram(e.Dir, TelegramConfig{OwnerID: 42, Token: "fixture"}, e, client)
			scope := choice
			if choice == "expired" || choice == "stale" || choice == "other-user" || choice == "retry" {
				scope = "once"
			}
			actor := 42
			if choice == "other-user" {
				actor = 84
			}
			var u tgUpdate
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"callback_query":{"id":"callback","data":"a:approval-id:%s","from":{"id":%d},"message":{"message_id":99,"chat":{"id":%d,"type":"private"}}}}`, scope, actor, actor)), &u); err != nil {
				t.Fatal(err)
			}
			err := tg.process(e.ctx, u)
			if choice == "retry" {
				if err == nil {
					t.Fatal("lost cleanup retry")
				}
				err = tg.process(e.ctx, u)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if choice == "invalid" || choice == "other-user" {
				want = 0
			}
			if choice == "retry" {
				want = 2
			}
			if deletes != want {
				t.Fatal("unexpected deletions", deletes, want)
			}
			if choice == "other-user" && acks != 0 {
				t.Fatal("unauthorized callback processed")
			}
			decisions := len(j.decision)
			if choice == "expired" || choice == "stale" || choice == "invalid" || choice == "other-user" {
				if decisions != 0 {
					t.Fatal("unexpected approval")
				}
			} else if decisions != 1 {
				t.Fatal("approval replayed or lost", decisions)
			}
		})
	}
}
