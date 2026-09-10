package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func familyConfig(dir string) Config {
	c := DefaultConfig()
	c.WorkDir = dir
	c.Telegram = TelegramConfig{Enabled: true, Token: "fixture", OwnerID: 42, Binding: "family-bot"}
	c.Users = []User{{ID: "alex", Name: "Alex", TelegramID: 42, Family: "home"}, {ID: "bea", Name: "Bea", TelegramID: 84, Family: "home"}, {ID: "chris", Name: "Chris", TelegramID: 126, Family: "other"}, {ID: "dana", Name: "Dana", TelegramID: 168}}
	c.LocalUser = "alex"
	return c
}
func familyEngine(t *testing.T, model Model) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := NewEngine(dir, familyConfig(dir), model, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestFamilyMemoryBoundariesAndOneGlobalSoul(t *testing.T) {
	e := familyEngine(t, nil)
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, nil)
	a, _ := tg.engineFor(42)
	b, _ := tg.engineFor(84)
	c, _ := tg.engineFor(126)
	d, _ := tg.engineFor(168)
	if a != b || a == c || c == d || a == e {
		t.Fatal("incorrect memory domains")
	}
	if a.gate != e.gate || c.jobChanged != e.jobChanged || d.global != e {
		t.Fatal("global execution resources duplicated")
	}
	id, err := a.Memory.Note(e.ctx, time.Now(), "tg-a", "fact", "preference", "Alex prefers peppermint tea", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Memory.Read(e.ctx, id, time.Now()); err != nil {
		t.Fatal("family did not share memory", err)
	}
	if _, err = c.Memory.Read(e.ctx, id, time.Now()); err == nil {
		t.Fatal("other family read a known ID")
	}
	if _, err = d.Memory.Read(e.ctx, id, time.Now()); err == nil {
		t.Fatal("unaffiliated person read family memory")
	}
	job := &runningJob{Job: Job{Owner: telegramOwner(e.Config.Telegram, 126), Kind: "chat"}, ctx: e.ctx}
	if _, err = c.stateTool(job, ToolCall{Name: "memory", Arguments: `{"action":"read","part":"` + id + `","scope":"family-home"}`}); err == nil {
		t.Fatal("user selected another memory scope")
	}
	context, err := c.runtimeContext(job)
	if err != nil || strings.Contains(context, "Alex") || strings.Contains(context, "peppermint") || !strings.Contains(context, "Chris") {
		t.Fatal("wrong person or family in prompt", context, err)
	}
	ownFile := filepath.Join(a.Workspace(), "family.txt")
	if err = writeText(ownFile, "family data"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.filePath(job, ownFile, false); err == nil {
		t.Fatal("native read crossed family boundary")
	}
	soul, _ := e.Memory.Soul()
	next := "# Alina\nI learn from different perspectives and respect confidences.\n"
	if err = e.Memory.reviseSoul(e.ctx, time.Now(), soul, next, "Learn to respect different perspectives."); err != nil {
		t.Fatal(err)
	}
	for _, engine := range e.engines() {
		got, _ := engine.Memory.Soul()
		if got != next || engine.Memory.SoulDir != e.AdminDir {
			t.Fatal("more than one soul")
		}
		if engine != e && engine.Scheduler.tasks["dream"].Enabled {
			t.Fatal("per-family dream enabled")
		}
	}
	if !e.Scheduler.tasks["dream"].Enabled {
		t.Fatal("global dream disabled")
	}
	if _, err = a.reflectionTool(&runningJob{Job: Job{Kind: "dream"}, ctx: a.ctx}, ToolCall{Name: "soul", Arguments: `{}`}); err == nil {
		t.Fatal("family reflection revised global soul")
	}
}

func TestGlobalDreamSeesFamiliesWithoutSharingItsPrivateNotes(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	e := familyEngine(t, modelFunc(func(_ context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		mu.Lock()
		prompts = append(prompts, jsonText(m))
		mu.Unlock()
		return Message{Role: "assistant", Content: "Different people need different approaches."}, nil
	}))
	for scope, engine := range e.scopes {
		msg := Message{Role: "user", Content: "Experience in " + scope}
		if err := engine.Memory.recordMessage(e.ctx, time.Now(), Job{ID: "event", Session: "conversation"}, &msg); err != nil {
			t.Fatal(err)
		}
	}
	job, err := e.submit("dream-test", "system", "reflect", "", "dream")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, job.ID, "completed")
	mu.Lock()
	captured := strings.Join(prompts, "\n")
	mu.Unlock()
	for _, scope := range []string{"family-home", "family-other", "user-dana"} {
		if !strings.Contains(captured, scope) {
			t.Fatal("dream missed a scope", scope)
		}
	}
	j := &runningJob{Job: Job{Kind: "dream"}, ctx: e.ctx}
	for _, scope := range []string{"family-home", "family-other", "user-dana"} {
		text, err := e.stateTool(j, ToolCall{Name: "memory", Arguments: fmt.Sprintf(`{"action":"read","part":"archive","scope":%q}`, scope)})
		if err != nil || !strings.Contains(text, "Experience in "+scope) {
			t.Fatal("dream could not inspect source", scope, err)
		}
	}
	id, err := e.Memory.Note(e.ctx, time.Now(), "mind", "reflection", "hypothesis", "Private global interpretation", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range e.scopes {
		if _, err = engine.Memory.Read(e.ctx, id, time.Now()); err == nil {
			t.Fatal("global private reflection exposed")
		}
	}
	_, _, changed, err := e.dreamScopes(e.ctx)
	if err != nil || changed {
		t.Fatal("global dream cursors not checkpointed", err)
	}
	msg := Message{Role: "user", Content: "Another experience"}
	if err = e.scopes["family-other"].Memory.recordMessage(e.ctx, time.Now(), Job{Session: "conversation"}, &msg); err != nil {
		t.Fatal(err)
	}
	_, _, changed, err = e.dreamScopes(e.ctx)
	if err != nil || !changed {
		t.Fatal("new experience not eligible", err)
	}
}

func TestTelegramMultiplePeopleRouteRepliesAndAttribution(t *testing.T) {
	e := familyEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{Role: "assistant", Content: "done"}, nil
	}))
	var mu sync.Mutex
	sent := map[int64][]string{}
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			ChatID int64 `json:"chat_id"`
			Text   string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		mu.Lock()
		sent[body.ChatID] = append(sent[body.ChatID], body.Text)
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	for i, user := range e.Config.Users {
		var update tgUpdate
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"update_id":%d,"message":{"text":"message from %s","from":{"id":%d},"chat":{"id":%d,"type":"private"}}}`, i+1, user.Name, user.TelegramID, user.TelegramID)), &update); err != nil {
			t.Fatal(err)
		}
		if err := tg.process(e.ctx, update); err != nil {
			t.Fatal(err)
		}
		engine, _ := tg.engineFor(user.TelegramID)
		awaitStatus(t, engine, tg.updateKey(int64(i+1)), "completed")
	}
	for {
		more, err := tg.deliverPending(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	mu.Lock()
	for _, u := range e.Config.Users {
		if len(sent[u.TelegramID]) != 1 || sent[u.TelegramID][0] != "done" {
			t.Error("wrong reply destination", u, sent)
		}
	}
	mu.Unlock()
	home := e.scopes["family-home"]
	text, err := home.Memory.Read(e.ctx, "archive", time.Now())
	if err != nil || !strings.Contains(text, "Speaker:") || !strings.Contains(text, "Alex") || !strings.Contains(text, "Bea") || strings.Contains(text, "Chris") {
		t.Fatal("archive attribution", text, err)
	}
	pending, err := tg.pendingNotifications(e.ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("receipts not stored in domains", pending, err)
	}
}

func TestMembershipChangeKeepsHistoricalScope(t *testing.T) {
	dir := t.TempDir()
	cfg := familyConfig(dir)
	e, err := NewEngine(dir, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.scopes["family-home"].Memory.Note(e.ctx, time.Now(), "old", "note", "fact", "Historical home fact", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	cfg.Users[0].Family = "new-home"
	cfg.Users[1].Family = "new-home"
	e, err = NewEngine(dir, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.localEngine().Memory.Read(e.ctx, id, time.Now()); err == nil {
		t.Fatal("moving users relabeled old family history")
	}
	cfg.Users = nil
	cfg.LocalUser = ""
	if err = SaveConfig(dir, cfg); err == nil {
		t.Fatal("family mode silently downgraded to legacy owner")
	}
}

func TestFamilyLocalAPISelectsScopeAndGlobalDream(t *testing.T) {
	e := familyEngine(t, nil)
	id, err := e.localEngine().Memory.Note(e.ctx, time.Now(), "local", "note", "fact", "Only home knows this", "")
	if err != nil {
		t.Fatal(err)
	}
	h := peopleHandler(e)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/v1/memory/read?q=" + id, 200}, {"/v1/memory/read?q=" + id + "&user=chris", 400}, {"/v1/people", 200}, {"/v1/memory/read?q=focus&user=unknown", 400}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/memory/jobs", strings.NewReader(`{"kind":"dream"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job Job
	if err = json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(job.ID); !ok {
		t.Fatal("dream was not submitted to global mind")
	}
}

func TestPeopleSetupAndPairingPreserveExistingMessages(t *testing.T) {
	dir := t.TempDir()
	c := familyConfig(dir)
	c.Users = c.Users[:1]
	var out bytes.Buffer
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true,"result":[]}`
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			body = `{"ok":true,"result":{"username":"fixture"}}`
		} else {
			var req struct{ Offset int64 }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			if req.Offset == 0 {
				text := out.String()
				start := strings.LastIndex(text, "?start=") + len("?start=")
				code := strings.Fields(text[start:])[0]
				body = fmt.Sprintf(`{"ok":true,"result":[{"update_id":10,"message":{"text":"keep this request","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":11,"message":{"text":"/start %s","from":{"id":84},"chat":{"id":84,"type":"private"}}}]}`, code)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	w := &wizard{dir: dir, ctx: context.Background(), in: bufio.NewReader(strings.NewReader("1\nBea\n4\n")), out: &out}
	if err := w.people(&c, client, true); err != nil {
		t.Fatal(err, out.String())
	}
	if len(c.Users) != 2 || c.Users[1].TelegramID != 84 || c.Users[1].Family != "home" {
		t.Fatal(c.Users)
	}
	tg := NewTelegram(dir, c.Telegram, nil, client)
	if tg.state.Offset != 12 || len(tg.state.Pending) != 1 || tg.state.Pending[0].Message.Text != "keep this request" {
		t.Fatal("pairing discarded pending messages", tg.state)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("wizard saved before outer commit")
	}
}

func TestFamilyConsentBelongsToRequester(t *testing.T) {
	e := familyEngine(t, &scriptedModel{Command: "printf approved", Network: true})
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	engine, _ := tg.engineFor(42)
	job, err := engine.Submit("requester", telegramOwner(tg.Config, 42), "request approval")
	if err != nil {
		t.Fatal(err)
	}
	pending := awaitStatus(t, engine, job.ID, "approval")
	callback := func(id int64) tgUpdate {
		var update tgUpdate
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"update_id":100,"callback_query":{"id":"callback","data":"a:%s:deny","from":{"id":%d},"message":{"chat":{"id":%d,"type":"private"}}}}`, pending.Approval.ID, id, id)), &update); err != nil {
			t.Fatal(err)
		}
		return update
	}
	if err = tg.process(e.ctx, callback(84)); err != nil {
		t.Fatal(err)
	}
	still, _ := engine.Get(job.ID)
	if still.Status != "approval" {
		t.Fatal("another family member decided the consent")
	}
	if err = tg.process(e.ctx, callback(42)); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, engine, job.ID, "completed")
}

func TestGlobalInitiativeBudgetIsSharedAcrossFamilies(t *testing.T) {
	e := familyEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{Role: "assistant", Content: "done"}, nil
	}))
	for _, engine := range e.engines() {
		engine.Config.Autonomy.Enabled = true
		engine.Config.Autonomy.MaxCalls = 1
	}
	job := func(engine *Engine) *runningJob {
		return &runningJob{Job: Job{ID: randomID(), Kind: "initiative"}, ctx: engine.ctx}
	}
	a, b := e.scopes["family-home"], e.scopes["family-other"]
	if _, err := (jobModel{e: a, j: job(a)}).Complete(e.ctx, "a", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (jobModel{e: b, j: job(b)}).Complete(e.ctx, "b", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatal("family multiplied daily budget", err)
	}
}

func TestPeopleConfigurationRejectsAmbiguityAndPreservesBinding(t *testing.T) {
	dir := t.TempDir()
	c := familyConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	for _, bad := range []string{
		`{"users":[{"id":"same","name":"A","telegram_id":42},{"id":"same","name":"B","telegram_id":84}]}`,
		`{"users":[{"id":"a","name":"A","telegram_id":42},{"id":"b","name":"B","telegram_id":42}],"local_user":"a"}`,
		`{"users":[],"local_user":""}`,
		`{"local_user":"missing"}`,
	} {
		var out bytes.Buffer
		if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(bad), &out); err == nil {
			t.Fatal("invalid people patch accepted", bad)
		}
	}
	c.Users[1].Family = "other"
	patch := jsonText(map[string]any{"users": c.Users})
	var out bytes.Buffer
	if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(patch), &out); err != nil {
		t.Fatal(err)
	}
	updated, err := LoadConfig(dir)
	if err != nil || updated.Telegram.Binding != c.Telegram.Binding {
		t.Fatal("membership reset bot updates", err)
	}
	for _, broken := range []string{`{`, `{"legacy_scope":"global"}`, `{"legacy_scope":"family-"}`, `{"legacy_scope":"family-../other"}`} {
		if err = writeText(filepath.Join(dir, "people-state.json"), broken); err != nil {
			t.Fatal(err)
		}
		if err = SaveConfig(dir, c); err == nil {
			t.Fatal("invalid archive binding accepted", broken)
		}
		if _, err = NewEngine(dir, c, nil, nil); err == nil {
			t.Fatal("invalid archive binding opened", broken)
		}
	}
}

func TestFamilyPairingInboxReplayAndMembershipChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint("changed=", changed), func(t *testing.T) {
			e := familyEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
				return Message{Role: "assistant", Content: "done"}, nil
			}))
			polled := make(chan struct{}, 1)
			var mu sync.Mutex
			var sent []string
			client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/getUpdates") {
					select {
					case polled <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				var body struct{ Text string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return nil, err
				}
				mu.Lock()
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					sent = append(sent, body.Text)
				}
				mu.Unlock()
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
			})}
			tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
			var update tgUpdate
			if err := json.Unmarshal([]byte(`{"update_id":10,"alina_scope":"family-home","message":{"text":"pending request","from":{"id":42},"chat":{"id":42,"type":"private"}}}`), &update); err != nil {
				t.Fatal(err)
			}
			if changed {
				update.Scope = "family-before-move"
			}
			tg.state.Offset, tg.state.Pending = 11, []tgUpdate{update}
			if err := tg.saveLocked(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				tg = NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
				ctx, cancel := context.WithCancel(e.ctx)
				done := make(chan struct{})
				go func() { defer close(done); tg.Run(ctx) }()
				select {
				case <-polled:
				case <-time.After(5 * time.Second):
					cancel()
					<-done
					t.Fatal("saved inbox did not drain")
				}
				cancel()
				<-done
			}
			if len(tg.state.Pending) != 0 || tg.state.Offset != 11 {
				t.Fatal("inbox not checkpointed", tg.state)
			}
			engine := e.localEngine()
			if changed {
				if _, ok := engine.Get(tg.updateKey(10)); ok {
					t.Fatal("old message executed in new family")
				}
				mu.Lock()
				defer mu.Unlock()
				if len(sent) != 1 || !strings.Contains(sent[0], "Reinvia") {
					t.Fatal("missing family change notice", sent)
				}
			} else {
				awaitStatus(t, engine, tg.updateKey(10), "completed")
				if len(engine.Jobs("")) != 1 {
					t.Fatal("inbox replay duplicated request")
				}
			}
		})
	}
}

func TestInactiveLegacyFamilyDoesNotBlockNotifications(t *testing.T) {
	dir := t.TempDir()
	c := familyConfig(dir)
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	c.Users, c.LocalUser = c.Users[2:3], "chris"
	e, err = NewEngine(dir, c, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		return Message{Role: "assistant", Content: "done"}, nil
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var recipient int64
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		recipient = body.ChatID
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})}
	tg := NewTelegram(dir, c.Telegram, e, client)
	tg.state.Delivered["old-job"] = "completed"
	job, err := e.localEngine().Submit("active", telegramOwner(c.Telegram, 126), "reply")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e.localEngine(), job.ID, "completed")
	if more, err := tg.deliverPending(e.ctx); err != nil || !more || recipient != 126 {
		t.Fatal("inactive family blocked active reply", more, recipient, err)
	}
	if tg.state.Delivered["old-job"] != "completed" {
		t.Fatal("inactive family's legacy receipt discarded")
	}
}

func TestBlockedTelegramPersonDoesNotStarveOtherReplies(t *testing.T) {
	e := familyEngine(t, nil)
	attempts := map[int64]int{}
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		attempts[body.ChatID]++
		response := `{"ok":true,"result":{}}`
		if body.ChatID == 42 {
			response = `{"ok":false,"error_code":403}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	for i := range 51 {
		person := e.Config.Users[0]
		if i == 50 {
			person = e.Config.Users[2]
		}
		j := &runningJob{Job: Job{ID: fmt.Sprintf("reply-%d", i), Owner: telegramOwner(tg.Config, person.TelegramID), Session: "local", Status: "completed", Created: time.Now().Add(time.Duration(i) * time.Second), Output: "done"}}
		if err := e.scopes[person.scope()].persist(j); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tg.deliverPending(e.ctx); err == nil {
		t.Fatal("blocked chat error lost")
	}
	if attempts[42] != 1 || attempts[126] != 1 {
		t.Fatal("one blocked person starved another or retried in the same batch", attempts)
	}
	if pending, err := pendingTelegramJobs(e.ctx, e.scopes["family-other"], telegramOwner(tg.Config, 126)); err != nil || len(pending) != 0 {
		t.Fatal("successful reply not checkpointed", pending, err)
	}
}
