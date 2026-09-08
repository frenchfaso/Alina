package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type diagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`
}
type diagnosticReport struct {
	Schema   int               `json:"schema"`
	Version  string            `json:"version"`
	Platform string            `json:"platform"`
	OK       bool              `json:"ok"`
	Running  bool              `json:"running"`
	Checks   []diagnosticCheck `json:"checks"`
}

func doctorCLI(ctx context.Context, dir string, args []string, out io.Writer) error {
	live, fix := false, false
	if len(args) > 1 {
		return errors.New("usage: alina doctor [--live|--fix]")
	}
	if len(args) == 1 {
		switch args[0] {
		case "--live":
			live = true
		case "--fix":
			fix = true
		default:
			return errors.New("usage: alina doctor [--live|--fix]")
		}
	}
	return diagnose(ctx, dir, live, fix, out, newHTTPClient())
}
func diagnose(ctx context.Context, dir string, live, fix bool, out io.Writer, client *http.Client) error {
	report := diagnosticReport{Schema: 1, Version: Version, Platform: runtime.GOOS + "/" + runtime.GOARCH, OK: true, Checks: []diagnosticCheck{}}
	add := func(name, status, hint string, details any) {
		report.Checks = append(report.Checks, diagnosticCheck{name, status, hint, details})
		if status == "error" {
			report.OK = false
		}
	}
	if fix || live {
		lock, err := lockDaemonState(dir)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	var status struct {
		Logging map[string]any `json:"logging"`
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	report.Running = localRequest(probe, dir, "GET", "/v1/status", nil, &status) == nil
	cancel()
	if report.Running {
		add("service", "ok", "", nil)
		if ok, _ := status.Logging["ok"].(bool); !ok {
			add("logging", "error", "Inspect log directory permissions and free disk space.", status.Logging)
		} else {
			add("logging", "ok", "", status.Logging)
		}
	} else {
		add("service", "warn", "alina serve", map[string]any{"running": false})
	}
	if !report.Running {
		if info, err := os.Lstat(socketPath(dir)); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				add("socket", "error", "The socket path is occupied by another file; inspect it before moving it.", nil)
			} else if fix {
				if err = os.Remove(socketPath(dir)); err != nil {
					add("socket", "error", "Inspect socket permissions.", errorInfo(err))
				} else {
					add("socket", "ok", "", map[string]bool{"repaired": true})
				}
			} else {
				add("socket", "warn", "alina doctor --fix", map[string]string{"state": "unreachable"})
			}
		}
	}
	c, configErr := checkedConfig(dir)
	if configErr != nil {
		add("config", "error", "alina config check; use alina config apply to repair fields, or alina setup for a new installation.", errorInfo(configErr))
	} else {
		add("config", "ok", "", map[string]any{"provider": c.Provider, "model": c.Model, "context_tokens": c.ContextTokens, "reasoning": c.ReasoningEffort, "dream_reasoning": c.DreamEffort, "timezone": c.Timezone})
		info, err := os.Stat(c.WorkDir)
		if err != nil || !info.IsDir() {
			add("work_dir", "error", "Set work_dir to an existing directory with alina config apply.", nil)
		} else {
			add("work_dir", "ok", "", nil)
		}
		if c.Provider == "chatgpt" || c.Search.Default == "openai" && c.Search.OpenAIKey == "" {
			var credential Credential
			b, err := os.ReadFile(filepath.Join(dir, "chatgpt.json"))
			if err == nil {
				err = json.Unmarshal(b, &credential)
			}
			if err != nil || credential.Access == "" || credential.AccountID == "" {
				add("chatgpt_auth", "error", "alina setup login", nil)
			} else if credential.Expires <= time.Now().Unix()+60 && credential.Refresh == "" {
				add("chatgpt_auth", "error", "alina setup login", map[string]string{"state": "expired"})
			} else {
				add("chatgpt_auth", "ok", "", map[string]bool{"refresh_needed": credential.Expires <= time.Now().Unix()+60})
			}
		}
		if c.Provider == "opencode-go" && c.OpenCodeKey == "" {
			add("opencode_auth", "error", "Set opencode_key with alina config apply.", nil)
		}
		if c.Search.Default == "tavily" && c.Search.TavilyKey == "" || c.Search.Default == "brave" && c.Search.BraveKey == "" {
			add("search_auth", "error", "Set the selected search provider key with alina config apply.", nil)
		}
	}
	for _, name := range []string{"config.json", "chatgpt.json", "telegram.json", "tasks.json", "grants.json", "logs/alina.jsonl", "logs/alina.jsonl.1", "logs/alina.jsonl.2"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			add("file:"+name, "error", "Inspect the file.", errorInfo(err))
			continue
		}
		if !info.Mode().IsRegular() {
			add("file:"+name, "error", "Expected a regular file; automatic repair will not follow symlinks.", nil)
			continue
		}
		if info.Mode().Perm() != 0600 {
			if fix {
				err = os.Chmod(path, 0600)
				if err == nil {
					add("permissions:"+name, "ok", "", map[string]bool{"repaired": true})
				}
			}
			if !fix || err != nil {
				add("permissions:"+name, "error", "alina doctor --fix", errorInfo(err))
			}
		}
		if name == "telegram.json" || name == "tasks.json" || name == "grants.json" {
			b, err := readSmallFile(path, 2<<20)
			if err != nil || !json.Valid([]byte(b)) {
				add("state:"+name, "error", "Inspect the file and restore a known-good backup; it will not be overwritten.", errorInfo(err))
			}
		}
	}
	path := filepath.Join(dir, "memory", "memory.sqlite")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		add("memory", "warn", "alina serve initializes local state.", nil)
	} else if err != nil {
		add("memory", "error", "Inspect the memory directory.", errorInfo(err))
	} else {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite3", u.String())
		if err == nil {
			var result string
			err = db.QueryRowContext(checkCtx, "PRAGMA quick_check").Scan(&result)
			if err == nil && result != "ok" {
				err = errors.New("database integrity check failed")
			}
			db.Close()
		}
		cancel()
		if err != nil {
			add("memory", "error", "Inspect the database and backups; automatic repair does not rewrite memory.", errorInfo(err))
		} else {
			add("memory", "ok", "", nil)
		}
	}
	if soul, err := readSmallFile(filepath.Join(dir, "soul.md"), 1600); err != nil || !validSoul(soul) {
		add("soul", "warn", "Inspect soul.md; Alina uses its last valid orientation if needed.", nil)
	} else {
		add("soul", "ok", "", nil)
	}
	converter, _ := exec.LookPath("markitdown")
	add("optional_converter", "info", "Optional; see workspace/procedures/markitdown.md.", map[string]bool{"available": converter != ""})
	if live && configErr == nil {
		p := &Provider{Config: c, Client: client, Auth: &Auth{Dir: dir, Client: client}}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		m, err := p.Complete(checkCtx, "doctor-"+randomID(), []Message{{Role: "system", Content: "Reply briefly."}, {Role: "user", Content: "Reply with ALINA_OK only."}}, nil, nil)
		cancel()
		if err == nil && (strings.TrimSpace(m.Content) != "ALINA_OK" || len(m.Calls) != 0) {
			err = errors.New("unexpected provider response")
		}
		if err != nil {
			add("model_live", "error", "Check provider access, credentials and selected model.", errorInfo(err))
		} else {
			add("model_live", "ok", "", m.Usage)
		}
		if c.Search.Default != "none" {
			s := Search{Config: c.Search, Provider: p, Client: client}
			checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			result, err := s.Run(checkCtx, "doctor-"+randomID(), "", "Termux official website")
			cancel()
			if err == nil && strings.TrimSpace(result) == "" {
				err = errors.New("empty search response")
			}
			if err != nil {
				add("search_live", "error", "Check the selected search provider; alina setup can retry or change it.", errorInfo(err))
			} else {
				add("search_live", "ok", "", map[string]string{"provider": c.Search.Default})
			}
		}
		if c.Telegram.Enabled {
			tg := NewTelegram(dir, c.Telegram, nil, client)
			var me struct {
				Username string `json:"username"`
			}
			err := tg.api(ctx, "getMe", nil, &me)
			if err == nil && me.Username == "" {
				err = errors.New("invalid bot response")
			}
			if err == nil {
				var webhook struct {
					URL string `json:"url"`
				}
				err = tg.api(ctx, "getWebhookInfo", nil, &webhook)
				if err == nil && webhook.URL != "" {
					err = errors.New("bot has an active webhook; use a dedicated bot")
				}
			}
			if err != nil {
				add("telegram_live", "error", "alina setup telegram", errorInfo(err))
			} else {
				add("telegram_live", "ok", "", nil)
			}
		}
		if c.Memory.Enabled && c.Memory.EmbeddingURL != "" {
			m := Memory{Config: c}
			_, err := m.embedding(ctx, "Alina embedding connectivity check")
			if err != nil {
				add("embedding_live", "error", "Check the configured embedding endpoint.", errorInfo(err))
			} else {
				add("embedding_live", "ok", "", nil)
			}
		}
	}
	if err := printJSON(out, report); err != nil {
		return err
	}
	if !report.OK {
		return fmt.Errorf("diagnostics found errors; see checks in the JSON report")
	}
	return nil
}
