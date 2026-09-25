package repl

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
)

type Config struct {
	SystemPrompt string                    `koanf:"system_prompt"`
	Mode         string                    `koanf:"mode"`
	Model        ModelConfig               `koanf:"model"`
	Editor       EditorConfig              `koanf:"editor"`
	Appearance   AppearanceConfig          `koanf:"appearance"`
	History      HistoryConfig             `koanf:"history"`
	Providers    map[string]ProviderConfig `koanf:"providers"`
}

type ModelConfig struct {
	Provider            string `koanf:"provider"`
	ID                  string `koanf:"id"`
	ReasoningEffort     string `koanf:"reasoning_effort"`
	MaxAttempts         int    `koanf:"max_attempts"`
	ContextWindowTokens int    `koanf:"context_window_tokens"`
}

type EditorConfig struct {
	Mode      string `koanf:"mode"`
	MaxHeight int    `koanf:"max_height"`
	Command   string `koanf:"command"`
}

type AppearanceConfig struct {
	Theme string `koanf:"theme"`
}

type HistoryConfig struct {
	MaxEntries int `koanf:"max_entries"`
}

type ProviderConfig struct {
	BaseURL        string         `koanf:"base_url"`
	Models         []string       `koanf:"models"`
	ContextWindows map[string]int `koanf:"context_windows"`
}

type ConfigStore struct {
	mu     sync.Mutex
	path   string
	getenv func(string) string
	active Config
}

func ConfigPath(getenv func(string) string) (string, error) {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("HOME is unset; set HOME or XDG_CONFIG_HOME")
		}
		base = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_CONFIG_HOME must be an absolute path: %q", base)
	}
	return filepath.Join(base, "unreal-agent-repl", "config.toml"), nil
}

func DefaultConfig() Config {
	return Config{
		SystemPrompt: defaultSystemPrompt,
		Mode:         "default",
		Model:        ModelConfig{Provider: "openai", ReasoningEffort: "high", MaxAttempts: 5},
		Editor:       EditorConfig{Mode: "plain", MaxHeight: 10},
		Appearance:   AppearanceConfig{Theme: "auto"},
		History:      HistoryConfig{MaxEntries: 1000},
		Providers:    map[string]ProviderConfig{},
	}
}

func LoadConfig(getenv func(string) string) (*ConfigStore, error) {
	path, err := ConfigPath(getenv)
	if err != nil {
		return nil, err
	}
	store := &ConfigStore{path: path, getenv: getenv}
	config, err := store.read()
	if err != nil {
		return nil, err
	}
	store.active = config
	return store, nil
}

func (store *ConfigStore) Path() string { return store.path }

func (store *ConfigStore) Current() Config {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneConfig(store.active)
}

func cloneConfig(source Config) Config {
	copy := source
	copy.Providers = make(map[string]ProviderConfig, len(source.Providers))
	for name, provider := range source.Providers {
		provider.Models = append([]string(nil), provider.Models...)
		if provider.ContextWindows != nil {
			windows := make(map[string]int, len(provider.ContextWindows))
			for modelID, limit := range provider.ContextWindows {
				windows[modelID] = limit
			}
			provider.ContextWindows = windows
		}
		copy.Providers[name] = provider
	}
	return copy
}

func (store *ConfigStore) read() (Config, error) {
	k := koanf.New(".")
	defaults := DefaultConfig()
	if err := k.Load(confmap.Provider(map[string]any{
		"system_prompt": defaults.SystemPrompt,
		"mode":          defaults.Mode,
		"model": map[string]any{
			"provider": defaults.Model.Provider, "id": defaults.Model.ID,
			"reasoning_effort": defaults.Model.ReasoningEffort, "max_attempts": defaults.Model.MaxAttempts,
			"context_window_tokens": defaults.Model.ContextWindowTokens,
		},
		"editor":     map[string]any{"mode": defaults.Editor.Mode, "max_height": defaults.Editor.MaxHeight, "command": defaults.Editor.Command},
		"appearance": map[string]any{"theme": defaults.Appearance.Theme},
		"history":    map[string]any{"max_entries": defaults.History.MaxEntries},
	}, "."), nil); err != nil {
		return Config{}, err
	}
	env := map[string]any{}
	for _, entry := range []struct{ name, key string }{
		{"UNREAL_HARNESS_LLM_PROVIDER", "provider"},
		{"UNREAL_HARNESS_LLM_MODEL", "id"},
		{"UNREAL_HARNESS_LLM_MAX_ATTEMPTS", "max_attempts"},
	} {
		if value := store.getenv(entry.name); value != "" {
			env[entry.key] = value
		}
	}
	if len(env) > 0 {
		if err := k.Load(confmap.Provider(map[string]any{"model": env}, "."), nil); err != nil {
			return Config{}, err
		}
	}
	if _, err := os.Stat(store.path); err == nil {
		if err := k.Load(file.Provider(store.path), toml.Parser()); err != nil {
			return Config{}, fmt.Errorf("load %s: %w", store.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("stat %s: %w", store.path, err)
	}
	var result Config
	if err := k.Unmarshal("", &result); err != nil {
		return Config{}, fmt.Errorf("decode %s: %w", store.path, err)
	}
	if result.Providers == nil {
		result.Providers = map[string]ProviderConfig{}
	}
	if err := validateConfig(result); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", store.path, err)
	}
	return result, nil
}

func validateConfig(value Config) error {
	switch value.Mode {
	case "default", "all", "edit", "plan":
	default:
		return fmt.Errorf("invalid mode %q", value.Mode)
	}
	providers := agentrunner.DefaultProviders()
	found := false
	for _, provider := range providers {
		if provider.Name == value.Model.Provider {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("unknown model provider %q", value.Model.Provider)
	}
	if value.Model.MaxAttempts < 1 {
		return errors.New("model.max_attempts must be positive")
	}
	if value.Model.ContextWindowTokens < 0 {
		return errors.New("model.context_window_tokens cannot be negative")
	}
	switch value.Model.ReasoningEffort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("invalid model.reasoning_effort %q", value.Model.ReasoningEffort)
	}
	if value.Editor.Mode != "plain" && value.Editor.Mode != "markdown" {
		return fmt.Errorf("invalid editor.mode %q", value.Editor.Mode)
	}
	if value.Editor.MaxHeight < 1 || value.Editor.MaxHeight > 100 {
		return errors.New("editor.max_height must be between 1 and 100")
	}
	if value.Appearance.Theme != "auto" && value.Appearance.Theme != "light" && value.Appearance.Theme != "dark" {
		return fmt.Errorf("invalid appearance.theme %q", value.Appearance.Theme)
	}
	if value.History.MaxEntries < 1 {
		return errors.New("history.max_entries must be positive")
	}
	for name, provider := range value.Providers {
		known := false
		for _, supported := range providers {
			if name == supported.Name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown configured provider %q", name)
		}
		if provider.BaseURL != "" {
			parsed, err := url.Parse(provider.BaseURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("providers.%s.base_url must be an absolute HTTP URL", name)
			}
		}
		for _, model := range provider.Models {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("providers.%s.models contains an empty ID", name)
			}
		}
		for modelID, limit := range provider.ContextWindows {
			if strings.TrimSpace(modelID) == "" || limit <= 0 {
				return fmt.Errorf("providers.%s.context_windows needs model IDs and positive token limits", name)
			}
		}
	}
	return nil
}

// SaveSetting edits one allowed file key. It rereads the file under an advisory
// lock, preserving unknown keys and never persisting environment values.
func (store *ConfigStore) SaveSetting(key string, value any) error {
	return store.SaveSettings(map[string]any{key: value})
}

// SaveSettings commits related settings together, including provider and model.
func (store *ConfigStore) SaveSettings(changes map[string]any) error {
	if len(changes) == 0 {
		return nil
	}
	for key := range changes {
		if !allowedSetting(key) {
			return fmt.Errorf("setting %q cannot be saved", key)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(store.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	data := map[string]any{}
	if source, err := os.ReadFile(store.path); err == nil {
		data, err = toml.Parser().Unmarshal(source)
		if err != nil {
			return fmt.Errorf("parse %s: %w", store.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for key, value := range changes {
		parts := strings.Split(key, ".")
		section := data
		for _, part := range parts[:len(parts)-1] {
			next, ok := section[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				section[part] = next
			}
			section = next
		}
		section[parts[len(parts)-1]] = value
	}
	encoded, err := toml.Parser().Marshal(data)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	// Validate against the file and environment before replacing the active value.
	oldPath := store.path
	store.path = temporary.Name()
	candidate, validationError := store.read()
	store.path = oldPath
	if validationError != nil {
		return validationError
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), store.path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	store.active = candidate
	return nil
}

func allowedSetting(key string) bool {
	switch key {
	case "mode", "system_prompt", "model.provider", "model.id", "model.reasoning_effort", "model.max_attempts", "model.context_window_tokens",
		"editor.mode", "editor.max_height", "editor.command", "appearance.theme", "history.max_entries":
		return true
	}
	parts := strings.Split(key, ".")
	if len(parts) == 3 && parts[0] == "providers" && (parts[2] == "base_url" || parts[2] == "models" || parts[2] == "context_windows") {
		for _, provider := range agentrunner.DefaultProviders() {
			if parts[1] == provider.Name {
				return true
			}
		}
	}
	return false
}
