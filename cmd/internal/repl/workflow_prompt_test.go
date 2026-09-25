package repl

import (
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowSystemPromptAppendPreservesConfiguredInstructions(t *testing.T) {
	base := cliSystemPrompt("/workspace", "Repository instructions", "Configured system prompt")
	got := cliSystemPrompt("/workspace", "Repository instructions", "Configured system prompt", "Workflow rules", "", "More workflow rules")
	if got != base+"\n\nWorkflow rules\n\nMore workflow rules" {
		t.Fatalf("unexpected assembled prompt: %q", got)
	}
	if !strings.Contains(got, "Repository instructions") || !strings.HasPrefix(got, "Configured system prompt") {
		t.Fatal("addition replaced original instructions")
	}
}

func TestWorkflowRuntimeFactoryAppendsBeforePlanRestrictions(t *testing.T) {
	model := workflowThreadFixture(t)
	if err := os.WriteFile(filepath.Join(model.state.Workspace, "AGENTS.md"), []byte("Repository guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := model.config.SaveSettings(map[string]any{"system_prompt": "Configured base", "mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	router := &ModelRouter{config: model.config, getenv: model.getenv}
	runtime, err := newAppRuntimeWithFormat(t.Context(), model.state, router, model.getenv, 0, nil, "Workflow extra")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	builder, _, err := runtime.options.NewBuilder("review")
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	prompt := result.Request.Input[0].Data.(llm.Message).Text
	prior := -1
	for _, part := range []string{"Configured base", "Workspace:", "Repository guidance", "Workflow extra", "Plan mode is read-only"} {
		index := strings.Index(prompt, part)
		if index <= prior {
			t.Fatalf("missing or reordered %q: %s", part, prompt)
		}
		prior = index
	}
}
