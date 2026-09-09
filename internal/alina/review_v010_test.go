package alina

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultLocalIdentityRemainsAttachedToTask(t *testing.T) {
	dir := t.TempDir()
	c := familyConfig(dir)
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	body := jsonText(map[string]string{"name": "Alex's task", "at": time.Now().Add(time.Hour).Format(time.RFC3339), "prompt": "Inspect the device"})
	peopleHandler(e).ServeHTTP(w, httptest.NewRequest("POST", "/v1/tasks", strings.NewReader(body)))
	var task ScheduledTask
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
		e.Close()
		t.Fatal(w.Code, w.Body.String())
	}
	e.Close()
	c.LocalUser = "bea"
	e, err = NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	person, ok := e.localEngine().Config.person(task.Owner)
	if !ok || person.ID != "alex" || task.Owner != "local:alex" {
		t.Fatal("changing the local default reassigned an existing commitment", task.Owner, person)
	}
	if e.localEngine().acceptsOwner("local") {
		t.Fatal("unattributed legacy local task was assigned to the new default")
	}
}

func TestSoulRecoveryIsGlobalAndSurvivesRestart(t *testing.T) {
	const next = "# Alina\nI make space for different perspectives and revise my assumptions.\n"
	t.Run("one fallback", func(t *testing.T) {
		e := familyEngine(t, nil)
		old, _ := e.Memory.Soul()
		if err := e.Memory.reviseSoul(e.ctx, time.Now(), old, next, "A considered change."); err != nil {
			t.Fatal(err)
		}
		if current, _ := e.Memory.Soul(); current != next {
			t.Fatal(current)
		}
		for _, name := range []string{"soul.md", "soul.last.md"} {
			if err := os.Remove(filepath.Join(e.AdminDir, name)); err != nil {
				t.Fatal(err)
			}
		}
		for _, engine := range e.engines() {
			if got, _ := engine.Memory.Soul(); got != next {
				t.Fatal("family used a different last-known orientation", engine.Scope, got)
			}
		}
		if err := writeText(filepath.Join(e.AdminDir, "soul.last.md"), old); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.Memory.Soul(); got != next {
			t.Fatal("an older disk backup replaced the latest known orientation", got)
		}
	})
	t.Run("missing primary file", func(t *testing.T) {
		dir := t.TempDir()
		c := familyConfig(dir)
		e, err := NewEngine(dir, c, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		old, _ := e.Memory.Soul()
		if err = e.Memory.reviseSoul(e.ctx, time.Now(), old, next, "A considered change."); err != nil {
			e.Close()
			t.Fatal(err)
		}
		e.Close()
		if err = os.Remove(filepath.Join(dir, "soul.md")); err != nil {
			t.Fatal(err)
		}
		e, err = NewEngine(dir, c, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		if got, _ := e.Memory.Soul(); got != next {
			t.Fatal("restart replaced the recoverable soul with the seed", got)
		}
	})
}

func TestPairingKeepsNewPersonsFirstMessage(t *testing.T) {
	c := TelegramConfig{Token: "fixture", Binding: "pair"}
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		var request struct{ Offset int64 }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		response := `{"ok":true,"result":[]}`
		if request.Offset == 0 {
			response = `{"ok":true,"result":[{"update_id":1,"message":{"text":"/start pairing-code","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":2,"message":{"text":"Hello Alina","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	tg := NewTelegram(t.TempDir(), c, nil, client)
	id, err := pairTelegram(context.Background(), tg, "pairing-code")
	if err != nil || id != 42 {
		t.Fatal(id, err)
	}
	tg = NewTelegram(tg.Dir, c, nil, client)
	if len(tg.state.Pending) != 1 || tg.state.Pending[0].Message.Text != "Hello Alina" {
		t.Fatal("the newly paired person's next message was acknowledged and discarded", tg.state)
	}
}

func TestUnreachableCommandReplyDoesNotBlockTelegramInbox(t *testing.T) {
	e := familyEngine(t, nil)
	ctx, cancel := context.WithTimeout(e.ctx, time.Second)
	defer cancel()
	sent := map[int64]int{}
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		response := `{"ok":true,"result":{}}`
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var request struct{ Offset int64 }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			if request.Offset == 0 {
				response = `{"ok":true,"result":[{"update_id":1,"message":{"text":"/help","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":2,"message":{"text":"/help","from":{"id":84},"chat":{"id":84,"type":"private"}}}]}`
			} else {
				cancel()
				return nil, ctx.Err()
			}
		} else {
			var body struct {
				ChatID int64 `json:"chat_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			sent[body.ChatID]++
			if body.ChatID == 42 {
				response = `{"ok":false,"error_code":403}`
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	tg := NewTelegram(e.AdminDir, e.Config.Telegram, e, client)
	tg.Run(ctx)
	if sent[42] != 1 || sent[84] != 1 || tg.state.Offset != 3 {
		t.Fatal("one undeliverable command reply stopped the bot's incoming stream", sent, tg.state.Offset)
	}
}

func TestDoctorRepairDoesNotFollowMemoryDirectorySymlink(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(dir, c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	external := filepath.Join(t.TempDir(), "external-memory")
	if err = os.Rename(filepath.Join(dir, "memory"), external); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(external, "memory.sqlite")
	if err = os.Chmod(db, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(external, filepath.Join(dir, "memory")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_ = doctorCLI(context.Background(), dir, []string{"--fix"}, &out)
	info, err := os.Stat(db)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("doctor followed a parent symlink to repair another location", err, out.String())
	}
}
