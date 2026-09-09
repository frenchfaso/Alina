package alina

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Families are Alina memory-sharing domains, independent of Telegram chats.
// Empty Family means personal memory. IDs are stable; names are display labels.
type User struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	TelegramID int64  `json:"telegram_id"`
	Family     string `json:"family,omitempty"`
}

func (u User) scope() string {
	if u.Family != "" {
		return "family-" + u.Family
	}
	return "user-" + u.ID
}

func (c Config) localUser() User {
	for _, u := range c.Users {
		if u.ID == c.LocalUser || c.LocalUser == "" {
			return u
		}
	}
	return User{}
}

func (c Config) telegramUser(id int64) (User, bool) {
	for _, u := range c.Users {
		if u.TelegramID == id {
			return u, true
		}
	}
	return User{}, false
}

func (c Config) person(owner string) (User, bool) {
	if strings.HasPrefix(owner, "local:") {
		for _, u := range c.Users {
			if u.ID == strings.TrimPrefix(owner, "local:") {
				return u, true
			}
		}
		return User{}, false
	}
	if owner == "local" {
		u := c.localUser()
		return u, u.ID != ""
	}
	for _, u := range c.Users {
		if owner == telegramOwner(c.Telegram, u.TelegramID) {
			return u, true
		}
	}
	return User{}, false
}

func (c Config) validatePeople() error {
	if len(c.Users) > 32 {
		return errors.New("maximum 32 configured users")
	}
	ids, accounts := map[string]bool{}, map[int64]bool{}
	for _, u := range c.Users {
		if !safeID(u.ID) || len(u.ID) > 40 || strings.TrimSpace(u.Name) == "" || len(u.Name) > 80 || strings.ContainsAny(u.Name, "\r\n\x00") || u.TelegramID <= 0 || u.TelegramID >= 1<<52 || u.Family != "" && (!safeID(u.Family) || len(u.Family) > 40) {
			return errors.New("users require a stable ID (40 ASCII letters/digits/_/-), name (80 bytes), positive Telegram ID and optional family ID (40 ASCII letters/digits/_/-)")
		}
		if ids[u.ID] || accounts[u.TelegramID] {
			return errors.New("duplicate user or Telegram ID")
		}
		ids[u.ID], accounts[u.TelegramID] = true, true
	}
	if c.LocalUser != "" && !ids[c.LocalUser] {
		return errors.New("local_user must identify a configured user")
	}
	return nil
}

// Reuse the same turn implementation for each memory domain. The global mind
// owns the only dream, soul, inference gate and daily initiative budget.
// Domain engines have their own archive, context, workspace, jobs and grants.
func NewEngine(dir string, c Config, model Model, search *Search, events ...*EventLog) (*Engine, error) {
	if err := c.validatePeople(); err != nil {
		return nil, err
	}
	legacyScope, err := readLegacyScope(dir)
	if len(c.Users) == 0 {
		if err == nil {
			return nil, errors.New("this instance has family memory; configure users instead of reverting to owner_id")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		return newEngine(dir, dir, c, model, search, nil, events...)
	}
	// The old single-user archive stays in place, bound once to its user's
	// scope. Later membership changes never relabel or merge historical data.
	if os.IsNotExist(err) {
		u := c.localUser()
		if c.Telegram.OwnerID > 0 {
			var ok bool
			u, ok = c.telegramUser(c.Telegram.OwnerID)
			if !ok {
				return nil, errors.New("include the previous Telegram owner in users when first enabling family memory")
			}
		}
		legacyScope = u.scope()
		err = writeJSON(filepath.Join(dir, "people-state.json"), map[string]string{"legacy_scope": legacyScope})
	}
	if err != nil {
		return nil, err
	}
	global, err := newEngine(filepath.Join(dir, "mind"), dir, c, model, search, nil, events...)
	if err != nil {
		return nil, err
	}
	global.scopes = map[string]*Engine{}
	global.Scope = "global"
	for _, u := range c.Users {
		scope := u.scope()
		if global.scopes[scope] != nil {
			continue
		}
		path := filepath.Join(dir, "scopes", scope)
		if scope == legacyScope {
			path = dir
		}
		private := c
		private.Memory.Dream = false
		if c.localUser().scope() != scope {
			private.LocalUser = u.ID
		}
		child, err := newEngine(path, dir, private, model, search, global, events...)
		if err != nil {
			global.Close()
			return nil, err
		}
		child.Scope = scope
		global.scopes[scope] = child
	}
	return global, nil
}

func (e *Engine) localEngine() *Engine {
	if len(e.scopes) == 0 {
		return e
	}
	return e.scopes[e.Config.localUser().scope()]
}

func (e *Engine) engines() []*Engine {
	out := []*Engine{e}
	keys := make([]string, 0, len(e.scopes))
	for k := range e.scopes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, e.scopes[k])
	}
	return out
}

func (e *Engine) runSchedulers(ctx context.Context) {
	var wg sync.WaitGroup
	for _, engine := range e.engines() {
		wg.Go(func() { engine.Scheduler.Run(ctx) })
	}
	wg.Wait()
}

func (e *Engine) acceptsOwner(owner string) bool {
	if e.Scope == "" || owner == "alina" || owner == "system" {
		return true
	}
	u, ok := e.Config.person(owner)
	return ok && u.scope() == e.Scope
}

func (e *Engine) memoryScope(scope string, j *runningJob) (*Engine, error) {
	if scope == "" {
		return e, nil
	}
	if e != e.global || len(e.scopes) == 0 || j.Kind != "dream" && j.Kind != "initiative" {
		return nil, errors.New("memory scope is fixed by the authenticated person")
	}
	if child := e.scopes[scope]; child != nil {
		return child, nil
	}
	return nil, errors.New("unknown memory scope")
}

func (e *Engine) personContext(j *runningJob) string {
	if len(e.Config.Users) == 0 {
		return ""
	}
	text := "Memory scope: " + jsonText(e.Scope) + ".\n"
	if u, ok := e.Config.person(j.Owner); ok {
		text += "Current person: " + jsonText(u) + ".\n"
	}
	if e.Scope != "global" {
		text += "People sharing this memory (family membership is configured, never inferred):\n"
		for _, u := range e.Config.Users {
			if u.scope() == e.Scope {
				text += jsonText(u) + "\n"
			}
		}
	} else {
		text += "Global private reflection. You may inspect these memory scopes using the memory tool's scope field:\n"
		for _, u := range e.Config.Users {
			text += jsonText(map[string]string{"scope": u.scope(), "id": u.ID, "name": u.Name}) + "\n"
		}
	}
	return text
}

// Native job routing uses authenticated Telegram IDs. The local Unix API is an
// administrative interface: ?user=ID selects a person's current memory domain.
func peopleHandler(root *Engine) http.Handler {
	handlers := map[*Engine]http.Handler{}
	people := map[string]http.Handler{}
	globalPeople := map[string]http.Handler{}
	for _, engine := range root.engines() {
		handlers[engine] = handler(engine)
	}
	for _, u := range root.Config.Users {
		people[u.ID] = handler(root.scopes[u.scope()], "local:"+u.ID)
		globalPeople[u.ID] = handler(root, "local:"+u.ID)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/people" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"users": root.Config.Users, "local_user": root.Config.localUser().ID, "global_scope": root.Scope})
			return
		}
		engine := root.localEngine()
		selected := handlers[engine]
		id, scope := r.URL.Query().Get("user"), r.URL.Query().Get("scope")
		if id != "" && scope != "" {
			http.Error(w, "choose user or scope", 400)
			return
		}
		if id != "" {
			selected = people[id]
			for _, u := range root.Config.Users {
				if u.ID == id {
					engine = root.scopes[u.scope()]
				}
			}
		} else if scope != "" {
			engine = root.scopes[scope]
			if scope == "global" {
				engine = root
			}
			selected = handlers[engine]
		}
		if selected == nil {
			http.Error(w, "unknown user or scope", 400)
			return
		}
		global := false
		if r.Method == "POST" && r.URL.Path == "/v1/memory/jobs" {
			b, err := io.ReadAll(io.LimitReader(r.Body, 40<<10+1))
			if err != nil || len(b) > 40<<10 {
				http.Error(w, "invalid request", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var request struct{ Kind string }
			global = json.Unmarshal(b, &request) == nil && request.Kind == "dream"
		} else if strings.HasPrefix(r.URL.Path, "/v1/jobs/") && scope == "" {
			jobID := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), "/")[0]
			if _, ok := engine.Get(jobID); !ok {
				_, global = root.Get(jobID)
			}
		}
		if global {
			selected = handlers[root]
			if id != "" {
				selected = globalPeople[id]
			}
		}
		selected.ServeHTTP(w, r)
	})
}

func validatePeopleState(dir string, c Config) error {
	_, err := readLegacyScope(dir)
	if err == nil && len(c.Users) == 0 {
		return errors.New("keep at least one user; family memory cannot revert to owner_id")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) && len(c.Users) > 0 && c.Telegram.OwnerID > 0 {
		if _, ok := c.telegramUser(c.Telegram.OwnerID); !ok {
			return errors.New("include the previous Telegram owner when first enabling family memory")
		}
	}
	return nil
}

func readLegacyScope(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "people-state.json"))
	if err != nil {
		return "", err
	}
	var state struct {
		LegacyScope string `json:"legacy_scope"`
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return "", fmt.Errorf("invalid family memory binding: %w", err)
	}
	id, family := strings.CutPrefix(state.LegacyScope, "family-")
	if !family {
		id, _ = strings.CutPrefix(state.LegacyScope, "user-")
	}
	if (!family && !strings.HasPrefix(state.LegacyScope, "user-")) || !safeID(id) || len(id) > 40 {
		return "", errors.New("invalid family memory binding: expected family-ID or user-ID")
	}
	return state.LegacyScope, nil
}
