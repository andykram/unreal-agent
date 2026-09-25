package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(home, xdg string) func(string) string {
	return func(key string) string {
		switch key {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return xdg
		}
		return ""
	}
}

func TestConfigPathUsesXDGFallback(t *testing.T) {
	home := t.TempDir()
	path, err := ConfigPath(testEnv(home, ""))
	if err != nil || path != filepath.Join(home, ".config", "unreal-agent-repl", "config.toml") {
		t.Fatalf("fallback path = %q, %v", path, err)
	}
	path, err = ConfigPath(testEnv(home, filepath.Join(home, "xdg")))
	if err != nil || path != filepath.Join(home, "xdg", "unreal-agent-repl", "config.toml") {
		t.Fatalf("XDG path = %q, %v", path, err)
	}
	if _, err := ConfigPath(testEnv(home, "relative")); err == nil {
		t.Fatal("relative XDG path was accepted")
	}
}

func TestContextWindowSetting(t *testing.T) {
	store, err := LoadConfig(testEnv(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSetting("model.context_window_tokens", 128000); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().Model.ContextWindowTokens; got != 128000 {
		t.Fatalf("context window = %d", got)
	}
	if err := store.SaveSetting("model.context_window_tokens", -1); err == nil {
		t.Fatal("negative context window accepted")
	}
}

func TestConfigSavePreservesUnknownKeysAndActivation(t *testing.T) {
	home := t.TempDir()
	getenv := testEnv(home, "")
	path, _ := ConfigPath(getenv)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[model]\nprovider = 'openai'\n[future]\nvalue = 'keep'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSetting("editor.mode", "markdown"); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().Editor.Mode; got != "markdown" {
		t.Fatalf("active editor mode = %q", got)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "[future]") || !strings.Contains(string(saved), "keep") {
		t.Fatalf("unknown key lost: %s", saved)
	}
	if err := store.SaveSetting("editor.max_height", 0); err == nil {
		t.Fatal("invalid height was saved")
	}
	if store.Current().Editor.MaxHeight != 10 {
		t.Fatal("failed save changed active settings")
	}
	if err := store.SaveSetting("model.api_key", "secret"); err == nil {
		t.Fatal("secret key was accepted")
	}
	reloaded, err := LoadConfig(getenv)
	if err != nil || reloaded.Current().Editor.Mode != "markdown" {
		t.Fatalf("reload after save = %#v, %v", reloaded, err)
	}
}

func TestConfigFileOverridesEnvironmentModel(t *testing.T) {
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "UNREAL_HARNESS_LLM_MODEL" {
			return "environment-model"
		}
		return testEnv(home, "")(key)
	}
	path, _ := ConfigPath(getenv)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[model]\nid = 'file-model'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Current().Model.ID; got != "file-model" {
		t.Fatalf("model = %q, want file-model", got)
	}
}

func TestConcurrentConfigSavesMergeKeysWithoutPersistingEnvironment(t *testing.T) {
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "secret-only-in-environment"
		}
		if key == "UNREAL_HARNESS_LLM_MODEL" {
			return "environment-default"
		}
		return testEnv(home, "")(key)
	}
	first, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- first.SaveSetting("editor.mode", "markdown") }()
	go func() { results <- second.SaveSetting("history.max_entries", 42) }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if current := reloaded.Current(); current.Editor.Mode != "markdown" || current.History.MaxEntries != 42 {
		t.Fatalf("concurrent edits did not merge: %#v", current)
	}
	encoded, err := os.ReadFile(first.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-only-in-environment") || strings.Contains(string(encoded), "environment-default") {
		t.Fatal("environment values leaked into config file")
	}
}

func TestSystemPromptOverridePreservesExplicitEmpty(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config.Current().SystemPrompt, "TaskWrite") {
		t.Fatal("default prompt not embedded")
	}
	for _, prompt := range []string{"Custom instructions.\nUse the project conventions.", ""} {
		if err := config.SaveSetting("system_prompt", prompt); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadConfig(getenv)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Current().SystemPrompt != prompt {
			t.Fatal("override changed on reload")
		}
		built := cliSystemPrompt("/tmp/project", "Project guidance", reloaded.Current().SystemPrompt)
		if !strings.Contains(built, "Workspace: /tmp/project") || !strings.Contains(built, "Project guidance") || strings.Contains(built, "TaskWrite") {
			t.Fatalf("override = %q", built)
		}
	}
}

func TestHarnessModesPersistAndRejectInvalid(t *testing.T) {
	store, err := LoadConfig(testEnv(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if store.Current().Mode != "default" {
		t.Fatal("existing behavior must remain default")
	}
	for _, mode := range []string{"all", "edit", "plan"} {
		if err := store.SaveSetting("mode", mode); err != nil {
			t.Fatal(err)
		}
		if store.Current().Mode != mode {
			t.Fatalf("mode = %q", store.Current().Mode)
		}
	}
	if err := store.SaveSetting("mode", "unsafe"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	reloaded, err := LoadConfig(testEnv(filepath.Dir(filepath.Dir(filepath.Dir(store.Path()))), ""))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Current().Mode != "plan" {
		t.Fatalf("reloaded mode = %q", reloaded.Current().Mode)
	}
}
