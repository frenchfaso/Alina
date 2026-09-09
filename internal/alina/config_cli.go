package alina

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var secretConfigPaths = [][]string{{"opencode_key"}, {"search", "tavily_key"}, {"search", "brave_key"}, {"search", "openai_key"}, {"telegram", "token"}, {"memory", "embedding_key"}}

const secretPlaceholder = "[redacted]"

func configObject(c Config) map[string]any {
	var obj map[string]any
	_ = json.Unmarshal([]byte(jsonText(c)), &obj)
	return obj
}
func secretParent(obj map[string]any, path []string) map[string]any {
	for _, key := range path[:len(path)-1] {
		next, ok := obj[key].(map[string]any)
		if !ok {
			return nil
		}
		obj = next
	}
	return obj
}
func redactedConfig(c Config) map[string]any {
	obj := configObject(c)
	for _, path := range secretConfigPaths {
		if parent := secretParent(obj, path); parent != nil {
			key := path[len(path)-1]
			if s, _ := parent[key].(string); s != "" {
				parent[key] = secretPlaceholder
			}
		}
	}
	return obj
}
func mergeConfig(dst, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(dst, key)
			continue
		}
		if obj, ok := value.(map[string]any); ok {
			current, ok := dst[key].(map[string]any)
			if !ok {
				current = map[string]any{}
			}
			mergeConfig(current, obj)
			dst[key] = current
		} else {
			dst[key] = value
		}
	}
}
func configCLI(ctx context.Context, dir string, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "check" {
		c, err := checkedConfig(dir)
		if err != nil {
			return configError(err)
		}
		if len(args) == 1 {
			return printJSON(out, map[string]any{"ok": true})
		}
		return printJSON(out, redactedConfig(c))
	}
	if len(args) != 1 || args[0] != "apply" {
		return errors.New("usage: alina config [check|apply]; apply reads a JSON merge patch from stdin")
	}
	patchBytes, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil {
		return err
	}
	if len(patchBytes) > 65536 {
		return errors.New("config patch exceeds 64 KiB")
	}
	var patch map[string]any
	if err = json.Unmarshal(patchBytes, &patch); err != nil || patch == nil {
		return errors.New("config patch must be a JSON object")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	lock, err := lockDaemonState(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	base := configObject(DefaultConfig())
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		// Read without Validate so a patch can repair an invalid field.
		var saved map[string]any
		if err = json.Unmarshal(raw, &saved); err != nil || saved == nil {
			return errors.New("config JSON is damaged; correct the file before applying a patch")
		}
		mergeConfig(base, saved)
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, path := range secretConfigPaths {
		parent := secretParent(patch, path)
		if parent == nil {
			continue
		}
		key := path[len(path)-1]
		if parent[key] == secretPlaceholder {
			original := secretParent(base, path)
			if original == nil || original[key] == nil {
				return errors.New("a redacted value cannot create a credential")
			}
			delete(parent, key)
		}
	}
	mergeConfig(base, patch)
	encoded, err := json.Marshal(base)
	if err != nil {
		return err
	}
	c := DefaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return err
	}
	if err = c.Validate(); err != nil {
		return err
	}
	previous, _ := LoadConfig(dir)
	if c.Telegram.Token != previous.Telegram.Token || len(c.Users) == 0 && c.Telegram.OwnerID != previous.Telegram.OwnerID {
		c.Telegram.Binding = randomID()
	}
	if err = SaveConfig(dir, c); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"ok": true, "config": redactedConfig(c), "next": "alina serve"})
}

func checkedConfig(dir string) (Config, error) {
	c, err := LoadConfig(dir)
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, err
	}
	var shape Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&shape); err != nil {
		return c, err
	}
	return c, nil
}
