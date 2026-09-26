package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const defaultSearchModel = "gpt-6-sol"
const defaultSearchEffort = "high"

type searchSelection struct{ Model, Effort string }
type searchSelectionKey struct{}

type Search struct {
	Config              SearchConfig
	Provider            *Provider
	Client              *http.Client
	TavilyURL, BraveURL string
}

func (s *Search) Run(ctx context.Context, session, provider, query string) (string, error) {
	m, err := s.complete(ctx, session, provider, query)
	return m.Content, err
}

func (s *Search) complete(ctx context.Context, session, provider, query string) (Message, error) {
	if strings.TrimSpace(query) == "" || len(query) > 4000 {
		return Message{}, errors.New("search query must be 1-4000 characters")
	}
	if provider == "" {
		provider = s.Config.Default
	}
	switch provider {
	case "tavily":
		if s.Config.TavilyKey == "" {
			return Message{}, errors.New("Tavily key missing: alina setup")
		}
		endpoint := s.TavilyURL
		if endpoint == "" {
			endpoint = "https://api.tavily.com/search"
		}
		var r struct {
			Results []struct{ Title, URL, Content string } `json:"results"`
		}
		e := requestJSON(ctx, s.Client, "POST", endpoint, map[string]any{"query": query, "max_results": 5, "search_depth": "basic", "include_raw_content": false}, map[string]string{"Authorization": "Bearer " + s.Config.TavilyKey}, &r)
		if e != nil {
			return Message{}, e
		}
		var b strings.Builder
		for _, v := range r.Results {
			fmt.Fprintf(&b, "%s\n%s\n%s\n\n", v.Title, v.URL, truncate(v.Content, 3000))
		}
		return Message{Role: "assistant", Content: b.String()}, nil
	case "brave":
		if s.Config.BraveKey == "" {
			return Message{}, errors.New("Brave Search key missing: alina setup")
		}
		endpoint := s.BraveURL
		if endpoint == "" {
			endpoint = "https://api.search.brave.com/res/v1/web/search"
		}
		var r struct {
			Web struct {
				Results []struct{ Title, URL, Description string } `json:"results"`
			} `json:"web"`
		}
		e := requestJSON(ctx, s.Client, "GET", endpoint+"?"+url.Values{"q": {query}, "count": {"5"}}.Encode(), nil, map[string]string{"X-Subscription-Token": s.Config.BraveKey}, &r)
		if e != nil {
			return Message{}, e
		}
		var b strings.Builder
		for _, v := range r.Web.Results {
			fmt.Fprintf(&b, "%s\n%s\n%s\n\n", v.Title, v.URL, truncate(v.Description, 3000))
		}
		return Message{Role: "assistant", Content: b.String()}, nil
	case "openai":
		if s.Provider == nil {
			return Message{}, errors.New("OpenAI search provider is not initialized")
		}
		selection := s.selection(ctx)
		ctx = context.WithValue(ctx, searchSelectionKey{}, selection)
		ctx = context.WithValue(ctx, reasoningEffortKey{}, selection.Effort)
		messages := []Message{{Role: "system", Content: searchPrompt}, {Role: "user", Content: query}}
		var m Message
		var e error
		if s.Config.OpenAIKey == "" {
			if s.Provider.Auth == nil {
				return Message{}, errors.New("OpenAI search requires ChatGPT login: alina setup login")
			}
			m, e = s.Provider.chatGPTResponses(ctx, selection.Model, "search-"+session, messages, nil, true, nil)
		} else {
			m, e = s.Provider.responses(ctx, s.Provider.endpoint("https://api.openai.com/v1/responses"), selection.Model, s.Config.OpenAIKey, "", "search-"+session, messages, nil, true, nil)
		}
		if e != nil {
			return Message{}, fmt.Errorf("OpenAI web search: %w; availability depends on account/model; configure Tavily or Brave if unavailable", e)
		}
		return m, nil
	default:
		return Message{}, errors.New("search disabled or unknown provider")
	}
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n] + "\n[output truncated]"
}

func (s *Search) openAIModel() string {
	if s.Config.OpenAIModel != "" {
		return s.Config.OpenAIModel
	}
	return defaultSearchModel
}

func (s *Search) selection(ctx context.Context) searchSelection {
	if choice, ok := ctx.Value(searchSelectionKey{}).(searchSelection); ok {
		return choice
	}
	return searchSelection{Model: s.openAIModel(), Effort: defaultSearchEffort}
}

func searchSpec(overrides bool) ToolSpec {
	props := map[string]any{"query": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string", "enum": []string{"openai", "tavily", "brave"}}}
	description := "Search the web and return concise notes with source URLs; no arbitrary file downloads. OpenAI defaults to Sol 6 high."
	if overrides {
		props["model"] = map[string]any{"type": "string", "description": "Optional OpenAI model override for this search only, when the user requests it or the task needs it. Must be available in the ChatGPT catalog."}
		props["reasoning"] = map[string]any{"type": "string", "description": "Optional reasoning override with model; must be supported. Normally omit both overrides."}
		description += " Normally delegate substantial research to a worker to keep your context small; quick lookups can be done directly."
	}
	return ToolSpec{Name: "web_search", Description: description, Parameters: map[string]any{"type": "object", "properties": props, "required": []string{"query"}}}
}

func (e *Engine) searchTool(j *runningJob, raw string) (string, error) {
	var a struct{ Query, Provider, Model, Reasoning string }
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", err
	}
	if e.Search == nil {
		return "", errors.New("search unavailable")
	}
	if j.Kind == "initiative" && !e.Config.Autonomy.Search {
		return "", errors.New("web research for personal exploration is disabled")
	}
	if a.Provider == "" {
		a.Provider = e.Search.Config.Default
	}
	ctx := j.ctx
	if a.Model != "" || a.Reasoning != "" {
		if j.Kind != "chat" && j.Kind != "" {
			return "", errors.New("only Alina in a user conversation can override the search model or reasoning; use the configured defaults")
		}
		if a.Provider != "openai" {
			return "", errors.New("model and reasoning apply only to OpenAI search")
		}
		if e.Search.Config.OpenAIKey != "" {
			return "", errors.New("dynamic search overrides require the ChatGPT catalog and credentials")
		}
		catalog, err := e.models(ctx, true)
		if err != nil {
			return "", err
		}
		if a.Model == "" {
			a.Model = e.Search.openAIModel()
		}
		model, ok := catalog.model(a.Model)
		if !ok {
			return "", errors.New("search model unavailable in the provider catalog")
		}
		if a.Reasoning == "" {
			a.Reasoning = defaultSearchEffort
			if !slices.Contains(model.Levels, a.Reasoning) {
				a.Reasoning = model.Default
			}
		}
		if a.Reasoning != "" && !slices.Contains(model.Levels, a.Reasoning) {
			return "", fmt.Errorf("supported reasoning levels for %s: %s", model.ID, strings.Join(model.Levels, ", "))
		}
		ctx = context.WithValue(ctx, searchSelectionKey{}, searchSelection{model.ID, a.Reasoning})
	}
	if a.Provider == "openai" {
		m, err := (jobModel{e: e, j: j}).infer(ctx, "search", func(ctx context.Context) (Message, error) {
			return e.Search.complete(ctx, j.Session, a.Provider, a.Query)
		})
		return m.Content, err
	}
	return e.Search.Run(ctx, j.Session, a.Provider, a.Query)
}
