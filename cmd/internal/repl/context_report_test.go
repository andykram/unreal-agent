package repl

import (
	"context"
	"strings"
	"testing"

	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestContextCapacityUsesSelectedProviderAndModel(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	model := newUI(context.Background(), getenv, 0, state, config, nil, catalog, nil, nil)
	if got, source := model.contextCapacity(); got != 1_050_000 || source != "OpenAI model guide" {
		t.Fatalf("OpenAI default = %d, %q", got, source)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "openai-codex", "model.id": "gpt-6-sol"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := model.contextCapacity(); got != 0 {
		t.Fatalf("subscription model reused API limit: %d", got)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "openai", "model.id": "gpt-6-astra"}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("model.context_window_tokens", 128000); err != nil {
		t.Fatal(err)
	}
	if got, source := model.contextCapacity(); got != 128000 || source != "configured" {
		t.Fatalf("override = %d, %q", got, source)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "openrouter", "model.id": "another/family", "model.context_window_tokens": 0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := model.contextCapacity(); got != 0 {
		t.Fatalf("unknown OpenRouter model = %d", got)
	}
	generation := catalog.BeginRefresh("openrouter")
	if !catalog.Apply(CatalogResult{Provider: "openrouter", Generation: generation, Choices: []ModelChoice{{Ref: ModelRef{Provider: "openrouter", ID: "another/family"}, ContextWindowTokens: 32000}}}) {
		t.Fatal("catalog result rejected")
	}
	if got, source := model.contextCapacity(); got != 32000 || source != "provider catalog" {
		t.Fatalf("OpenRouter catalog = %d, %q", got, source)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "fireworks", "model.id": "accounts/fireworks/models/example"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := model.contextCapacity(); got != 0 {
		t.Fatalf("other model family reused limit: %d", got)
	}
	if err := config.SaveSetting("providers.fireworks.context_windows", map[string]int{"accounts/fireworks/models/example": 65536}); err != nil {
		t.Fatal(err)
	}
	if got, source := model.contextCapacity(); got != 65536 || source != "configured model" {
		t.Fatalf("Fireworks model override = %d, %q", got, source)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "ollama", "model.id": "local"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := model.contextCapacity(); got != 0 {
		t.Fatalf("Ollama reused Fireworks limit: %d", got)
	}
}

func TestContextReportSeparatesProviderUsageFromStoredText(t *testing.T) {
	report := contextReport{capacity: 100, parts: []contextPart{{label: "Core prompt", tokens: 20}, {label: "Instructions"}, {label: "Skills index"}, {label: "Tool definitions"}, {label: "Conversation"}, {label: "Tool activity"}}}
	operations := map[operation.ID]operation.Operation{}
	errors := map[string]string{}
	input, err := json.Marshal("hello")
	if err != nil {
		t.Fatal(err)
	}
	report.addItem(sessionstore.Item{Kind: sessionstore.ItemInput, Data: inbox.Input{ID: "user", Kind: inbox.InputExternal, Payload: input}}, operations, errors)
	report.addItem(sessionstore.Item{Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{Response: llm.Response{
		Usage: llm.Usage{InputTokens: 42, OutputTokens: 7, CachedInputTokens: 8, ReasoningTokens: 2},
		Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "answer"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{Name: "Bash", Arguments: `{"command":"true"}`}},
			{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"thought"}, Raw: jsontext.Value(`{"opaque":true}`)}},
		},
	}}}, operations, errors)
	report.addItem(sessionstore.Item{Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "call", Status: tool.CallStatus{Error: "oops"}}}, operations, errors)
	for _, message := range errors {
		report.parts[5].tokens += estimatedTokens(message)
	}
	if report.requests != 1 || report.inputTokens != 42 || report.outputTokens != 7 || report.cachedTokens != 8 || report.reasoningTokens != 2 || !report.usageReported {
		t.Fatalf("provider usage = %#v", report)
	}
	if report.parts[4].tokens != estimatedTokens("hello")+estimatedTokens("answer")+estimatedTokens("thought") || report.parts[5].tokens <= estimatedTokens("oops") || report.opaqueItems != 1 {
		t.Fatalf("stored text breakdown = %#v", report)
	}
	view := report.view(60, 20, true)
	if !strings.Contains(view, "42 input tokens") || !strings.Contains(view, "ESTIMATED CONTEXT TOKENS") || !strings.Contains(view, "Tool activity") || !strings.Contains(view, "opaque reasoning") || !strings.Contains(view, "% used") {
		t.Fatalf("context view = %q", view)
	}
	narrow := report.view(24, 14, true)
	if !strings.Contains(narrow, "Input 42") || !strings.Contains(narrow, "Output 7") {
		t.Fatalf("narrow usage = %q", narrow)
	}
	if rows := strings.Count(report.view(24, 8, true), "\n") + 1; rows > 8 {
		t.Fatalf("narrow context view has %d rows", rows)
	}
}
