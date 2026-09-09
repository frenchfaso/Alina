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

const Version = "0.14.4-poc"

// Codex ChatGPT model catalog, 2026-09-08. These are backend limits, not
// the larger public API window. ContextTokens remains user configurable.
const defaultModel = "gpt-6-astra"
const astraContextTokens = 272000
const astraMaxContextTokens = 872000

type Config struct {
	Version          int            `json:"version"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	ReasoningEffort  string         `json:"reasoning_effort"`
	CheckpointEffort string         `json:"checkpoint_reasoning_effort"`
	DreamEffort      string         `json:"dream_reasoning_effort"`
	Verbosity        string         `json:"verbosity"`
	ModelTimeout     int            `json:"model_timeout_seconds"`
	OpenCodeKey      string         `json:"opencode_key,omitempty"`
	OpenCodeAPI      string         `json:"opencode_api,omitempty"`
	Search           SearchConfig   `json:"search"`
	Telegram         TelegramConfig `json:"telegram"`
	Users            []User         `json:"users,omitempty"`
	LocalUser        string         `json:"local_user,omitempty"`
	WorkDir          string         `json:"work_dir"`
	MaxSteps         int            `json:"max_steps"`
	ContextTokens    int            `json:"context_tokens"`
	CommandTimeout   int            `json:"command_timeout_seconds"`
	Timezone         string         `json:"timezone"`
	Location         string         `json:"location,omitempty"`
	NetworkPolicy    string         `json:"network_policy"`
	Autonomy         AutonomyConfig `json:"autonomy"`
	Memory           MemoryConfig   `json:"memory"`
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
	Binding string `json:"binding,omitempty"`
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
	return Config{
		Version: 1, Provider: "chatgpt", Model: defaultModel,
		ReasoningEffort: "medium", DreamEffort: "high", CheckpointEffort: "low", Verbosity: "low",
		ContextTokens: astraContextTokens, ModelTimeout: 600, MaxSteps: 20, CommandTimeout: 120,
		WorkDir: d, Timezone: "Local", NetworkPolicy: "strict", OpenCodeAPI: "chat",
		Search:   SearchConfig{Default: "openai"},
		Memory:   MemoryConfig{Enabled: true, Dream: true, DreamCron: "0 3 * * *", CatchUp: true},
		Autonomy: AutonomyConfig{MaxCalls: 12, Minutes: 5, Scope: "Explore installed tools and develop useful procedures inside your personal workspace."},
	}
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
	// Migrate the retired POC default without replacing other selected models
	// or custom context budgets. Disk changes still go through setup/save.
	if c.Provider == "chatgpt" && c.Model == "gpt-5.4" {
		c.Model = defaultModel
		if c.ContextTokens == 32768 {
			c.ContextTokens = astraContextTokens
		}
	}
	if c.Search.OpenAIModel == "gpt-5.4" && c.Search.OpenAIKey == "" {
		c.Search.OpenAIModel = ""
	}
	if err := validatePeopleState(dir, c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if err := c.validatePeople(); err != nil {
		return err
	}
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
	for _, effort := range []string{c.ReasoningEffort, c.CheckpointEffort, c.DreamEffort} {
		if effort != "low" && effort != "medium" && effort != "high" && effort != "xhigh" && effort != "max" {
			return errors.New("reasoning effort must be low, medium, high, xhigh or max")
		}
	}
	if c.Verbosity != "low" && c.Verbosity != "medium" && c.Verbosity != "high" {
		return errors.New("verbosity must be low, medium or high")
	}
	if c.ModelTimeout < 30 || c.ModelTimeout > 3600 {
		return errors.New("model_timeout_seconds must be between 30 and 3600")
	}
	if c.Model == defaultModel && c.Provider == "opencode-go" && c.OpenCodeAPI != "responses" {
		return errors.New("GPT-6 Astra tool calling requires the responses protocol")
	}
	if c.Provider == "chatgpt" && c.Model == defaultModel && c.ContextTokens > astraMaxContextTokens {
		return fmt.Errorf("ChatGPT Astra context_tokens must not exceed %d", astraMaxContextTokens)
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
	if c.ContextTokens < 8192 || c.ContextTokens > 2000000 {
		return errors.New("context_tokens must be between 8192 and 2000000")
	}
	if c.Search.Default != "openai" && c.Search.Default != "tavily" && c.Search.Default != "brave" && c.Search.Default != "none" {
		return errors.New("invalid search provider")
	}
	if c.Telegram.Binding != "" && (!safeID(c.Telegram.Binding) || len(c.Telegram.Binding) > 32) {
		return errors.New("invalid Telegram binding")
	}
	if c.Telegram.Enabled && (c.Telegram.Token == "" || c.Telegram.OwnerID <= 0 && len(c.Users) == 0) {
		return errors.New("Telegram requires a bot token and a positive owner ID")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	// The scheduler loads this expression even while reflection is disabled.
	if _, err := parseSchedule(c.Memory.DreamCron, c.Timezone); err != nil {
		return err
	}
	if c.Memory.EmbeddingURL != "" {
		if err := validateEmbedding(c.Memory); err != nil {
			return err
		}
	}
	return nil
}
func SaveConfig(dir string, c Config) error {
	if err := validatePeopleState(dir, c); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "config.json"), c)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return writeText(path, string(b)+"\n")
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
