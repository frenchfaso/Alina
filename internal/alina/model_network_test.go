package alina

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInferenceNetworkRecovery(t *testing.T) {
	for _, mode := range []string{"recover", "worker", "bounded", "interrupted stream"} {
		t.Run(mode, func(t *testing.T) {
			var dials, requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "interrupted stream" {
					fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"unfinished\",\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"PRIVATE_COMMAND\\\"}\"}}\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Verified.\"}]}]}}\n\n")
			}))
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				n := dials.Add(1)
				if mode == "bounded" || mode != "interrupted stream" && n == 1 {
					httptrace.ContextClientTrace(ctx).DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_HOST"})
					return nil, &net.DNSError{Err: "PRIVATE_DNS_ERROR", Name: "PRIVATE_HOST", IsTimeout: true}
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}}
			defer transport.CloseIdleConnections()
			e := newTestEngine(t, nil)
			e.Config.Provider, e.Config.OpenCodeAPI, e.Config.OpenCodeKey = "opencode", "responses", "PRIVATE_KEY"
			e.Model = &Provider{Config: e.Config, Client: &http.Client{Transport: transport}, BaseURL: server.URL}
			events, err := openEventLog(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer events.Close()
			e.Events = events
			j := &runningJob{Job: Job{ID: "network-fixture", Kind: "chat"}, ctx: e.ctx}
			if mode == "worker" {
				j.Kind = "delegate"
				var cancel context.CancelFunc
				j.workCtx, cancel = context.WithTimeout(e.ctx, 10*time.Second)
				defer cancel()
			}
			answer, err := (jobModel{e: e, j: j}).Complete(e.ctx, "network-fixture", []Message{{Role: "user", Content: "PRIVATE_PROMPT"}}, nil, nil)
			switch mode {
			case "recover", "worker":
				if err != nil || answer.Content != "Verified." || dials.Load() != 2 || requests.Load() != 1 {
					t.Fatal("pre-dispatch recovery failed or repeated a request", dials.Load(), requests.Load(), err)
				}
			case "bounded":
				if err == nil || dials.Load() != 2 || requests.Load() != 0 {
					t.Fatal("persistent DNS failure was not bounded", dials.Load(), requests.Load(), err)
				}
			case "interrupted stream":
				if err == nil || dials.Load() != 1 || requests.Load() != 1 || len(answer.Calls) != 0 {
					t.Fatal("ambiguous stream was replayed or exposed tools", dials.Load(), requests.Load(), err)
				}
			}
			data, readErr := os.ReadFile(events.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			text := string(data)
			if strings.Contains(text, "PRIVATE_") || strings.Contains(text, server.URL) {
				t.Fatal("network diagnostics leaked content", text)
			}
			if mode == "interrupted stream" {
				if strings.Contains(text, "model.retrying") || !strings.Contains(text, `"stage":"read_response"`) {
					t.Fatal("stream failure lacked phase evidence or was retried", text)
				}
			} else if !strings.Contains(text, "model.retrying") || !strings.Contains(text, `"stage":"dns"`) {
				t.Fatal("missing DNS failure diagnostics or recovery event", text)
			}
		})
	}
}
