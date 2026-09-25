package repl

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func recoveryViewFixture(t *testing.T) *uiModel {
	model := workflowViewFixture(t)
	model.workflow.recovery = &workflowRecovery{stepID: "review", stage: "inspect"}
	model.workflow.state["review"] = workflow.Node{Status: "running", DispatchStarted: true, Phase: "check", AttemptKey: "attempt-123", ExternalKey: "external-456", Workspace: "/tmp/worktree", Inputs: map[string]any{"task": "review"}, History: []string{"Dispatch persisted"}}
	return model
}

func TestWorkflowRecoveryViewEvidenceAndActions(t *testing.T) {
	model := recoveryViewFixture(t)
	before := model.workflow.state["review"]
	view := model.workflowRecoveryView()
	for _, text := range []string{"Recover interrupted step", "execution paused", "attempt-123", "external-456", "/tmp/worktree", "Check saved evidence", "Record verified result", "Mark failed", "Retry this attempt"} {
		if !strings.Contains(view.Content, text) {
			t.Fatalf("missing %q: %s", text, view.Content)
		}
	}
	if len(model.workflow.recovery.rows) != 4 {
		t.Fatalf("action rows: %v", model.workflow.recovery.rows)
	}
	if !reflect.DeepEqual(before, model.workflow.state["review"]) {
		t.Fatal("rendering mutated workflow state")
	}
	if !view.AltScreen {
		t.Fatal("recovery must own terminal screen")
	}
}

func TestWorkflowRecoveryViewGeometryAndControls(t *testing.T) {
	model := recoveryViewFixture(t)
	recovery := model.workflow.recovery
	recovery.stepID = "review\x1b]0;unsafe\a\n界"
	recovery.evidence = strings.Repeat("界👩‍💻", 40) + "\x1b[2J\n" + strings.Repeat("evidence\n", 30)
	recovery.err = "bad\x1b[31m\tstatus\nmessage"
	_ = recovery.result.Restore("{\n\"output\": \"界\"\n}", 0)
	_ = recovery.note.Restore("reason\x1b[2J\nverified", 0)
	for _, stage := range []string{"inspect", "edit", "confirm"} {
		recovery.stage = stage
		for _, action := range []int{1, 2, 3} {
			recovery.action = action
			for _, width := range []int{1, 2, 20, 60, 99, 100, 160} {
				for _, height := range []int{1, 2, 3, 5, 10, 24} {
					model.width, model.height = width, height
					view := model.workflowRecoveryView()
					lines := strings.Split(view.Content, "\n")
					if len(lines) > height {
						t.Fatalf("%s %dx%d rows=%d", stage, width, height, len(lines))
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > width || strings.ContainsAny(line, "\x1b\t\r\a") {
							t.Fatalf("%s %dx%d unsafe row %q", stage, width, height, line)
						}
					}
					if view.Cursor != nil && (view.Cursor.X < 0 || view.Cursor.X >= width || view.Cursor.Y < 0 || view.Cursor.Y >= height) {
						t.Fatalf("cursor outside %dx%d: %+v", width, height, view.Cursor)
					}
					for row, index := range recovery.rows {
						if row < 0 || row >= height || index < 0 || index > 3 {
							t.Fatalf("bad action hit %d=%d", row, index)
						}
					}
				}
			}
		}
	}
}

func TestWorkflowRecoveryViewEditingAndRetryConfirmation(t *testing.T) {
	model := recoveryViewFixture(t)
	recovery := model.workflow.recovery
	recovery.stage, recovery.action = "edit", 1
	_ = recovery.result.Restore("{\n  \"output\": \"verified\"\n}", 0)
	_ = recovery.note.Restore("Inspected saved output", 0)
	view := model.workflowRecoveryView()
	if view.Cursor == nil || !strings.Contains(view.Content, "RESULT JSON") || !strings.Contains(view.Content, "REASON") || len(recovery.rows) != 0 {
		t.Fatalf("editing affordances missing: %s", view.Content)
	}
	recovery.stage, recovery.action = "confirm", 3
	_ = recovery.confirm.Restore("retry review", 12)
	view = model.workflowRecoveryView()
	if view.Cursor == nil || !strings.Contains(view.Content, "Type retry review") || !strings.Contains(view.Content, "may already have taken effect") || !strings.Contains(view.Content, "Inspected saved output") {
		t.Fatalf("retry safeguards missing: %s", view.Content)
	}
}
