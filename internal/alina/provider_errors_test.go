package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestProviderResponseErrors(t *testing.T) {
	const items = "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"touch sentinel\\\"}\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"PRIVATE_PARTIAL_OUTPUT\"}]}}\n\n"
	for _, tc := range []struct {
		name, terminal, contentType      string
		event, code, kind, param, reason string
		retry, usage                     bool
	}{
		{name: "nested failed", terminal: `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","type":"server_error","param":"tools.0","message":"PRIVATE_REMOTE_MESSAGE"},"usage":{"input_tokens":12,"output_tokens":4,"output_tokens_details":{"reasoning_tokens":3}}}}`, event: "response.failed", code: "server_error", kind: "server_error", param: "tools.0", retry: true, usage: true},
		{name: "subscription unavailable", terminal: `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_unavailable","message":"PRIVATE_REMOTE_MESSAGE"}}}`, event: "response.failed", code: "subscription_sharing_usage_unavailable", retry: true},
		{name: "incomplete", terminal: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"error":{"type":"server_error"},"usage":{"input_tokens":12,"output_tokens":4,"output_tokens_details":{"reasoning_tokens":3}}}}`, event: "response.incomplete", kind: "server_error", reason: "max_output_tokens", usage: true},
		{name: "root error", terminal: `{"type":"error","error":{"code":"internal_server_error","type":"server_error","param":"model","message":"PRIVATE_REMOTE_MESSAGE"}}`, event: "error", code: "internal_server_error", kind: "server_error", param: "model", retry: true},
		{name: "top level error", terminal: `{"type":"error","code":"service_unavailable","param":"model","message":"PRIVATE_REMOTE_MESSAGE"}`, event: "error", code: "service_unavailable", param: "model", retry: true},
		{name: "type fallback", terminal: `{"type":"error","error":{"type":"server_error","message":"PRIVATE_REMOTE_MESSAGE"}}`, event: "error", kind: "server_error", retry: true},
		{name: "unknown code", terminal: `{"type":"error","error":{"code":"unknown_error","type":"server_error"}}`, event: "error", code: "unknown_error", kind: "server_error"},
		{name: "top level quota overrides type fallback", terminal: `{"type":"error","code":"insufficient_quota","error":{"type":"server_error"}}`, event: "error", code: "insufficient_quota", kind: "server_error"},
		{name: "JSON failed", contentType: "application/json", terminal: `{"status":"failed","error":{"code":"temporarily_unavailable","type":"server_error","param":"model","message":"PRIVATE_REMOTE_MESSAGE"},"usage":{"input_tokens":12,"output_tokens":4,"output_tokens_details":{"reasoning_tokens":3}}}`, event: "response.failed", code: "temporarily_unavailable", kind: "server_error", param: "model", retry: true, usage: true},
		{name: "JSON incomplete", contentType: "application/json", terminal: `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"error":{"code":"server_error"}}`, event: "response.incomplete", code: "server_error", reason: "content_filter"},
		{name: "completed incomplete envelope", terminal: `{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, event: "response.incomplete", reason: "max_output_tokens"},
		{name: "completed failed envelope", terminal: `{"type":"response.completed","response":{"status":"failed","error":{"code":"server_error"}}}`, event: "response.failed", code: "server_error", retry: true},
		{name: "EOF after completed items", event: "stream.interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-request-id", "req-safe_123")
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
					fmt.Fprint(w, tc.terminal)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, items)
				if tc.terminal != "" {
					fmt.Fprint(w, "data: "+tc.terminal+"\n\n")
				}
			}))
			defer srv.Close()
			p := Provider{Client: srv.Client()}
			m, err := p.responses(context.Background(), srv.URL, "test", "key", "account", "session", nil, nil, false, nil)
			var failure *providerResponseError
			if !errors.As(err, &failure) {
				t.Fatalf("expected structured provider failure, got %v", err)
			}
			if failure.Event != tc.event || failure.Code != tc.code || failure.Type != tc.kind || failure.Param != tc.param || failure.Reason != tc.reason || failure.RequestID != "req-safe_123" || failure.retryable() != tc.retry {
				t.Fatalf("wrong provider diagnostics: %+v, retry=%v", failure, failure.retryable())
			}
			if len(m.Calls) != 0 || len(m.Raw) != 0 || m.Content != "" {
				t.Fatal("accepted output or tools from a failed stream")
			}
			if tc.usage {
				if m.Usage == nil || m.Usage.InputTokens != 12 || m.Usage.OutputTokens != 4 || m.Usage.ReasoningTokens != 3 {
					t.Fatal("lost usage from the failed response", m.Usage)
				}
			} else if m.Usage != nil {
				t.Fatal("invented usage for a failed response")
			}
			info := errorInfo(fmt.Errorf("PRIVATE_WRAPPER: %w", err))
			if info["code"] != "provider_response_error" || !reflect.DeepEqual(info["provider"], failure.metadata()) {
				t.Fatal("wrong log classification or metadata", info)
			}
			if strings.Contains(err.Error()+jsonText(info), "PRIVATE_") || strings.Contains(err.Error()+jsonText(info), "touch sentinel") {
				t.Fatal("remote message or partial output leaked into diagnostics")
			}
		})
	}
}

func TestProviderResponseRetryAllowlist(t *testing.T) {
	for _, code := range []string{"server_error", "internal_server_error", "temporarily_unavailable", "service_unavailable", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable"} {
		for _, event := range []string{"response.failed", "error"} {
			if !(&providerResponseError{Event: event, Code: code}).retryable() {
				t.Fatalf("transient failure not retryable: %s %s", event, code)
			}
		}
	}
	for _, tc := range []providerResponseError{
		{Event: "response.incomplete", Code: "server_error"},
		{Event: "stream.interrupted", Code: "server_error"},
		{Event: "response.completed", Code: "server_error"},
		{Event: "response.failed", Type: "unknown_error"},
		{Event: "response.failed", Code: "server_error", Reason: "max_output_tokens"},
		{Event: "response.failed", Code: "server_error", Reason: "content_filter"},
	} {
		if tc.retryable() {
			t.Fatalf("unsafe failure retryable: %+v", tc)
		}
	}
	for _, code := range []string{"rate_limit_exceeded", "insufficient_quota", "invalid_api_key", "model_not_found", "max_output_tokens", "content_filter", "unknown_error", "server_error\nPRIVATE_TOKEN"} {
		if (&providerResponseError{Event: "error", Code: code, Type: "server_error"}).retryable() {
			t.Fatalf("unknown or permanent code accepted through type fallback: %q", code)
		}
	}
}

func TestProviderErrorMetadataRedaction(t *testing.T) {
	for _, value := range []string{"server_error\nPRIVATE_TOKEN", "private message", "https://example.com/token", "token=value", "mémoire", strings.Repeat("a", 129)} {
		failure := &providerResponseError{Event: "response.failed", Code: value, Type: "server_error", Param: value, Reason: value, RequestID: value}
		want := map[string]any{"event": "response.failed", "type": "server_error"}
		if !reflect.DeepEqual(failure.metadata(), want) || strings.Contains(failure.Error(), value) || failure.retryable() {
			t.Fatalf("unsafe provider metadata retained or retry enabled: %q", value)
		}
	}
	for _, value := range []string{"request_123-456.abc", strings.Repeat("a", 128)} {
		if providerMetadata(value) != value {
			t.Fatalf("safe bounded token rejected: %q", value)
		}
	}
}

func TestProviderErrorMalformedJSON(t *testing.T) {
	for _, terminal := range []string{`{"type":"response.failed",`, `{"type":"error","error":{"code":42,"type":"server_error"}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+terminal+"\n\n")
		}))
		p := Provider{Client: srv.Client()}
		m, err := p.responses(context.Background(), srv.URL, "test", "key", "account", "session", nil, nil, false, nil)
		srv.Close()
		var failure *providerResponseError
		var syntax *json.SyntaxError
		var mismatch *json.UnmarshalTypeError
		if err == nil || errors.As(err, &failure) || !(errors.As(err, &syntax) || errors.As(err, &mismatch)) || len(m.Calls) != 0 {
			t.Fatalf("malformed JSON accepted as a retryable provider event: %v", err)
		}
	}
}

func TestProviderRequestIDRedaction(t *testing.T) {
	for _, id := range []string{"PRIVATE request data", strings.Repeat("a", 129)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-request-id", id)
			fmt.Fprint(w, `{"status":"failed","error":{"code":"server_error"}}`)
		}))
		p := Provider{Client: srv.Client()}
		_, err := p.responses(context.Background(), srv.URL, "test", "key", "account", "session", nil, nil, false, nil)
		srv.Close()
		var failure *providerResponseError
		if !errors.As(err, &failure) || failure.RequestID != "" || strings.Contains(err.Error(), id) || strings.Contains(jsonText(errorInfo(err)), id) {
			t.Fatal("unsafe request ID exposed", err)
		}
	}
}
