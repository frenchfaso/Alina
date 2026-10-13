package alina

import (
	"strings"
	"time"
)

// Retain structured provider diagnostics without exposing remote messages,
// which may contain prompts or account details. Filter metadata at every
// output boundary; an invalid nonempty code must never enable the type fallback.
type providerResponseError struct {
	Event, Code, Type, Param, Reason, RequestID string
}

func providerMetadata(value string) string {
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return value
}

func (e *providerResponseError) metadata() map[string]any {
	out := map[string]any{}
	for _, field := range e.fields() {
		if value := providerMetadata(field.value); value != "" {
			out[field.name] = value
		}
	}
	return out
}

func (e *providerResponseError) fields() []struct{ name, value string } {
	return []struct{ name, value string }{
		{"event", e.Event}, {"code", e.Code}, {"type", e.Type},
		{"param", e.Param}, {"reason", e.Reason}, {"request_id", e.RequestID},
	}
}

func (e *providerResponseError) Error() string {
	text := "provider response failed"
	if e.Event == "response.incomplete" {
		text = "provider response incomplete"
	} else if e.Event == "stream.interrupted" {
		text = "model stream ended before completion"
	}
	fields := []string{}
	for _, field := range e.fields() {
		if value := providerMetadata(field.value); value != "" {
			fields = append(fields, field.name+"="+value)
		}
	}
	if len(fields) != 0 {
		text += " (" + strings.Join(fields, ", ") + ")"
	}
	return text
}

func (e *providerResponseError) retryable() bool {
	if e.Event != "response.failed" && e.Event != "error" || e.Reason != "" {
		return false
	}
	switch e.Code {
	case "server_error", "internal_server_error", "temporarily_unavailable", "service_unavailable", "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return true
	case "":
		return e.Type == "server_error"
	default:
		return false
	}
}

type providerErrorDetail struct {
	Code  string `json:"code"`
	Type  string `json:"type"`
	Param string `json:"param"`
}

func responseFailure(event string, body responseBody, detail *providerErrorDetail, requestID string) *providerResponseError {
	if body.Status == "incomplete" {
		event = "response.incomplete"
	}
	e := &providerResponseError{Event: event, RequestID: requestID}
	if body.IncompleteDetails != nil {
		e.Reason = body.IncompleteDetails.Reason
	}
	if body.Error != nil {
		combined := *body.Error
		if detail != nil {
			if combined.Code == "" {
				combined.Code = detail.Code
			}
			if combined.Type == "" {
				combined.Type = detail.Type
			}
			if combined.Param == "" {
				combined.Param = detail.Param
			}
		}
		detail = &combined
	}
	if detail != nil {
		e.Code, e.Type, e.Param = detail.Code, detail.Type, detail.Param
	}
	return e
}

func responseUsage(body responseBody, started time.Time) *TokenUsage {
	u := body.Usage
	if u == nil {
		return nil
	}
	return &TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedTokens: u.InputDetails.CachedTokens, CacheWriteTokens: u.InputDetails.CacheWriteTokens, ReasoningTokens: u.OutputDetails.ReasoningTokens, DurationMS: time.Since(started).Milliseconds()}
}
