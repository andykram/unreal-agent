package workflow

import (
	"strings"
	"testing"
)

func TestWorkspaceMustBeAWorktreeDependency(t *testing.T) {
	graph := Graph{Version: 1, Name: "workspace", Steps: []Step{
		{ID: "workspace", Kind: "worktree"},
		{ID: "plan", Kind: "approval"},
		{ID: "execute", Kind: "command", Needs: []string{"plan"}, Spec: map[string]any{"workspace": "workspace"}},
	}}
	if err := Validate(graph); err == nil || !strings.Contains(err.Error(), "must be a dependency") {
		t.Fatalf("accepted early execution: %v", err)
	}
	graph.Steps[2].Needs = append(graph.Steps[2].Needs, "workspace")
	if err := Validate(graph); err != nil {
		t.Fatalf("rejected workspace dependency: %v", err)
	}
}

func TestWorkspaceMustBeANonemptyStepID(t *testing.T) {
	for _, workspace := range []any{nil, 123, ""} {
		graph := Graph{Version: 1, Name: "workspace", Steps: []Step{
			{ID: "workspace", Kind: "worktree"},
			{ID: "execute", Kind: "command", Needs: []string{"workspace"}, Spec: map[string]any{"workspace": workspace}},
		}}
		if err := Validate(graph); err == nil || !strings.Contains(err.Error(), "workspace must be a nonempty worktree step ID") {
			t.Fatalf("accepted workspace %#v: %v", workspace, err)
		}
	}
}

func TestStepIDsMustBeUsableInViewerCommands(t *testing.T) {
	for _, id := range []string{"security review", "security\treview", "security\u2003review"} {
		graph := Graph{Version: 1, Name: "IDs", Steps: []Step{{ID: id, Kind: "approval"}}}
		if err := Validate(graph); err == nil || !strings.Contains(err.Error(), "whitespace-containing") {
			t.Fatalf("accepted unaddressable ID %q: %v", id, err)
		}
	}
	graph := Graph{Version: 1, Name: "IDs", Steps: []Step{{ID: "security-review", Kind: "approval"}}}
	if err := Validate(graph); err != nil {
		t.Fatalf("rejected viewer-addressable ID: %v", err)
	}
}
