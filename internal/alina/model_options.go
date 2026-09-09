package alina

import (
	"context"
	"net/http"
	"time"
)

// A checkpoint is a separate request; ordinary turns keep a stable effort.
type reasoningEffortKey struct{}

func (p *Provider) reasoningEffort(ctx context.Context) string {
	if effort, ok := ctx.Value(reasoningEffortKey{}).(string); ok {
		return effort
	}
	if p.Config.ReasoningEffort != "" {
		return p.Config.ReasoningEffort
	}
	return "medium"
}

func (p *Provider) modelClient() *http.Client {
	base := p.Client
	if base == nil {
		base = newHTTPClient()
	}
	client := *base // Do not change the shared auth/search/Telegram client.
	seconds := p.Config.ModelTimeout
	if seconds == 0 {
		seconds = 600
	}
	client.Timeout = time.Duration(seconds) * time.Second
	return &client
}

// Provider-reported tokens, never inferred from a subscription's allowance.
type TokenUsage struct {
	InputTokens      int   `json:"input_tokens"`
	OutputTokens     int   `json:"output_tokens"`
	CachedTokens     int   `json:"cached_tokens"`
	CacheWriteTokens int   `json:"cache_write_tokens"`
	ReasoningTokens  int   `json:"reasoning_tokens"`
	DurationMS       int64 `json:"duration_ms"`
}

func (u *TokenUsage) add(v TokenUsage) {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CachedTokens += v.CachedTokens
	u.CacheWriteTokens += v.CacheWriteTokens
	u.ReasoningTokens += v.ReasoningTokens
	u.DurationMS += v.DurationMS
}

// Saved with a working response, never sent to the provider. Compaction clears
// samples because their measured input describes the pre-compaction history.
type contextSample struct {
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	PrefixTokens int    `json:"prefix_tokens"`
}

func (e *Engine) historyTokens(history []Message, models ...string) int {
	model := e.Config.Model
	if len(models) > 0 && models[0] != "" {
		model = models[0]
	}
	for i := len(history) - 1; i >= 0; i-- {
		if sample := history[i].Context; sample != nil && sample.Model == model && sample.InputTokens > 0 {
			return max(0, sample.InputTokens-sample.PrefixTokens) + sample.OutputTokens + estimatedTokens(history[i+1:])
		}
	}
	return estimatedTokens(history)
}
