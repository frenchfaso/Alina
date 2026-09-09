package alina

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesCompletedItems(t *testing.T) {
	items := `data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","encrypted_content":"opaque"}}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example"}]}]}}

`
	for _, tc := range []struct {
		name, terminal string
		fail           bool
		want           string
		raw            int
	}{
		{"empty terminal output", `{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":2}}}`, false, "Hello", 3},
		{"omitted terminal output", `{"type":"response.done","response":{"status":"completed"}}`, false, "Hello", 3},
		{"terminal authoritative", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Final"}]}]}}`, false, "Final", 1},
		{"truncated", "", true, "", 0},
		{"failed", `{"type":"response.failed"}`, true, "", 0},
		{"incomplete envelope", `{"type":"response.completed","response":{"status":"incomplete"}}`, true, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, items)
				if tc.terminal != "" {
					fmt.Fprint(w, "data: "+tc.terminal+"\n\n")
				}
			}))
			defer srv.Close()
			p := Provider{Client: srv.Client()}
			m, err := p.responses(context.Background(), srv.URL, "test", "key", "account", "session", nil, nil, false, nil)
			if tc.fail {
				if err == nil {
					t.Fatal("accepted unfinished stream")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(m.Content, tc.want) || len(m.Raw) != tc.raw {
				t.Fatal("incorrect assembled output", m)
			}
			if tc.raw == 3 {
				if len(m.Calls) != 1 || !strings.Contains(string(m.Raw[0]), "opaque") || !strings.Contains(m.Content, "https://example.com") {
					t.Fatal("lost item data", m)
				}
			}
			if tc.name == "empty terminal output" && (m.Usage == nil || m.Usage.InputTokens != 10) {
				t.Fatal("lost usage")
			}
		})
	}
}
