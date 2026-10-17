package alina

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type temporaryNetworkFault struct{ timeout bool }

func (e temporaryNetworkFault) Error() string   { return "PRIVATE_NETWORK_ERROR" }
func (e temporaryNetworkFault) Timeout() bool   { return e.timeout }
func (e temporaryNetworkFault) Temporary() bool { return true }

func TestNetworkRecoveryIsBoundedToTemporaryUnsentRequests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  bool
	}{
		{"DNS temporary", &net.DNSError{Err: "PRIVATE_DNS", Name: "PRIVATE_HOST", IsTemporary: true}, true},
		{"DNS timeout", &net.DNSError{Err: "PRIVATE_DNS", IsTimeout: true}, true},
		{"connect timeout", &net.OpError{Op: "dial", Net: "tcp", Err: temporaryNetworkFault{timeout: true}}, true},
		{"wrapped temporary", &url.Error{Op: "Post", URL: "https://PRIVATE_HOST/PRIVATE_TOKEN", Err: temporaryNetworkFault{}}, true},
		{"DNS not found", &net.DNSError{Err: "PRIVATE_DNS", IsNotFound: true, IsTemporary: true}, false},
		{"permanent", errors.New("PRIVATE_ERROR"), false},
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, false},
		{"cancelled", fmt.Errorf("wrapped: %w", context.Canceled), false},
		{"internal dial deadline", &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := newNetworkTrace()
			ctx := trace.context(context.Background())
			if ctx.Value(networkTraceKey{}) != trace || trace.used() {
				t.Fatal("trace was not stored or unused trace was reported active")
			}
			hooks := httptrace.ContextClientTrace(ctx)
			hooks.GetConn("PRIVATE_HOST")
			hooks.DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_HOST"})
			failure := newNetworkFailure(ctx, &http.Client{Transport: &http.Transport{}}, tc.cause)
			if failure.retryable() != tc.want || !errors.Is(failure, tc.cause) || !trace.used() {
				t.Fatal("wrong recovery classification or lost cause", failure.retryable(), tc.want)
			}
			if strings.Contains(failure.Error(), "PRIVATE") || strings.Contains(fmt.Sprint(trace.fields()), "PRIVATE") {
				t.Fatal("network diagnostics exposed private information")
			}
		})
	}
}

func TestNetworkRecoverySnapshotsCallingContextAtFailure(t *testing.T) {
	for _, mode := range []string{"cancelled", "expired", "cancelled after failure"} {
		t.Run(mode, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "expired" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			trace := newNetworkTrace()
			ctx = trace.context(ctx)
			hooks := httptrace.ContextClientTrace(ctx)
			hooks.GetConn("PRIVATE_HOST")
			if mode == "cancelled" {
				cancel()
			}
			failure := newNetworkFailure(ctx, &http.Client{}, &net.OpError{Op: "dial", Err: context.DeadlineExceeded})
			if mode == "cancelled after failure" {
				// inferOnce cancels its derived worker context when returning; the
				// classifier must retain the state when the transport actually failed.
				cancel()
			}
			if want := mode == "cancelled after failure"; failure.retryable() != want {
				t.Fatal("calling context was not snapshotted at the failure", failure.retryable(), want)
			}
		})
	}
}

func TestNetworkRecoveryNeverForgetsEarlierDispatch(t *testing.T) {
	for _, stage := range []string{"connection", "write", "failed write", "response"} {
		t.Run(stage, func(t *testing.T) {
			trace := newNetworkTrace()
			ctx := trace.context(context.Background())
			hooks := httptrace.ContextClientTrace(ctx)
			hooks.GetConn("PRIVATE_HOST")
			switch stage {
			case "connection":
				hooks.GotConn(httptrace.GotConnInfo{Reused: true})
			case "write":
				hooks.WroteRequest(httptrace.WroteRequestInfo{})
			case "failed write":
				hooks.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})
			case "response":
				hooks.GotFirstResponseByte()
			}
			// The transport may try another connection; that cannot erase evidence
			// that an earlier attempt could already have reached the provider.
			hooks.GetConn("PRIVATE_HOST")
			hooks.DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_HOST"})
			failure := newNetworkFailure(ctx, &http.Client{}, temporaryNetworkFault{})
			if failure.retryable() || trace.requestNotSent() {
				t.Fatal("later dial erased an earlier dispatch")
			}
		})
	}
}

func TestNetworkRecoveryLateCallbacksOnlyRestrictRetry(t *testing.T) {
	for _, stage := range []string{"connection", "write", "failed write", "response"} {
		t.Run(stage, func(t *testing.T) {
			trace := newNetworkTrace()
			ctx := trace.context(context.Background())
			hooks := httptrace.ContextClientTrace(ctx)
			hooks.GetConn("PRIVATE_HOST")
			hooks.DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_HOST"})
			before := traceMetadata(trace)
			failure := newNetworkFailure(ctx, &http.Client{}, temporaryNetworkFault{})
			if !failure.retryable() {
				t.Fatal("temporary preconnection error was not eligible")
			}
			switch stage {
			case "connection":
				hooks.GotConn(httptrace.GotConnInfo{})
			case "write":
				hooks.WroteRequest(httptrace.WroteRequestInfo{})
			case "failed write":
				hooks.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})
			case "response":
				hooks.GotFirstResponseByte()
			}
			hooks.GetConn("PRIVATE_HOST")
			if failure.retryable() || !reflect.DeepEqual(before, traceMetadata(trace)) {
				t.Fatal("late callback allowed retry or changed frozen diagnostics")
			}
		})
	}
	trace := newNetworkTrace()
	hooks := httptrace.ContextClientTrace(trace.context(context.Background()))
	if trace.requestNotSent() {
		t.Fatal("unobserved request was considered unsent")
	}
	hooks.GetConn("PRIVATE_HOST")
	if trace.requestNotSent() {
		t.Fatal("late GetConn made retry more permissive")
	}
}

type recoveryRoundTripper struct{ cause error }

func (r recoveryRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, r.cause
}

func TestNetworkRecoveryRequiresStandardTransportAndObservedAttempt(t *testing.T) {
	trace := newNetworkTrace()
	ctx := trace.context(context.Background())
	client := &http.Client{Transport: recoveryRoundTripper{cause: temporaryNetworkFault{}}}
	err := requestJSON(ctx, client, "POST", "https://PRIVATE_HOST/PRIVATE_TOKEN", nil, nil, &map[string]any{})
	var failure *networkFailure
	if !errors.As(err, &failure) || failure.retryable() || trace.used() {
		t.Fatal("custom untraced transport was eligible for retry", err)
	}
	hooks := httptrace.ContextClientTrace(ctx)
	hooks.GetConn("PRIVATE_HOST")
	if newNetworkFailure(ctx, client, temporaryNetworkFault{}).retryable() {
		t.Fatal("custom transport acquired retry authority through callbacks")
	}
	if newNetworkFailure(context.Background(), &http.Client{}, temporaryNetworkFault{}).retryable() {
		t.Fatal("missing trace was eligible for retry")
	}
	if newNetworkFailure(newNetworkTrace().context(context.Background()), &http.Client{}, temporaryNetworkFault{}).retryable() {
		t.Fatal("standard transport without an observed attempt was eligible for retry")
	}
	if (&networkFailure{cause: temporaryNetworkFault{}}).retryable() {
		t.Fatal("untraced response-read error was eligible for retry")
	}
}
