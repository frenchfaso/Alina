package alina

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func newHTTPClient() *http.Client {
	return &http.Client{Transport: hostHTTPTransport(), Timeout: 180 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

type networkFailure struct {
	cause             error
	trace             *networkTrace
	standardTransport bool
	contextDone       bool
}

func (e *networkFailure) Error() string { return "network request failed" }
func (e *networkFailure) Unwrap() error { return e.cause }

func newNetworkFailure(ctx context.Context, client *http.Client, cause error) *networkFailure {
	trace, _ := ctx.Value(networkTraceKey{}).(*networkTrace)
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	_, standard := transport.(*http.Transport)
	return &networkFailure{cause: cause, trace: trace, standardTransport: standard, contextDone: ctx.Err() != nil}
}

func (e *networkFailure) retryable() bool {
	if e.contextDone || !e.standardTransport || e.trace == nil || !e.trace.requestNotSent() || errors.Is(e.cause, context.Canceled) {
		return false
	}
	var dns *net.DNSError
	if errors.As(e.cause, &dns) && dns.IsNotFound {
		return false
	}
	var network net.Error
	return errors.As(e.cause, &network) && (network.Timeout() || network.Temporary())
}

type remoteHTTPError struct{ Status int }

func (e *remoteHTTPError) Error() string {
	return fmt.Sprintf("remote service returned HTTP %d", e.Status)
}

func requestJSON(ctx context.Context, client *http.Client, method, url string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, url, reader)
	if e != nil {
		return e
	}
	req.Header.Set("User-Agent", "alina/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newNetworkFailure(ctx, client, e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &remoteHTTPError{Status: resp.StatusCode}
	}
	return decodeLimited(resp.Body, out)
}
func decodeLimited(r io.Reader, out any) error {
	b, e := io.ReadAll(io.LimitReader(r, 8<<20+1))
	if e != nil {
		return &networkFailure{cause: e}
	}
	if len(b) > 8<<20 {
		return fmt.Errorf("response exceeds 8 MiB")
	}
	return json.Unmarshal(b, out)
}
