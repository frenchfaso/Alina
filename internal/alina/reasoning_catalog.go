package alina

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Protocol compatibility version, not Alina's identity.
// openai/codex: codex-rs/codex-api/src/endpoint/models.rs.
const catalogClientVersion = "0.153.4"

type reasoningCatalog struct {
	mu      sync.Mutex
	cached  modelCatalog
	key     string
	retry   time.Time
	loading chan struct{}
}
type catalogModel struct {
	ID      string   `json:"id"`
	Levels  []string `json:"levels"`
	Default string   `json:"default"`
	Context int      `json:"context"`
	Vision  bool     `json:"vision"`
}
type modelCatalog struct {
	Key     string         `json:"key"`
	Models  []catalogModel `json:"models"`
	Fetched time.Time      `json:"fetched"`
	Stale   bool           `json:"-"`
}

func (c modelCatalog) model(id string) (catalogModel, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return catalogModel{}, false
}
func validModelID(s string) bool {
	return s != "" && len(s) <= 120 && !strings.ContainsAny(s, "\r\n\x00\t ")
}

// Wire efforts only: ultra is a Codex orchestration mode, not an effort.
func wireEffort(s string) bool {
	return slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, s)
}
func parseModelCatalog(raw []byte) (modelCatalog, error) {
	var response struct {
		Models []struct {
			Slug       string   `json:"slug"`
			Visibility string   `json:"visibility"`
			Default    string   `json:"default_reasoning_level"`
			Context    int      `json:"context_window"`
			Modalities []string `json:"input_modalities"`
			Levels     *[]struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return modelCatalog{}, errors.New("invalid model catalog")
	}
	c := modelCatalog{Models: []catalogModel{}}
	for _, m := range response.Models {
		if !validModelID(m.Slug) || m.Visibility == "hide" || m.Context < 8192 || m.Levels == nil {
			continue
		}
		if _, exists := c.model(m.Slug); exists {
			continue
		}
		item := catalogModel{ID: m.Slug, Levels: []string{}, Default: m.Default, Context: m.Context, Vision: slices.Contains(m.Modalities, "image")}
		for _, level := range *m.Levels {
			if wireEffort(level.Effort) && !slices.Contains(item.Levels, level.Effort) {
				item.Levels = append(item.Levels, level.Effort)
			}
		}
		if !slices.Contains(item.Levels, item.Default) {
			item.Default = ""
		}
		c.Models = append(c.Models, item)
	}
	if len(c.Models) == 0 || len(c.Models) > 500 {
		return c, errors.New("no compatible models in provider catalog")
	}
	return c, nil
}
func (p *Provider) catalogKey(account string) string {
	return contentID(p.Config.Provider + "\n" + p.BaseURL + "\n" + account + "\n" + catalogClientVersion)
}
func (p *Provider) currentCatalogKey() (string, error) {
	var credential Credential
	raw, err := os.ReadFile(filepath.Join(p.Auth.Dir, "chatgpt.json"))
	if err != nil || json.Unmarshal(raw, &credential) != nil || credential.AccountID == "" {
		return "", errors.New("model catalog requires ChatGPT login")
	}
	return p.catalogKey(credential.AccountID), nil
}
func (c modelCatalog) snapshot() modelCatalog {
	c.Stale = time.Since(c.Fetched) > 24*time.Hour
	return c
}
func (c modelCatalog) available(err error) (modelCatalog, error) {
	if c.Key != "" {
		return c.snapshot(), nil
	}
	if err == nil {
		err = errors.New("model catalog unavailable")
	}
	return c, err
}
func loadModelCatalog(path, key string) modelCatalog {
	b, err := readSmallFile(path, 256<<10)
	var saved modelCatalog
	if err != nil || json.Unmarshal([]byte(b), &saved) != nil || saved.Key != key || len(saved.Models) == 0 || len(saved.Models) > 500 || saved.Fetched.IsZero() || saved.Fetched.After(time.Now().Add(time.Minute)) {
		return modelCatalog{}
	}
	seen := map[string]bool{}
	for _, m := range saved.Models {
		if !validModelID(m.ID) || m.Context < 8192 || seen[m.ID] || m.Default != "" && !slices.Contains(m.Levels, m.Default) {
			return modelCatalog{}
		}
		seen[m.ID] = true
		for _, level := range m.Levels {
			if !wireEffort(level) {
				return modelCatalog{}
			}
		}
	}
	return saved
}
func (e *Engine) models(ctx context.Context, refresh bool) (modelCatalog, error) {
	root := e.global
	p, ok := root.Model.(*Provider)
	if !ok || p.Config.Provider != "chatgpt" || p.Auth == nil {
		return modelCatalog{}, errors.New("dynamic model controls require the ChatGPT catalog adapter")
	}
	cache := &root.catalog
	path := filepath.Join(root.AdminDir, "model-catalog.json")
	for {
		if err := ctx.Err(); err != nil {
			return modelCatalog{}, err
		}
		key, err := p.currentCatalogKey()
		if err != nil {
			return modelCatalog{}, err
		}
		cache.mu.Lock()
		if cache.key != key {
			cache.key, cache.retry = key, time.Time{}
			cache.cached = loadModelCatalog(path, key)
		}
		cached := cache.cached.snapshot()
		if !refresh || cached.Key != "" && !cached.Stale {
			cache.mu.Unlock()
			return cached.available(nil)
		}
		if done := cache.loading; done != nil {
			cache.mu.Unlock()
			// A stale snapshot is usable immediately; only the first load waits.
			if cached.Key != "" {
				return cached, nil
			}
			select {
			case <-ctx.Done():
				return modelCatalog{}, ctx.Err()
			case <-done:
				continue
			}
		}
		if time.Now().Before(cache.retry) {
			cache.mu.Unlock()
			return cached.available(nil)
		}
		cache.loading = make(chan struct{})
		cache.retry = time.Now().Add(5 * time.Minute)
		cache.mu.Unlock()

		// Network/auth waits never own the snapshot lock.
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		fetched, err := p.fetchModelCatalog(check)
		cancel()
		currentKey, identityErr := p.currentCatalogKey()
		cache.mu.Lock()
		if identityErr != nil || currentKey != key || cache.key != key || err == nil && fetched.Key != key {
			err = errors.New("model catalog account changed during refresh")
			cached = modelCatalog{}
		} else if err == nil {
			fetched.Fetched = time.Now().UTC()
			err = writeJSON(path, fetched)
			if err == nil {
				cache.cached, cached = fetched, fetched
			}
		}
		close(cache.loading)
		cache.loading = nil
		cache.mu.Unlock()
		return cached.available(err)
	}
}
func (p *Provider) fetchModelCatalog(ctx context.Context) (modelCatalog, error) {
	return authenticatedRequest(ctx, p.Auth, func(c Credential) (modelCatalog, error) {
		return p.fetchModelCatalogWithCredential(ctx, c)
	})
}

func (p *Provider) fetchModelCatalogWithCredential(ctx context.Context, credential Credential) (modelCatalog, error) {
	endpoint := "https://chatgpt.com/backend-api/codex/models"
	if p.BaseURL != "" {
		endpoint = strings.TrimSuffix(p.BaseURL, "/responses") + "/models"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?client_version="+catalogClientVersion, nil)
	if err != nil {
		return modelCatalog{}, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Access)
	req.Header.Set("ChatGPT-Account-ID", credential.AccountID)
	req.Header.Set("User-Agent", "alina/"+Version)
	req.Header.Set("originator", "alina")
	client := p.Client
	if client == nil {
		client = newHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return modelCatalog{}, &networkFailure{cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return modelCatalog{}, &remoteHTTPError{Status: resp.StatusCode}
	}
	var raw json.RawMessage
	if err = decodeLimited(resp.Body, &raw); err != nil {
		return modelCatalog{}, errors.New("invalid model catalog response")
	}
	catalog, err := parseModelCatalog(raw)
	catalog.Key = p.catalogKey(credential.AccountID)
	return catalog, err
}

type modelPreference struct{ Catalog, Model, Effort string }

func (e *Engine) modelPreferenceKey(owner string) string {
	if u, ok := e.Config.person(owner); ok {
		owner = "user:" + u.ID
	}
	return "model-preference:" + contentID(owner)
}
func (e *Engine) modelPreference(ctx context.Context, owner, key string) modelPreference {
	var raw string
	var p modelPreference
	if e.global.Memory.DB.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key=?", e.modelPreferenceKey(owner)).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &p) != nil || p.Catalog != key {
		return modelPreference{Catalog: key}
	}
	return p
}
func (e *Engine) selectedModel(ctx context.Context, owner string, c modelCatalog) (catalogModel, string) {
	p := e.modelPreference(ctx, owner, c.Key)
	id := p.Model
	if id == "" {
		id = e.Config.Model
	}
	m, ok := c.model(id)
	if !ok {
		m, _ = c.model(e.Config.Model)
	}
	if m.ID == "" {
		return catalogModel{ID: e.Config.Model, Context: e.Config.ContextTokens, Vision: true}, e.Config.ReasoningEffort
	}
	effort := p.Effort
	if effort == "" || !slices.Contains(m.Levels, effort) {
		effort = e.Config.ReasoningEffort
		if !slices.Contains(m.Levels, effort) {
			effort = m.Default
		}
	}
	return m, effort
}
func (e *Engine) setModelPreference(ctx context.Context, owner, action, value string, c modelCatalog) error {
	p := e.modelPreference(ctx, owner, c.Key)
	if action == "model" {
		if value != "default" {
			if _, ok := c.model(value); !ok {
				return errors.New("model is not available in this catalog")
			}
		}
		p.Model = value
		if value == "default" {
			p.Model = ""
		}
		p.Effort = ""
	} else {
		m, _ := e.selectedModel(ctx, owner, c)
		if value != "default" && !slices.Contains(m.Levels, value) {
			return errors.New("unsupported reasoning effort for this model")
		}
		p.Effort = value
		if value == "default" {
			p.Effort = ""
		}
	}
	_, err := e.global.Memory.DB.ExecContext(ctx, `INSERT INTO memory_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, e.modelPreferenceKey(owner), jsonText(p))
	return err
}

type selectedModelKey struct{}

// Only the job goroutine updates its selection. A changed selection invalidates
// prepared tools, context and model parameters together, before dispatch.
func (e *Engine) refreshJobModel(j *runningJob) bool {
	if j.Kind != "" && j.Kind != "chat" {
		return false
	}
	var selected *catalogModel
	model, effort := e.Config.Model, e.Config.ReasoningEffort
	if c, err := e.models(j.ctx, false); err == nil {
		m, r := e.selectedModel(j.ctx, j.Owner, c)
		model, effort = m.ID, r
		if _, known := c.model(m.ID); known {
			selected = &m
		}
	}
	changed := j.Model != model || j.Reasoning != effort || !sameCatalogModel(j.model, selected)
	e.mu.Lock()
	j.model, j.Model, j.Reasoning = selected, model, effort
	e.mu.Unlock()
	return changed
}
func sameCatalogModel(a, b *catalogModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.Default == b.Default && a.Context == b.Context && a.Vision == b.Vision && slices.Equal(a.Levels, b.Levels)
}
func (e *Engine) contextBudget(j *runningJob) int {
	if j.model != nil && j.model.ID != e.Config.Model {
		return min(e.Config.ContextTokens, j.model.Context)
	}
	return e.Config.ContextTokens
}
func (e *Engine) jobVision(j *runningJob) bool {
	if j != nil && j.model != nil {
		return j.model.Vision
	}
	if p, ok := e.Model.(*Provider); ok {
		return p.supportsImages()
	}
	return true
}
