package alina

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTelegramPollCopiesClientAndOwnsTransport(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	dial := (&net.Dialer{Timeout: time.Second}).DialContext
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := &http.Transport{Proxy: proxy, DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "fixture"}, ResponseHeaderTimeout: 7 * time.Second, IdleConnTimeout: time.Minute}
	base := &http.Client{Transport: transport, Timeout: 77 * time.Second, Jar: jar, CheckRedirect: redirect}
	p := newTelegramPoll(base)
	defer p.close()
	if p.client == base || p.transport == transport || p.client.Transport != p.transport || p.client.Timeout != base.Timeout || p.client.Jar != base.Jar || reflect.ValueOf(p.client.CheckRedirect).Pointer() != reflect.ValueOf(base.CheckRedirect).Pointer() {
		t.Fatal("poll client lost injected options or shares transport")
	}
	checkTransport := func() {
		t.Helper()
		if p.transport.TLSClientConfig == transport.TLSClientConfig || p.transport.TLSClientConfig.ServerName != "fixture" || p.transport.TLSClientConfig.MinVersion != tls.VersionTLS12 || p.transport.ResponseHeaderTimeout != transport.ResponseHeaderTimeout || p.transport.IdleConnTimeout != transport.IdleConnTimeout || reflect.ValueOf(p.transport.DialContext).Pointer() != reflect.ValueOf(dial).Pointer() || reflect.ValueOf(p.transport.Proxy).Pointer() != reflect.ValueOf(proxy).Pointer() {
			t.Fatal("transport reset lost TLS/DNS/proxy/timeout configuration")
		}
	}
	checkTransport()
	first := p.transport
	if p.failed(&networkFailure{cause: errors.New("offline")}) || !p.failed(context.DeadlineExceeded) || p.transport == first || base.Transport != transport {
		t.Fatal("two network errors did not reset only the polling transport")
	}
	checkTransport()
}

func TestTelegramPollFailureClassificationAndSuccess(t *testing.T) {
	for _, permanent := range []error{&telegramAPIError{code: 401}, &telegramAPIError{code: 409}, &telegramAPIError{code: 429}, &remoteHTTPError{Status: 503}, &url.Error{Op: "Post", Err: &telegramAPIError{code: 401}}, fmt.Errorf("poll: %w", &remoteHTTPError{Status: 429}), &json.SyntaxError{}, fmt.Errorf("poll: %w", &json.SyntaxError{}), errors.New("response exceeds 8 MiB")} {
		t.Run(permanent.Error(), func(t *testing.T) {
			p := newTelegramPoll(&http.Client{Transport: &http.Transport{}})
			defer p.close()
			original := p.transport
			if p.failed(context.DeadlineExceeded) || p.failed(permanent) || p.failed(context.DeadlineExceeded) || p.transport != original {
				t.Fatal("HTTP/API/schema error counted toward transport reset")
			}
			p.succeeded()
			if p.failures != 0 || p.networkFailures != 0 || p.failed(context.DeadlineExceeded) || p.transport != original {
				t.Fatal("success did not clear failure streak")
			}
			if p.failed(context.Canceled) || p.transport != original || p.networkFailures != 1 || p.failures != 1 {
				t.Fatal("shutdown cancellation altered recovery state")
			}
		})
	}
	if !telegramPollNetworkError(io.ErrUnexpectedEOF) {
		t.Fatal("broken response body was classified as schema error")
	}
}

func TestTelegramPollDoesNotResetOnRejectedOrMalformedResponse(t *testing.T) {
	for _, fixture := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", 401, `{"ok":false,"error_code":401}`},
		{"conflict", 409, `{"ok":false,"error_code":409}`},
		{"rate_limit", 429, `{"ok":false,"error_code":429}`},
		{"http_server_error", 503, `{"ok":false,"error_code":503}`},
		{"api_error", 200, `{"ok":false,"error_code":400}`},
		{"malformed_envelope", 200, `{"ok":`},
		{"malformed_result", 200, `{"ok":true,"result":{"unexpected":"object"}}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(fixture.code)
				io.WriteString(w, fixture.body)
			}))
			defer server.Close()
			p := newTelegramPoll(server.Client())
			defer p.close()
			tg := &Telegram{BaseURL: server.URL, Config: TelegramConfig{Token: "fixture"}}
			original := p.transport
			for range 2 {
				var updates []tgUpdate
				err := tg.apiWithClient(context.Background(), p.client, "getUpdates", nil, &updates)
				if err == nil || p.failed(err) || p.transport != original || p.networkFailures != 0 {
					t.Fatal("rejected or malformed response reset polling", err)
				}
			}
		})
	}
}

type telegramCustomPollTransport struct {
	closes atomic.Int32
	calls  atomic.Int32
}

func (t *telegramCustomPollTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("injected offline transport")
}
func (t *telegramCustomPollTransport) CloseIdleConnections() { t.closes.Add(1) }

func TestTelegramPollPreservesCustomTransport(t *testing.T) {
	transport := &telegramCustomPollTransport{}
	base := &http.Client{Transport: transport}
	p := newTelegramPoll(base)
	if p.transport != nil || p.client.Transport != transport {
		t.Fatal("custom transport replaced")
	}
	for range 4 {
		req, err := http.NewRequest("POST", "https://must-not-use-real-network.invalid/getUpdates", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.client.Do(req); err == nil {
			t.Fatal("injected failure lost")
		}
		if p.failed(&networkFailure{cause: err}) || p.client.Transport != transport {
			t.Fatal("custom transport reset or replaced by real network")
		}
	}
	p.close()
	if transport.closes.Load() != 0 || transport.calls.Load() != 4 || base.Transport != transport {
		t.Fatal("shared custom transport was closed or bypassed")
	}
}

func TestTelegramPollShutdownDoesNotLogFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newTestEngine(t, nil)
		log, err := openEventLog(e.Dir)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		e.Events = log
		var canceled atomic.Bool
		tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/getUpdates") {
				<-r.Context().Done()
				canceled.Store(true)
				return nil, r.Context().Err()
			}
			return telegramFixtureResponse(200), nil
		})})
		ctx, cancel := context.WithCancel(e.ctx)
		done := make(chan struct{})
		go func() { defer close(done); tg.Run(ctx) }()
		synctest.Wait()
		cancel()
		<-done
		raw, err := os.ReadFile(filepath.Join(e.Dir, "logs", "alina.jsonl"))
		if err != nil || !canceled.Load() || strings.Contains(string(raw), "telegram.poll_failed") || strings.Contains(string(raw), "telegram.poll_transport_reset") {
			t.Fatal("shutdown logged a failure or reset polling", canceled.Load(), string(raw), err)
		}
	})
}

// Exercise real TLS connections and Run's durable update offset, rather than
// proving recovery only by inspecting a replacement transport pointer.
func TestTelegramPollReconnectsWithoutResettingSharedClient(t *testing.T) {
	e := newTestEngine(t, nil)
	log, err := openEventLog(e.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	e.Events = log
	type connectionKey struct{}
	var connectionID atomic.Int64
	var mu sync.Mutex
	var offsets, pollConnections, sharedConnections []int64
	polled := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Context().Value(connectionKey{}).(int64)
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var request struct {
				Offset  int64 `json:"offset"`
				Timeout int   `json:"timeout"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Timeout != 50 {
				t.Error("invalid poll", request, err)
			}
			mu.Lock()
			offsets = append(offsets, request.Offset)
			pollConnections = append(pollConnections, id)
			call := len(offsets)
			mu.Unlock()
			switch call {
			case 1, 4:
				updateID := 40
				if call == 4 {
					updateID = 41
				}
				fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":%d,"message":{"message_id":1,"text":"/help","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, updateID)
			case 2, 3:
				// Interrupt only the response body. The HTTP/2 connection
				// remains pooled until Alina resets its polling transport.
				w.Header().Set("Content-Length", "128")
				io.WriteString(w, `{"ok":true`)
			default:
				close(polled)
				<-r.Context().Done()
			}
			return
		}
		mu.Lock()
		sharedConnections = append(sharedConnections, id)
		mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{"message_id":99}}`)
	}))
	server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connectionKey{}, connectionID.Add(1))
	}
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	base := server.Client()
	defer base.CloseIdleConnections()
	transport := base.Transport.(*http.Transport)
	transport.TLSClientConfig.InsecureSkipVerify = false
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	transport.TLSClientConfig.RootCAs.AddCert(server.Certificate())
	base.Timeout = 77 * time.Second
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	var response json.RawMessage
	if err := requestJSON(ctx, base, "POST", server.URL+"/provider", nil, nil, &response); err != nil {
		t.Fatal(err)
	}
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture-secret", OwnerID: 42}, e, base)
	tg.BaseURL = server.URL
	done := make(chan struct{})
	go func() { defer close(done); tg.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-polled:
	case <-ctx.Done():
		t.Fatal("poll recovery stalled", ctx.Err())
	}
	cancel()
	<-done
	// Closing the dedicated polling client must also leave the shared pool usable.
	if err := requestJSON(e.ctx, base, "POST", server.URL+"/provider", nil, nil, &response); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if fmt.Sprint(offsets) != "[0 41 41 41 42]" || len(pollConnections) != 5 || pollConnections[0] != pollConnections[1] || pollConnections[1] != pollConnections[2] || pollConnections[0] == pollConnections[3] {
		t.Error("offset replay or no real reconnection", offsets, pollConnections)
	}
	for _, id := range sharedConnections {
		if id != sharedConnections[0] || id == pollConnections[0] || id == pollConnections[3] {
			t.Error("polling reset or shared the provider/notification pool", sharedConnections, pollConnections)
			break
		}
	}
	mu.Unlock()
	stored := NewTelegram(e.Dir, tg.Config, e, base)
	if stored.state.Offset != 42 || base.Transport != transport || base.Timeout != 77*time.Second {
		t.Fatal("checkpoint or shared client changed", stored.state.Offset)
	}
	raw, err := os.ReadFile(filepath.Join(e.Dir, "logs", "alina.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event struct {
			Message  string `json:"msg"`
			Failures int    `json:"consecutive_failures"`
			Protocol string `json:"protocol"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		counts[event.Message]++
		if (event.Message == "telegram.poll_failed" || event.Message == "telegram.poll_recovered") && event.Protocol != "h2" {
			t.Error("polling stopped using HTTP/2", event)
		}
		if event.Message == "telegram.poll_recovered" && event.Failures != 2 {
			t.Error("recovery lost failure count", event)
		}
	}
	if counts["telegram.poll_failed"] != 2 || counts["telegram.poll_transport_reset"] != 1 || counts["telegram.poll_recovered"] != 1 || strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), server.URL) {
		t.Fatal("wrong or sensitive recovery logs", counts, string(raw))
	}
}
