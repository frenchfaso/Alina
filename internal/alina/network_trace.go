package alina

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// Keep only progress metadata: Telegram URLs contain credentials, and trace
// callbacks can run concurrently or arrive after the request has failed.
type networkTrace struct {
	mu       sync.Mutex
	started  time.Time
	stage    int
	gotConn  bool
	reused   bool
	protocol string
	frozen   bool
	elapsed  int64
}

var networkStages = [...]string{"connection", "dns", "connect", "tls", "write", "response", "read_response"}

func newNetworkTrace() *networkTrace { return &networkTrace{started: time.Now()} }

func (t *networkTrace) advance(stage int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.frozen && stage > t.stage {
		t.stage = stage
	}
}

func (t *networkTrace) context(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.frozen {
				t.stage, t.gotConn, t.reused, t.protocol = 0, false, false, ""
			}
		},
		DNSStart: func(httptrace.DNSStartInfo) { t.advance(1) },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err == nil {
				t.advance(2)
			}
		},
		ConnectStart:      func(string, string) { t.advance(2) },
		TLSHandshakeStart: func() { t.advance(3) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.frozen {
				t.stage, t.gotConn, t.reused = 4, true, info.Reused
				if conn, ok := info.Conn.(*tls.Conn); ok {
					protocol := conn.ConnectionState().NegotiatedProtocol
					if protocol == "h2" || protocol == "http/1.1" {
						t.protocol = protocol
					}
				}
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.advance(5)
			}
		},
		GotFirstResponseByte: func() { t.advance(6) },
	})
}

func (t *networkTrace) fields() []any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.frozen {
		t.frozen, t.elapsed = true, time.Since(t.started).Milliseconds()
	}
	fields := []any{"stage", networkStages[t.stage], "elapsed_ms", t.elapsed}
	if t.gotConn {
		fields = append(fields, "connection_reused", t.reused)
	}
	if t.protocol != "" {
		fields = append(fields, "protocol", t.protocol)
	}
	return fields
}
