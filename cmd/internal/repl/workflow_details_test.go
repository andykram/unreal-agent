package repl

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWorkflowDetailsHumanBriefAndRawToggle(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.graph.Steps[0].Spec = map[string]any{
		"prompt":        "Review the payment boundary.\nExplain any duplicate-charge risk.",
		"output_schema": map[string]any{"type": "object", "properties": map[string]any{"safe": map[string]any{"type": "boolean"}, "findings": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []any{"safe"}},
	}
	node := panel.state["review"]
	node.Output = map[string]any{"summary": "No duplicate charge", "safe": true}
	panel.state["review"] = node
	human := strings.Join(model.workflowDetails(100), "\n")
	for _, text := range []string{"PROMPT", "Review the payment boundary.", "Requires: implement", "OUTPUT", "summary: No duplicate charge", "safe · boolean · required", "findings · list of string · optional"} {
		if !strings.Contains(human, text) {
			t.Fatalf("human brief missing %q: %s", text, human)
		}
	}
	if strings.Contains(human, "RECOVERY") || strings.Contains(human, `"properties"`) || strings.Contains(human, "DEFINITION") {
		t.Fatal("human view leaked raw schema or empty recovery")
	}
	panel.rawDetails = true
	raw := strings.Join(model.workflowDetails(100), "\n")
	for _, text := range []string{"RAW JSON", "DEFINITION", `"output_schema"`, `"properties"`, `"summary": "No duplicate charge"`} {
		if !strings.Contains(raw, text) {
			t.Fatalf("raw detail missing %q: %s", text, raw)
		}
	}
	if strings.Contains(raw, "RECOVERY DECISIONS") {
		t.Fatal("raw view displays null recovery")
	}
}

func TestWorkflowDetailsColoredGeometryAndSanitization(t *testing.T) {
	model := workflowViewFixture(t)
	model.noColor = false
	model.workflow.graph.Steps[0].Spec = map[string]any{"prompt": "界👩‍💻\x1b]0;injected\a\nlong\ttext", "output_schema": map[string]any{"type": "boolean"}}
	for _, raw := range []bool{false, true} {
		model.workflow.rawDetails = raw
		for _, width := range []int{1, 2, 8, 30, 80} {
			details := model.workflowDetails(width)
			styled := false
			for _, line := range details {
				if strings.Contains(line, "\x1b[") {
					styled = true
				}
				plain := ansi.Strip(line)
				if ansi.StringWidth(line) > width || strings.ContainsAny(plain, "\x1b\a\t\r") || strings.Contains(line, "\x1b]0;") {
					t.Fatalf("raw=%v width=%d unsafe row %q", raw, width, line)
				}
			}
			if width >= 30 && !styled {
				t.Fatal("color enabled but no styled sections")
			}
		}
	}
}

func TestWorkflowViewReservesSidebarWithoutChangingTerminalWidth(t *testing.T) {
	model := workflowViewFixture(t)
	model.width = 160
	before := model.width
	view := model.workflowView()
	if model.width != before {
		t.Fatal("workflow rendering changed terminal width")
	}
	if !strings.Contains(view.Content, "UNREAL") {
		t.Fatal("workflow graph lost session rail")
	}
	if model.workflow.graphWidth >= model.width-model.sidebarSize() {
		t.Fatal("wide detail pane did not reserve rail")
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("rail overflow: %q", line)
		}
	}
}

func TestWorkflowFullPromptShowsCapturedMessagesWithoutPreviewTruncation(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.fullPrompt = true
	node := panel.state["review"]
	node.ExternalKey = "current-key"
	panel.state["review"] = node
	panel.prompts = map[string]workflowPromptSnapshot{"review": {ExternalKey: "current-key", Phase: node.Phase, System: strings.Repeat("long system instructions\n", 1000) + "SYSTEM_END", User: "resolved inputs and user prompt\nUSER_END"}}
	full := strings.Join(model.workflowDetails(80), "\n")
	for _, text := range []string{"FULL PROMPT", "Captured for this attempt", "SYSTEM MESSAGE", "USER MESSAGE", "SYSTEM_END", "USER_END"} {
		if !strings.Contains(full, text) {
			t.Fatalf("full prompt missing %q", text)
		}
	}
	if strings.Contains(full, "preview truncated") {
		t.Fatal("captured prompt was truncated")
	}
	panel.rawDetails = true
	raw := strings.Join(model.workflowDetails(80), "\n")
	if !strings.Contains(raw, "RAW JSON") || strings.Contains(raw, "SYSTEM_END") {
		t.Fatal("raw toggle did not retain its definition view")
	}
}

func TestWorkflowFullPromptFallbackAndAuthoredAugmentation(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.graph.Steps[0].Spec = map[string]any{"prompt": "Review carefully", "system_prompt_append": "Use service conventions", "repair_system_prompt_append": "Keep repair focused"}
	human := strings.Join(model.workflowDetails(100), "\n")
	for _, text := range []string{"SYSTEM PROMPT ADDITION", "Use service conventions", "REPAIR SYSTEM PROMPT ADDITION", "Keep repair focused"} {
		if !strings.Contains(human, text) {
			t.Fatalf("authored addition missing %q", text)
		}
	}
	panel.fullPrompt = true
	full := strings.Join(model.workflowDetails(100), "\n")
	if !strings.Contains(full, "Full prompt becomes available when this agent starts") || !strings.Contains(full, "AUTHORED PROMPT") || !strings.Contains(full, "Use service conventions") || strings.Contains(full, "Captured for this attempt") {
		t.Fatalf("misleading uncaptured prompt: %s", full)
	}
	panel.prompts = map[string]workflowPromptSnapshot{"review": {System: "safe\x1b]0;bad\a界", User: "safe\x1b[2J"}}
	for _, width := range []int{1, 2, 20, 80} {
		for _, line := range model.workflowDetails(width) {
			if ansi.StringWidth(line) > width || strings.ContainsAny(line, "\x1b\a\t") {
				t.Fatalf("unsafe full prompt row %q", line)
			}
		}
	}
}

func TestWorkflowPromptCaptureLabelsAttemptAndLegacyHonestly(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.fullPrompt = true
	node := panel.state["review"]
	node.ExternalKey = "repair-2"
	node.Phase = "repair"
	panel.state["review"] = node
	for _, test := range []struct {
		name, key, phase string
		current          bool
	}{
		{"current", "repair-2", "repair", true},
		{"previous attempt", "repair-1", "repair", false},
		{"previous phase", "repair-2", "check", false},
		{"legacy", "", "repair", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			panel.prompts = map[string]workflowPromptSnapshot{"review": {ExternalKey: test.key, Phase: test.phase, System: "retained system", User: "retained user"}}
			content := strings.Join(model.workflowDetails(100), "\n")
			if strings.Contains(content, "Captured for this attempt") != test.current {
				t.Fatalf("incorrect capture attribution: %s", content)
			}
			if !test.current && !strings.Contains(content, "Last captured prompt") {
				t.Fatal("old prompt was not labeled")
			}
			if !strings.Contains(content, "retained system") || !strings.Contains(content, "retained user") {
				t.Fatal("historical evidence was discarded")
			}
		})
	}
}
