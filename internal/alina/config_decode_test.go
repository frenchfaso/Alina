package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigReadersRejectUnknownFieldsAndRepairPreservesBinding(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.Telegram = TelegramConfig{Token: "fixture-secret", OwnerID: 42, Binding: "existing-binding"}
	obj := configObject(c)
	obj["memory"].(map[string]any)["dreem"] = false
	if err := writeJSON(filepath.Join(dir, "config.json"), obj); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	readers := map[string]func() error{
		"load":   func() error { _, err := LoadConfig(dir); return err },
		"config": func() error { return configCLI(ctx, dir, []string{"check"}, strings.NewReader(""), io.Discard) },
		"setup":  func() error { return SetupAdvanced(ctx, dir, bufio.NewReader(strings.NewReader("")), io.Discard) },
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			if err := read(); err == nil || !strings.Contains(err.Error(), `unknown field "dreem"`) {
				t.Fatal("reader accepted misspelled field", err)
			}
		})
	}
	var out bytes.Buffer
	if err := configCLI(ctx, dir, []string{"apply"}, strings.NewReader(`{"memory":{"dreem":null},"reasoning_effort":"high"}`), &out); err != nil {
		t.Fatal(err)
	}
	after, err := LoadConfig(dir)
	if err != nil || after.Telegram != c.Telegram || after.ReasoningEffort != "high" || strings.Contains(out.String(), c.Telegram.Token) {
		t.Fatal("repair lost binding, settings or redaction", err)
	}
}

func TestConfigDecodeRequiresOneObjectAndKeepsDefaults(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `{} {}`, `{} trailing`, `{"memory":{"dreem":true}}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := decodeConfig([]byte(raw)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	c, err := decodeConfig([]byte(" \n{} \t"))
	if err != nil || jsonText(c) != jsonText(DefaultConfig()) {
		t.Fatal("defaults lost", err)
	}
	// A top-level typo before credentials must also be repairable without
	// changing the bot binding or revealing the old token.
	dir := t.TempDir()
	raw := `{"aaa_typo":1,"telegram":{"token":"fixture","owner_id":42,"binding":"kept"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(`{"aaa_typo":null}`), io.Discard); err != nil {
		t.Fatal(err)
	}
	c, err = LoadConfig(dir)
	if err != nil || c.Telegram.Binding != "kept" || c.Telegram.Token != "fixture" {
		t.Fatal("unknown-field repair changed bot identity", err)
	}
}

func TestConfigRepairAcceptsWrongJSONTypes(t *testing.T) {
	for _, tc := range []struct{ field, raw, patch string }{
		{"context_tokens", `"oops"`, `{"context_tokens":272000}`},
		{"users", `false`, `{"users":[]}`},
		{"memory", `false`, `{"memory":null}`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			dir := t.TempDir()
			c := DefaultConfig()
			c.Telegram = TelegramConfig{Token: "fixture", OwnerID: 42, Binding: "kept"}
			obj := configObject(c)
			var bad any
			if err := json.Unmarshal([]byte(tc.raw), &bad); err != nil {
				t.Fatal(err)
			}
			obj[tc.field] = bad
			if err := writeJSON(filepath.Join(dir, "config.json"), obj); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(dir); err == nil {
				t.Fatal("invalid saved type accepted")
			}
			if err := configCLI(context.Background(), dir, []string{"apply"}, strings.NewReader(tc.patch), io.Discard); err != nil {
				t.Fatal("cannot repair invalid saved type", err)
			}
			after, err := LoadConfig(dir)
			if err != nil || after.Telegram != c.Telegram {
				t.Fatal("repair changed Telegram binding", err)
			}
		})
	}
}
