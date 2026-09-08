package alina

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

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
		key := s.Config.OpenAIKey
		account := ""
		endpoint := "https://api.openai.com/v1/responses"
		model := s.openAIModel()
		if key == "" {
			if s.Provider.Auth == nil {
				return Message{}, errors.New("OpenAI search requires ChatGPT login: alina setup login")
			}
			c, e := s.Provider.Auth.Get(ctx)
			if e != nil {
				return Message{}, errors.New("OpenAI search requires ChatGPT login or a separate OpenAI API key")
			}
			key = c.Access
			account = c.AccountID
			endpoint = "https://chatgpt.com/backend-api/codex/responses"
		}
		ctx = context.WithValue(ctx, reasoningEffortKey{}, "low")
		m, e := s.Provider.responses(ctx, s.Provider.endpoint(endpoint), model, key, account, "search-"+session, []Message{{Role: "system", Content: searchPrompt}, {Role: "user", Content: query}}, nil, true, nil)
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
	if s.Provider != nil && s.Provider.Config.Provider == "chatgpt" {
		return s.Provider.Config.Model
	}
	return defaultModel
}
