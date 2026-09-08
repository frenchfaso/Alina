package alina

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"
)

const Version = "0.2.0-poc"

type Config struct {
	Version        int            `json:"version"`
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	OpenCodeKey    string         `json:"opencode_key,omitempty"`
	OpenCodeAPI    string         `json:"opencode_api,omitempty"`
	Search         SearchConfig   `json:"search"`
	Telegram       TelegramConfig `json:"telegram"`
	WorkDir        string         `json:"work_dir"`
	MaxSteps       int            `json:"max_steps"`
	CommandTimeout int            `json:"command_timeout_seconds"`
	Timezone       string         `json:"timezone"`
	Location       string         `json:"location,omitempty"`
	NetworkPolicy  string         `json:"network_policy"`
	Autonomy       AutonomyConfig `json:"autonomy"`
	Memory         MemoryConfig   `json:"memory"`
}
type AutonomyConfig struct {
	Enabled  bool   `json:"enabled"`
	Scope    string `json:"scope"`
	MaxCalls int    `json:"max_calls_per_day"`
	Minutes  int    `json:"minutes_per_run"`
	Search   bool   `json:"web_search"`
}
type MemoryConfig struct {
	Enabled        bool   `json:"enabled"`
	Dream          bool   `json:"dream"`
	DreamCron      string `json:"dream_cron"`
	CatchUp        bool   `json:"catch_up"`
	EmbeddingURL   string `json:"embedding_url,omitempty"`
	EmbeddingModel string `json:"embedding_model,omitempty"`
	EmbeddingKey   string `json:"embedding_key,omitempty"`
}
type SearchConfig struct {
	Default     string `json:"default"`
	TavilyKey   string `json:"tavily_key,omitempty"`
	BraveKey    string `json:"brave_key,omitempty"`
	OpenAIKey   string `json:"openai_key,omitempty"`
	OpenAIModel string `json:"openai_model,omitempty"`
}
type TelegramConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
	OwnerID int64  `json:"owner_id,omitempty"`
}

func Home() string {
	if s := os.Getenv("ALINA_HOME"); s != "" {
		return s
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "alina")
}
func DefaultConfig() Config {
	d, _ := os.UserHomeDir()
	return Config{Version: 1, NetworkPolicy: "strict", Autonomy: AutonomyConfig{MaxCalls: 12, Minutes: 5, Scope: "Explore installed tools and develop useful procedures inside your personal workspace."}, Provider: "chatgpt", Model: "gpt-5.4", WorkDir: d, MaxSteps: 20, CommandTimeout: 120, Timezone: "Local", Memory: MemoryConfig{Enabled: true, Dream: true, DreamCron: "0 3 * * *", CatchUp: true}, OpenCodeAPI: "chat", Search: SearchConfig{Default: "openai", OpenAIModel: "gpt-5.4"}}
}
func LoadConfig(dir string) (Config, error) {
	c := DefaultConfig()
	b, e := os.ReadFile(filepath.Join(dir, "config.json"))
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.NetworkPolicy != "strict" && c.NetworkPolicy != "declared" {
		return errors.New("network_policy must be strict or declared")
	}
	if c.Autonomy.MaxCalls < 1 || c.Autonomy.MaxCalls > 100 || c.Autonomy.Minutes < 1 || c.Autonomy.Minutes > 30 || len(c.Autonomy.Scope) > 2000 || c.Autonomy.Enabled && strings.TrimSpace(c.Autonomy.Scope) == "" {
		return errors.New("invalid autonomy scope or budget")
	}
	if c.Autonomy.Enabled && !c.Memory.Enabled {
		return errors.New("personal exploration requires memory")
	}
	if c.Version != 1 {
		return errors.New("unsupported config version")
	}
	if c.Provider != "chatgpt" && c.Provider != "opencode-go" {
		return errors.New("provider must be chatgpt or opencode-go")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("model is required")
	}
	if c.OpenCodeAPI != "chat" && c.OpenCodeAPI != "responses" && c.OpenCodeAPI != "messages" {
		return errors.New("invalid opencode_api")
	}
	if !filepath.IsAbs(c.WorkDir) {
		return errors.New("work_dir must be absolute")
	}
	if c.MaxSteps < 1 || c.MaxSteps > 100 || c.CommandTimeout < 1 || c.CommandTimeout > 3600 {
		return errors.New("invalid execution limits")
	}
	if c.Search.Default != "openai" && c.Search.Default != "tavily" && c.Search.Default != "brave" && c.Search.Default != "none" {
		return errors.New("invalid search provider")
	}
	if c.Telegram.Enabled && (c.Telegram.Token == "" || c.Telegram.OwnerID <= 0) {
		return errors.New("Telegram requires a bot token and a positive owner ID")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	if c.Memory.Dream {
		if _, err := parseSchedule(c.Memory.DreamCron, c.Timezone); err != nil {
			return err
		}
	}
	if c.Memory.EmbeddingURL != "" {
		if err := validateEmbedding(c.Memory); err != nil {
			return err
		}
	}
	return nil
}
func SaveConfig(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "config.json"), c)
}
func writeJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".alina-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func randomID() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func safeID(s string) bool {
	if len(s) < 1 || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func configError(err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("run 'alina setup' first")
	}
	return err
}
