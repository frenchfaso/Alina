package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

type setupFixture struct {
	t                                          *testing.T
	out                                        bytes.Buffer
	calls                                      []string
	modelFailures, searchFailures, botFailures int
	webhook                                    bool
	ack                                        int64
}

func (f *setupFixture) client() *http.Client {
	return &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		f.calls = append(f.calls, r.URL.Host+r.URL.Path)
		status, body := 200, ""
		switch {
		case strings.HasSuffix(r.URL.Path, "/deviceauth/usercode"):
			body = `{"device_auth_id":"fixture","user_code":"ABCD-1234","interval":2}`
		case strings.HasSuffix(r.URL.Path, "/deviceauth/token"):
			body = `{"authorization_code":"fixture-code","code_verifier":"fixture-verifier"}`
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`))
			body = fmt.Sprintf(`{"access_token":"a.%s.b","refresh_token":"fixture-refresh","expires_in":3600}`, payload)
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			if f.botFailures > 0 {
				f.botFailures--
				status = 401
			}
			body = `{"ok":true,"result":{"username":"alina_fixture_bot"}}`
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			url := ""
			if f.webhook {
				url = "https://example.org/webhook"
			}
			body = fmt.Sprintf(`{"ok":true,"result":{"url":%q}}`, url)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			var request struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				f.t.Fatal(err)
			}
			if request.Offset > 0 {
				f.ack = request.Offset
				body = `{"ok":true,"result":[]}`
			} else {
				codes := regexp.MustCompile(`\?start=(alina-[a-z0-9]+)`).FindAllStringSubmatch(f.out.String(), -1)
				if len(codes) == 0 {
					f.t.Fatal("pairing URL not shown")
				}
				code := codes[len(codes)-1][1]
				body = fmt.Sprintf(`{"ok":true,"result":[{"update_id":7,"message":{"text":"old command","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":8,"message":{"text":"/start %s","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, code)
			}
		case strings.HasSuffix(r.URL.Path, "/responses"):
			var request struct {
				Tools []struct {
					Type string `json:"type"`
				} `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				f.t.Fatal(err)
			}
			answer := "ALINA_OK"
			if len(request.Tools) > 0 && request.Tools[0].Type == "web_search" {
				answer = "Termux — https://termux.dev"
				if f.searchFailures > 0 {
					f.searchFailures--
					status = 403
				}
			} else if f.modelFailures > 0 {
				f.modelFailures--
				status = 503
			}
			body = fmt.Sprintf(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, answer)
		case r.URL.Host == "api.tavily.com":
			body = `{"results":[{"title":"Termux","url":"https://termux.dev","content":"Official site"}]}`
		default:
			f.t.Fatalf("unexpected network call %s", r.URL)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func setupCredential(t *testing.T, dir string) {
	t.Helper()
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), Credential{Access: "fixture-access", Refresh: "fixture-refresh", AccountID: "fixture-account", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
}

func (f *setupFixture) run(dir, input string, telegramOnly bool) error {
	return setupQuick(context.Background(), dir, bufio.NewReader(strings.NewReader(input)), &f.out, f.client(), telegramOnly)
}

func TestQuickSetupFullOnboarding(t *testing.T) {
	t.Setenv("TZ", "Europe/Rome")
	dir := t.TempDir()
	f := &setupFixture{t: t}
	if err := f.run(dir, "\nfixture-bot-token\nAlex\nhome\nn\n", false); err != nil {
		t.Fatal(err, f.out.String())
	}
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "chatgpt" || c.Model != defaultModel || c.ContextTokens != astraContextTokens || c.ReasoningEffort != "medium" || c.DreamEffort != "high" {
		t.Fatal("model defaults changed")
	}
	if !c.Telegram.Enabled || c.Telegram.OwnerID != 42 || c.Telegram.Binding == "" || f.ack != 9 {
		t.Fatal("pairing incomplete", c.Telegram.OwnerID, f.ack)
	}
	if !c.Memory.Enabled || !c.Memory.Dream || !c.Memory.CatchUp || !c.Autonomy.Enabled || !c.Autonomy.Search || c.Search.Default != "openai" || c.NetworkPolicy != "declared" || c.Timezone != "Europe/Rome" {
		t.Fatal("features not enabled")
	}
	for _, name := range []string{"config.json", "chatgpt.json", "memory/memory.sqlite", "soul.md", "tasks.json", "workspace/procedures/markitdown.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(name, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal(name, "not private", info.Mode())
		}
	}
	for _, secret := range []string{"fixture-bot-token", "fixture-refresh", "fixture-access"} {
		if strings.Contains(f.out.String(), secret) {
			t.Fatal("secret printed")
		}
	}
	for _, unwanted := range []string{"Salvare", "Model ID", "Reasoning:", "Context window", "API key", "user ID", "0 3 * * *"} {
		if strings.Contains(f.out.String(), unwanted) {
			t.Fatal("unnecessary setup question", unwanted)
		}
	}
	if !strings.Contains(f.out.String(), "Pronta") {
		t.Fatal(f.out.String())
	}
	for _, call := range f.calls {
		if strings.Contains(call, "sendMessage") {
			t.Fatal("setup sent a Telegram message")
		}
	}
}

func TestQuickSetupReusesAndPreservesConfiguration(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = dir
	c.Telegram = TelegramConfig{Enabled: true, Token: "fixture-existing", OwnerID: 42, Binding: "existing"}
	c.Users = []User{{ID: "owner", Name: "Alex", TelegramID: 42, Family: "home"}}
	c.LocalUser = "owner"
	c.Search.TavilyKey = "unused-secret"
	c.Memory.EmbeddingURL, c.Memory.EmbeddingModel = "https://example.org/embeddings", "custom-model"
	c.ReasoningEffort, c.Memory.DreamCron = "high", "30 4 * * *"
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	setupCredential(t, dir)
	f := &setupFixture{t: t}
	if err := f.run(dir, "", false); err != nil {
		t.Fatal(err, f.out.String())
	}
	after, err := LoadConfig(dir)
	if err != nil || !reflect.DeepEqual(c, after) {
		t.Fatal("existing settings overwritten", err)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "auth.openai.com") || strings.Contains(call, "getUpdates") || strings.Contains(call, "tavily") {
			t.Fatal("unnecessary account call", call)
		}
	}
	if strings.Contains(f.out.String(), "Token Telegram") || strings.Contains(f.out.String(), "fixture-existing") {
		t.Fatal("unnecessary prompt or exposed token")
	}
}

func TestQuickSetupFailureRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, input                   string
		modelFailures, searchFailures int
		wantError                     bool
		search                        string
	}{
		{"model retry", "\ns\n", 1, 0, false, "openai"},
		{"model stop", "\nn\n", 1, 0, true, "openai"},
		{"search retry", "\n1\n", 0, 1, false, "openai"},
		{"search fallback", "\n2\nfixture-tavily\n", 0, 1, false, "tavily"},
		{"search deferred", "\n4\n", 0, 1, false, "none"},
		{"search EOF", "\n", 0, 1, true, "openai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			setupCredential(t, dir)
			f := &setupFixture{t: t, modelFailures: tc.modelFailures, searchFailures: tc.searchFailures}
			err := f.run(dir, tc.input, false)
			if (err != nil) != tc.wantError {
				t.Fatal(err, f.out.String())
			}
			c, loadErr := LoadConfig(dir)
			if loadErr != nil || c.Search.Default != tc.search {
				t.Fatal("progress not saved", loadErr)
			}
			if tc.wantError && strings.Contains(f.out.String(), "Pronta") {
				t.Fatal("false readiness")
			}
			if c.Telegram.Enabled || !strings.Contains(f.out.String(), "Telegram non collegato") && !tc.wantError {
				t.Fatal("deferred Telegram not disclosed")
			}
		})
	}
}

func TestQuickSetupCancellationAndInvalidConfig(t *testing.T) {
	for _, existing := range []string{"", `{broken`} {
		dir := t.TempDir()
		if existing != "" {
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
		}
		f := &setupFixture{t: t}
		if err := f.run(dir, "", false); err == nil {
			t.Fatal("EOF/invalid config accepted")
		}
		if len(f.calls) > 0 {
			t.Fatal("network access before input")
		}
		b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if string(b) != existing {
			t.Fatal("configuration overwritten")
		}
	}
}

func TestSetupTelegramFocusedAndRetry(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.Telegram = TelegramConfig{Enabled: true, Token: "old-bot", OwnerID: 24}
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	f := &setupFixture{t: t, botFailures: 1}
	if err := f.run(dir, "bad-bot\n2\nnew-bot\nAlex\nhome\nn\n", true); err != nil {
		t.Fatal(err, f.out.String())
	}
	after, err := LoadConfig(dir)
	if err != nil || after.Telegram.Token != "new-bot" || after.Telegram.OwnerID != 42 || after.Telegram.Binding == "" {
		t.Fatal("bot not updated", err)
	}
	c.Telegram = after.Telegram
	c.Users, c.LocalUser = after.Users, after.LocalUser
	if !reflect.DeepEqual(c, after) {
		t.Fatal("focused setup changed other options")
	}
	for _, call := range f.calls {
		if !strings.HasPrefix(call, "api.telegram.org/") {
			t.Fatal("unrelated account requested")
		}
	}
}

func TestSetupTelegramWebhookAndEOF(t *testing.T) {
	for _, input := range []string{"fixture-token\n3\n", "fixture-token\n"} {
		dir := t.TempDir()
		c := DefaultConfig()
		if err := SaveConfig(dir, c); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		f := &setupFixture{t: t, webhook: true}
		err := f.run(dir, input, true)
		if strings.HasSuffix(input, "3\n") && err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(input, "3\n") && err == nil {
			t.Fatal("EOF accepted")
		}
		after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("failed pairing changed configuration")
		}
		for _, call := range f.calls {
			if strings.Contains(call, "getUpdates") || strings.Contains(call, "deleteWebhook") {
				t.Fatal("interfered with existing webhook")
			}
		}
	}
}

func TestTelegramBindingIsolatesReconfiguredBot(t *testing.T) {
	dir := t.TempDir()
	old := NewTelegram(dir, TelegramConfig{OwnerID: 42, Binding: "old"}, nil, nil)
	old.state.Binding = "old"
	old.state.Offset = 9999
	old.state.Sessions["42"] = "old-session"
	old.state.Delivered["old-job"] = "done"
	if err := old.saveLocked(); err != nil {
		t.Fatal(err)
	}
	same := NewTelegram(dir, old.Config, nil, nil)
	if same.state.Offset != 9999 {
		t.Fatal("existing offset lost")
	}
	fresh := NewTelegram(dir, TelegramConfig{OwnerID: 42, Binding: "new"}, nil, nil)
	if fresh.state.Offset != 0 || len(fresh.state.Sessions) != 0 || len(fresh.state.Delivered) != 0 {
		t.Fatal("new bot inherited old receipts")
	}
	if fresh.owner() == old.owner() || fresh.updateKey(1) == old.updateKey(1) {
		t.Fatal("old jobs can collide with the new bot")
	}
	legacy := NewTelegram(t.TempDir(), TelegramConfig{OwnerID: 42}, nil, nil)
	if legacy.owner() != "telegram:42" || legacy.updateKey(1) != "tg1" {
		t.Fatal("legacy binding changed")
	}
}

func TestWizardInvalidChoiceAndCancellation(t *testing.T) {
	var out bytes.Buffer
	w := &wizard{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("maybe\ns\ninvalid\n2\n")), out: &out}
	if !w.yes("Start", true) || w.choice("Choice", "1", "1", "2") != "2" || w.err != nil {
		t.Fatal("invalid answers not retried", w.err)
	}
	if w.yes("Start", true) || !errors.Is(w.err, io.EOF) {
		t.Fatal("EOF meant yes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = &wizard{ctx: ctx, in: bufio.NewReader(strings.NewReader("")), out: &out}
	w.secret("Secret", "")
	if w.err == nil {
		t.Fatal("secret ignored cancellation")
	}
}
