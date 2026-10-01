package alina

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func traceMetadata(t *networkTrace) map[string]any {
	fields := t.fields()
	metadata := make(map[string]any, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		metadata[fields[i].(string)] = fields[i+1]
	}
	return metadata
}

func TestNetworkTraceHTTP2TimeoutRedactsAndPreservesReuse(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/first" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	first := newNetworkTrace()
	if err := requestJSON(first.context(context.Background()), client, "GET", server.URL+"/first", nil, nil, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if metadata := traceMetadata(first); metadata["protocol"] != "h2" || metadata["stage"] != "read_response" {
		t.Fatal(metadata)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	trace := newNetworkTrace()
	err := requestJSON(trace.context(ctx), client, "POST", server.URL+"/PRIVATE_TOKEN", map[string]string{"private": "PRIVATE_BODY"}, nil, &map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	metadata := traceMetadata(trace)
	if metadata["stage"] != "response" || metadata["connection_reused"] != true || metadata["protocol"] != "h2" {
		t.Fatal(metadata)
	}
	events, err := openEventLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events.emit("telegram.poll_failed", &networkFailure{cause: errors.New("PRIVATE_ERROR")}, trace.fields()...)
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(events.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), server.URL) || !strings.Contains(string(data), `"connection_reused":true`) {
		t.Fatal("unsafe or incomplete trace", string(data))
	}
}

func TestNetworkTraceLateCallbacksDoNotChangeFailureSnapshot(t *testing.T) {
	trace := newNetworkTrace()
	hooks := httptrace.ContextClientTrace(trace.context(context.Background()))
	hooks.GotFirstResponseByte()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			hooks.DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_HOST"})
			hooks.ConnectStart("tcp", "PRIVATE_IP:443")
			hooks.TLSHandshakeStart()
		})
	}
	wg.Wait()
	before := traceMetadata(trace)
	if before["stage"] != "read_response" {
		t.Fatal("late connection callback regressed progress", before)
	}
	hooks.GetConn("PRIVATE_HOST")
	hooks.GotConn(httptrace.GotConnInfo{Reused: true})
	hooks.WroteRequest(httptrace.WroteRequestInfo{})
	if after := traceMetadata(trace); !reflect.DeepEqual(before, after) {
		t.Fatal("late callbacks changed saved failure", before, after)
	}
}

type responseReadFault struct{ err error }

func (r responseReadFault) Read([]byte) (int, error) { return 0, r.err }

func TestResponseReadFaultIsNetworkFailureWithoutMisclassifyingJSON(t *testing.T) {
	cause := errors.New("PRIVATE_PROTOCOL_ERROR")
	err := decodeLimited(io.MultiReader(strings.NewReader(`{"ok":`), responseReadFault{cause}), &map[string]any{})
	var failure *networkFailure
	if !errors.As(err, &failure) || !errors.Is(err, cause) || !telegramPollNetworkError(err) || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("read failure lost classification, cause or privacy", err)
	}
	for _, body := range []string{`{"ok":`, strings.Repeat("x", 8<<20+1)} {
		err := decodeLimited(strings.NewReader(body), &map[string]any{})
		if err == nil || telegramPollNetworkError(err) {
			t.Fatal("invalid content was classified as a connection failure", err)
		}
	}
}
