package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Message struct {
	ArchiveID   string            `json:"archive_id,omitempty"`
	Role        string            `json:"role"`
	Content     string            `json:"content"`
	Calls       []ToolCall        `json:"calls,omitempty"`
	CallID      string            `json:"call_id,omitempty"`
	Raw         []json.RawMessage `json:"raw,omitempty"`
	Reasoning   string            `json:"reasoning,omitempty"`
	Runtime     bool              `json:"runtime,omitempty"`
	Usage       *TokenUsage       `json:"usage,omitempty"`
	Context     *contextSample    `json:"context_sample,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	imageInputs []string
}
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}
type Model interface {
	Complete(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error)
}
type Provider struct {
	Config    Config
	Auth      *Auth
	Client    *http.Client
	BaseURL   string
	Workspace string
}

func (p *Provider) Complete(ctx context.Context, session string, msg []Message, tools []ToolSpec, delta func(string)) (Message, error) {
	if p.Config.Provider == "chatgpt" {
		c, e := p.Auth.Get(ctx)
		if e != nil {
			return Message{}, e
		}
		return p.responses(ctx, p.endpoint("https://chatgpt.com/backend-api/codex/responses"), p.Config.Model, c.Access, c.AccountID, session, msg, tools, false, delta)
	}
	if p.Config.OpenCodeKey == "" {
		return Message{}, errors.New("OpenCode Go key missing: alina setup")
	}
	switch p.Config.OpenCodeAPI {
	case "responses":
		return p.responses(ctx, p.endpoint("https://opencode.ai/zen/go/v1/responses"), p.Config.Model, p.Config.OpenCodeKey, "", session, msg, tools, false, delta)
	case "messages":
		return p.anthropic(ctx, session, msg, tools)
	default:
		return p.chat(ctx, session, msg, tools)
	}
}
func (p *Provider) endpoint(fallback string) string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return fallback
}
func (p *Provider) headers(key, session string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + key, "x-opencode-session": session}
}
func (p *Provider) chat(ctx context.Context, session string, msg []Message, tools []ToolSpec) (Message, error) {
	messages := []any{}
	for _, m := range msg {
		j := map[string]any{"role": m.Role, "content": nonVisualText(m)}
		if m.CallID != "" {
			j["tool_call_id"] = m.CallID
		}
		if m.Reasoning != "" {
			j["reasoning_content"] = m.Reasoning
		}
		if len(m.Calls) > 0 {
			calls := []any{}
			for _, c := range m.Calls {
				calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Arguments}})
			}
			j["tool_calls"] = calls
		}
		messages = append(messages, j)
	}
	ts := []any{}
	for _, t := range tools {
		ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
	}
	body := map[string]any{"model": p.Config.Model, "messages": messages, "stream": false}
	if len(ts) > 0 {
		body["tools"] = ts
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
				Calls     []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	e := requestJSON(ctx, p.modelClient(), "POST", p.endpoint("https://opencode.ai/zen/go/v1/chat/completions"), body, p.headers(p.Config.OpenCodeKey, session), &r)
	if e != nil {
		return Message{}, e
	}
	if len(r.Choices) == 0 {
		return Message{}, errors.New("provider returned no choices")
	}
	m := r.Choices[0].Message
	out := Message{Role: "assistant", Content: m.Content, Reasoning: m.Reasoning}
	for _, c := range m.Calls {
		out.Calls = append(out.Calls, ToolCall{c.ID, c.Function.Name, c.Function.Arguments})
	}
	return out, nil
}
func (p *Provider) anthropic(ctx context.Context, session string, msg []Message, tools []ToolSpec) (Message, error) {
	var system string
	messages := []any{}
	for _, m := range msg {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		role := m.Role
		blocks := []any{}
		if role == "tool" {
			role = "user"
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": m.CallID, "content": nonVisualText(m)})
		} else {
			if len(m.Raw) > 0 && m.Role == "assistant" {
				for _, raw := range m.Raw {
					var b any
					if json.Unmarshal(raw, &b) == nil {
						blocks = append(blocks, b)
					}
				}
			} else {
				if m.Content != "" || len(m.Attachments) > 0 {
					blocks = append(blocks, map[string]any{"type": "text", "text": nonVisualText(m)})
				}
				for _, c := range m.Calls {
					var input any
					if json.Unmarshal([]byte(c.Arguments), &input) != nil {
						return Message{}, errors.New("invalid tool arguments")
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": input})
				}
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	ts := []any{}
	for _, t := range tools {
		ts = append(ts, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
	}
	body := map[string]any{"model": p.Config.Model, "system": system, "messages": messages, "max_tokens": 4096}
	if len(ts) > 0 {
		body["tools"] = ts
	}
	var r struct {
		Content []json.RawMessage `json:"content"`
	}
	h := p.headers(p.Config.OpenCodeKey, session)
	h["x-api-key"] = p.Config.OpenCodeKey
	h["anthropic-version"] = "2023-06-01"
	if e := requestJSON(ctx, p.modelClient(), "POST", p.endpoint("https://opencode.ai/zen/go/v1/messages"), body, h, &r); e != nil {
		return Message{}, e
	}
	out := Message{Role: "assistant", Raw: r.Content}
	for _, b := range r.Content {
		var j struct {
			Type, Text, ID, Name string
			Input                json.RawMessage
		}
		if e := json.Unmarshal(b, &j); e != nil {
			return Message{}, e
		}
		if j.Type == "text" {
			out.Content += j.Text
		}
		if j.Type == "tool_use" {
			out.Calls = append(out.Calls, ToolCall{j.ID, j.Name, string(j.Input)})
		}
	}
	return out, nil
}

func responseInput(msg []Message) (string, []any) {
	var system string
	input := []any{}
	for _, m := range msg {
		text := messageText(m)
		var content any = text
		if len(m.imageInputs) > 0 {
			blocks := []any{map[string]any{"type": "input_text", "text": text}}
			for _, data := range m.imageInputs {
				blocks = append(blocks, map[string]any{"type": "input_image", "image_url": data, "detail": "high"})
			}
			content = blocks
		}
		if m.Role == "system" {
			system = m.Content
			continue
		}
		if m.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.CallID, "output": content})
			continue
		}
		if m.Role == "assistant" && len(m.Raw) > 0 {
			for _, raw := range m.Raw {
				input = append(input, raw)
			}
			continue
		}
		if text != "" || len(m.imageInputs) > 0 {
			input = append(input, map[string]any{"role": m.Role, "content": content})
		}
		for _, c := range m.Calls {
			input = append(input, map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
		}
	}
	return system, input
}

type responseBody struct {
	Output []json.RawMessage `json:"output"`
	Status string            `json:"status"`
	Usage  *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		InputDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Provider) responses(ctx context.Context, endpoint, model, key, account, session string, msg []Message, tools []ToolSpec, search bool, delta func(string)) (Message, error) {
	started := time.Now()
	system, input := responseInput(p.prepareImages(msg))
	ts := []any{}
	for _, t := range tools {
		ts = append(ts, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters, "strict": false})
	}
	if search {
		ts = append(ts, map[string]any{"type": "web_search"})
	}
	body := map[string]any{"model": model, "instructions": system, "input": input, "store": false, "stream": true, "tools": ts}
	if model == defaultModel {
		body["reasoning"] = map[string]any{"effort": p.reasoningEffort(ctx)}
		verbosity := p.Config.Verbosity
		if verbosity == "" {
			verbosity = "low"
		}
		body["text"] = map[string]any{"verbosity": verbosity}
		body["prompt_cache_key"] = "alina-" + session
	}
	if account != "" {
		body["include"] = []string{"reasoning.encrypted_content"}
		body["parallel_tool_calls"] = false
	}
	if search {
		body["tool_choice"] = "required"
	}
	b, e := json.Marshal(body)
	if e != nil {
		return Message{}, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	if e != nil {
		return Message{}, e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "alina/"+Version)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-opencode-session", session)
	if account != "" {
		req.Header.Set("ChatGPT-Account-ID", account)
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "alina")
		req.Header.Set("session-id", session)
	}
	r, e := p.modelClient().Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, errors.New("model connection failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return Message{}, fmt.Errorf("model returned HTTP %d (check account, model and endpoint)", r.StatusCode)
	}
	var result responseBody
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if e = decodeLimited(r.Body, &result); e != nil {
			return Message{}, e
		}
	} else {
		done := false
		e = readSSE(r.Body, func(data []byte) error {
			if string(data) == "[DONE]" {
				return nil
			}
			var ev struct {
				Type     string       `json:"type"`
				Delta    string       `json:"delta"`
				Response responseBody `json:"response"`
			}
			if e := json.Unmarshal(data, &ev); e != nil {
				return e
			}
			switch ev.Type {
			case "response.output_text.delta":
				if delta != nil {
					delta(ev.Delta)
				}
			case "response.completed", "response.done":
				result = ev.Response
				done = true
			case "response.failed", "response.incomplete", "error":
				return errors.New("provider stream failed or incomplete")
			}
			return nil
		})
		if e != nil {
			return Message{}, e
		}
		if !done {
			return Message{}, errors.New("model stream ended before completion")
		}
	}
	if result.Error != nil || result.Status == "failed" || result.Status == "incomplete" {
		return Message{}, errors.New("model response failed or incomplete")
	}
	out, err := parseResponse(result.Output)
	if u := result.Usage; u != nil {
		out.Usage = &TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedTokens: u.InputDetails.CachedTokens, CacheWriteTokens: u.InputDetails.CacheWriteTokens, ReasoningTokens: u.OutputDetails.ReasoningTokens, DurationMS: time.Since(started).Milliseconds()}
	}
	return out, err
}
func parseResponse(raw []json.RawMessage) (Message, error) {
	out := Message{Role: "assistant", Raw: raw}
	seen := map[string]bool{}
	sources := []string{}
	for _, b := range raw {
		var j struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type        string                              `json:"type"`
				Text        string                              `json:"text"`
				Annotations []struct{ Type, URL, Title string } `json:"annotations"`
			} `json:"content"`
		}
		if e := json.Unmarshal(b, &j); e != nil {
			return out, e
		}
		if j.Type == "function_call" {
			out.Calls = append(out.Calls, ToolCall{j.CallID, j.Name, j.Arguments})
		}
		for _, c := range j.Content {
			if c.Type == "output_text" {
				out.Content += c.Text
			}
			for _, a := range c.Annotations {
				if a.Type == "url_citation" && !seen[a.URL] {
					u := a.URL
					if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
						seen[u] = true
						sources = append(sources, fmt.Sprintf("%s — %s", a.Title, u))
					}
				}
			}
		}
	}
	if len(sources) > 0 {
		out.Content += "\n\nSources:\n" + strings.Join(sources, "\n")
	}
	return out, nil
}
func readSSE(r io.Reader, fn func([]byte) error) error {
	s := bufio.NewScanner(io.LimitReader(r, 32<<20))
	s.Buffer(make([]byte, 4096), 8<<20)
	var lines []string
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		e := fn([]byte(strings.Join(lines, "\n")))
		lines = nil
		return e
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if e := flush(); e != nil {
				return e
			}
		} else if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if e := s.Err(); e != nil {
		return e
	}
	return flush()
}
