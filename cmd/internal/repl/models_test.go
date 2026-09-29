package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type controlledModelCall struct {
	request  llm.Request
	response chan llm.Response
}

type controlledModelClient struct{ calls chan controlledModelCall }

func nextControlledCall(t *testing.T, client *controlledModelClient) controlledModelCall {
	t.Helper()
	select {
	case call := <-client.calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
		return controlledModelCall{}
	}
}

func (client *controlledModelClient) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	call := controlledModelCall{request: request, response: make(chan llm.Response, 1)}
	client.calls <- call
	select {
	case response := <-call.response:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}
func (*controlledModelClient) Close() error { return nil }

func TestModelSwitchAppliesToToolFollowupWithoutReplacingActiveRequest(t *testing.T) {
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" || key == "OPENROUTER_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	first := &controlledModelClient{calls: make(chan controlledModelCall, 1)}
	second := &controlledModelClient{calls: make(chan controlledModelCall, 1)}
	router, err := newModelRouter(config, getenv, []agentrunner.Provider{
		{Name: "openai", DefaultModel: "first", APIKeyEnvironment: "OPENAI_API_KEY", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) { return first, nil }},
		{Name: "openrouter", APIKeyEnvironment: "OPENROUTER_API_KEY", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) { return second, nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	runtime, _, _, _ := newRuntimeTestHost(t)
	manager := &heldOperationManager{adds: make(chan operation.Operation, 1), updates: make(chan operation.Operation, 1)}
	runtime.options.LLM = router
	runtime.options.Operations = manager
	runtime.options.Tools = tool.NewRegistry(tool.StaticTranslators{ViewImage: heldToolTranslator{}}, tool.ViewImageName)
	if _, _, err := runtime.Submit(t.Context(), "inspect", false); err != nil {
		t.Fatal(err)
	}
	active := nextControlledCall(t, first)
	if active.request.Model.ID != "first" {
		t.Fatalf("active model = %q", active.request.Model.ID)
	}
	if err := router.Select("openrouter", "second"); err != nil {
		t.Fatal(err)
	}
	if choices := router.ActiveSelections(); len(choices) != 1 || choices[0].ID != "first" {
		t.Fatalf("active selection changed: %#v", choices)
	}
	active.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "held", Name: tool.ViewImageName, Arguments: `{}`}}}}
	var held operation.Operation
	select {
	case held = <-manager.adds:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	held.Status = operation.StatusCompleted
	manager.updates <- held
	followup := nextControlledCall(t, second)
	if followup.request.Model.ID != "second" {
		t.Fatalf("tool follow-up model = %q", followup.request.Model.ID)
	}
	followup.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	waitRuntimeTasks(t, runtime, 1)
}

type routerCall struct {
	model llm.Model
	done  chan struct{}
}

type routerClient struct {
	calls  chan routerCall
	mu     sync.Mutex
	closed bool
}

func (client *routerClient) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	call := routerCall{model: request.Model, done: make(chan struct{})}
	client.calls <- call
	<-call.done
	return llm.Response{}, nil
}

func (client *routerClient) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.closed = true
	return nil
}

func TestModelRouterSwitchAffectsNextRequest(t *testing.T) {
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" || key == "OPENROUTER_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	first := &routerClient{calls: make(chan routerCall, 1)}
	second := &routerClient{calls: make(chan routerCall, 1)}
	providers := []agentrunner.Provider{
		{Name: "openai", DefaultModel: "first-default", APIKeyEnvironment: "OPENAI_API_KEY", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) { return first, nil }},
		{Name: "openrouter", APIKeyEnvironment: "OPENROUTER_API_KEY", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) { return second, nil }},
	}
	router, err := newModelRouter(config, getenv, providers)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { _, err := router.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); results <- err }()
	oldCall := <-first.calls
	if oldCall.model.ID != "first-default" {
		t.Fatalf("first model = %q", oldCall.model.ID)
	}
	if err := router.Select("openrouter", "vendor/second"); err != nil {
		t.Fatal(err)
	}
	if active := router.ActiveSelections(); len(active) != 1 || active[0].Provider != "openai" {
		t.Fatalf("active request after selection = %#v", active)
	}
	go func() { _, err := router.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); results <- err }()
	newCall := <-second.calls
	if newCall.model.ID != "vendor/second" {
		t.Fatalf("next model = %q", newCall.model.ID)
	}
	first.mu.Lock()
	closedDuringRequest := first.closed
	first.mu.Unlock()
	if closedDuringRequest {
		t.Fatal("old client closed during its request")
	}
	close(oldCall.done)
	close(newCall.done)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(getenv)
	if err != nil || reloaded.Current().Model.Provider != "openrouter" || reloaded.Current().Model.ID != "vendor/second" {
		t.Fatalf("persisted selection = %#v, %v", reloaded, err)
	}
}

func TestModelRouterSaveFailureKeepsSelection(t *testing.T) {
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	created := []*routerClient{}
	provider := agentrunner.Provider{Name: "openai", DefaultModel: "default", APIKeyEnvironment: "OPENAI_API_KEY", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) {
		client := &routerClient{calls: make(chan routerCall, 1)}
		created = append(created, client)
		return client, nil
	}}
	router, err := newModelRouter(config, getenv, []agentrunner.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(home, "block")
	if err := os.WriteFile(block, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	config.path = filepath.Join(block, "config.toml")
	if err := router.Select("openai", "new-model"); err == nil {
		t.Fatal("selection succeeded despite config save failure")
	}
	if router.Selected().ID != "default" || len(created) != 2 {
		t.Fatalf("selection after failure = %#v, clients = %d", router.Selected(), len(created))
	}
	created[1].mu.Lock()
	closed := created[1].closed
	created[1].mu.Unlock()
	if !closed {
		t.Fatal("prepared client was not closed after save failure")
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReloadConfigReindexesSkillsAndPreservesDiskAndActiveRequest(t *testing.T) {
	root := t.TempDir()
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	client := &controlledModelClient{calls: make(chan controlledModelCall, 2)}
	router, err := newModelRouter(config, getenv, []agentrunner.Provider{{Name: "openai", DefaultModel: "first", NewClient: func(string, string, int, func(string) string) (agentrunner.Client, error) { return client, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := newUI(t.Context(), getenv, 0, state, config, router, NewModelCatalog(config, getenv), nil, nil)
	done := make(chan error, 1)
	go func() { _, err := router.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); done <- err }()
	active := nextControlledCall(t, client)
	candidate := "# retain this comment\n[model]\nprovider = 'openai'\nid = 'second'\nreasoning_effort = 'low'\n[appearance]\ntheme = 'light'\n"
	writeInstructionFixture(t, config.Path(), candidate)
	skillPath := filepath.Join(root, ".agents", "skills", "new", "SKILL.md")
	writeInstructionFixture(t, skillPath, "---\nname: new\ndescription: Added after launch\n---\n")
	model.runReloadConfig("", "")
	if config.Current().Appearance.Theme != "light" || router.Selected().ID != "second" || len(model.skills.Entries) != 1 {
		t.Fatalf("reload: %s, %#v", model.message, config.Current())
	}
	data, err := os.ReadFile(config.Path())
	if err != nil || string(data) != candidate {
		t.Fatal("reload rewrote disk config")
	}
	if active.request.Model.ID != "first" {
		t.Fatal("active request changed")
	}
	active.response <- llm.Response{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { _, err := router.Respond(t.Context(), llm.Request{}, llm.RequestOptions{}); done <- err }()
	next := nextControlledCall(t, client)
	next.response <- llm.Response{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if next.request.Model.ID != "second" || string(next.request.Model.ReasoningEffort) != "low" {
		t.Fatal("new settings not applied to next request")
	}
	// A malformed skill must not partly install a valid config change.
	writeInstructionFixture(t, config.Path(), "[appearance]\ntheme = 'dark'\n")
	writeInstructionFixture(t, skillPath, "not a skill")
	model.runReloadConfig("", "")
	if config.Current().Appearance.Theme != "light" || len(model.skills.Entries) != 1 || !strings.Contains(model.message, "Reload failed") {
		t.Fatal("failed reload changed active state")
	}
	writeInstructionFixture(t, config.Path(), "invalid toml [")
	model.runReloadConfig("", "")
	if router.Selected().ID != "second" || !strings.Contains(model.message, "Reload failed") {
		t.Fatal("invalid config changed router")
	}
}
