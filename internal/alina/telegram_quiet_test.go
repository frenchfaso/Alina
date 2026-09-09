package alina

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTelegramQuietDeliveryKeepsActionableNotices(t *testing.T) {
	for _, tc := range []struct {
		name, status, output, failure string
		pending                       int
		want                          string
	}{
		{name: "answer", status: "completed", output: "Ciao!", want: "Ciao!"},
		{name: "empty completion", status: "completed", output: " \n"},
		{name: "error", status: "failed", failure: "Connection unavailable", want: "Connection unavailable"},
		{name: "interrupted", status: "interrupted", want: "interrupted"},
		{name: "pending steering", status: "completed", pending: 1, want: "Reinvia le indicazioni ancora valide"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, nil)
			var sent []string
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				var b struct{ Text string }
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
					return nil, err
				}
				sent = append(sent, b.Text)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
			})}
			tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, client)
			j := &runningJob{Job: Job{ID: "quiet-job", Owner: tg.owner(), Session: "local", Status: tc.status, Output: tc.output, Error: tc.failure, PendingSteering: tc.pending}}
			if err := e.persist(j); err != nil {
				t.Fatal(err)
			}
			if _, err := tg.deliverPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(sent) != 0 {
					t.Fatal("empty completion generated noise", sent)
				}
			} else if len(sent) != 1 || !strings.Contains(sent[0], tc.want) {
				t.Fatal("missing notice", sent)
			}
			if tc.name == "answer" && sent[0] != tc.output {
				t.Fatal("technical prefix in answer", sent)
			}
			if pending, err := tg.pendingNotifications(e.ctx); err != nil || len(pending) != 0 {
				t.Fatal("missing receipt", pending, err)
			}
		})
	}
}
