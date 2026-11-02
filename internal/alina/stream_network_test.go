package alina

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSSEReaderFailuresPreserveCauseAndClassification(t *testing.T) {
	cause := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("PRIVATE_SOCKET_FAILURE")}
	err := readSSE(responseReadFault{cause}, func([]byte) error { return nil })
	var failure *networkFailure
	var socketError *net.OpError
	if !errors.As(err, &failure) || !errors.As(err, &socketError) || !errors.Is(err, cause) || failure.retryable() || err.Error() != "network request failed" {
		t.Fatal("stream failure lost privacy, cause or retry boundary", err)
	}
	for _, callbackError := range []error{cause, io.EOF, &providerResponseError{Event: "response.failed", Code: "server_error"}} {
		err = readSSE(strings.NewReader("data: {}\n\n"), func([]byte) error { return callbackError })
		if err != callbackError {
			t.Fatalf("callback error was reclassified: %v", err)
		}
	}
	err = readSSE(strings.NewReader("data: "+strings.Repeat("x", 8<<20)), func([]byte) error {
		t.Fatal("oversized event reached callback")
		return nil
	})
	if !errors.Is(err, bufio.ErrTooLong) || errors.As(err, &failure) {
		t.Fatal("content limit was classified as a network failure", err)
	}
}

func TestSSEReadFailureDoesNotReplayToolsOrLeakToTelegram(t *testing.T) {
	cause := &net.OpError{Op: "read", Net: "tcp",
		Source: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 12345},
		Addr:   &net.TCPAddr{IP: net.ParseIP("198.51.100.20"), Port: 443},
		Err:    errors.New("PRIVATE_SOCKET_FAILURE")}
	var requests atomic.Int32
	e := newTestEngine(t, nil)
	e.Config.Provider, e.Config.OpenCodeAPI, e.Config.OpenCodeKey = "opencode", "responses", "fixture"
	e.Model = &Provider{Config: e.Config, BaseURL: "https://provider.invalid", Client: &http.Client{Transport: fetchTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		partial := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"unfinished\",\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"touch should-not-exist\\\"}\"}}\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(partial), responseReadFault{cause}))}, nil
	})}}
	j, err := e.Submit("stream-fault", "telegram:42", "Run the requested command.")
	if err != nil {
		t.Fatal(err)
	}
	j = awaitStatus(t, e, j.ID, "failed")
	if requests.Load() != 1 || j.Error != "network request failed" || j.Output != "" {
		t.Fatal("dispatched stream was replayed or exposed partial output", requests.Load(), j)
	}
	if _, err = os.Stat(filepath.Join(e.Config.WorkDir, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unfinished tool call was executed", err)
	}
	var sent string
	tg := NewTelegram(e.Dir, TelegramConfig{Enabled: true, OwnerID: 42, Token: "fixture"}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		sent += string(body)
		return telegramFixtureResponse(200), nil
	})})
	if _, err = tg.deliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, "network request failed") || strings.Contains(sent, "PRIVATE_") || strings.Contains(sent, "192.0.2.10") || strings.Contains(sent, "198.51.100.20") {
		t.Fatal("Telegram failure leaked network details or was not delivered", sent)
	}
}
