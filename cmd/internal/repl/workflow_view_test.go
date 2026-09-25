package repl

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func workflowViewFixture(t *testing.T) *uiModel {
	t.Helper()
	config, err := LoadConfig(testEnv(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	return &uiModel{
		config: config, noColor: true, width: 120, height: 20,
		workflow: &workflowPanel{
			mode: "Execution", runID: "run-example", revision: 7,
			graph: workflow.Graph{Name: "Delivery", Steps: []workflow.Step{
				{ID: "review", Kind: "agent", Needs: []string{"implement"}},
				{ID: "implement", Kind: "agent"},
				{ID: "approve", Kind: "approval", Needs: []string{"review"}},
			}},
			state: workflow.State{
				"implement": {Status: "completed", Outcome: "passed", Output: map[string]any{"summary": "implemented"}},
				"review":    {Status: "running", Phase: "model", Inputs: map[string]any{"summary": "implemented"}},
			},
		},
	}
}

func TestWorkflowViewTopologicalSelectionAndDetails(t *testing.T) {
	model := workflowViewFixture(t)
	if order := workflowStepOrder(model.workflow.graph); !reflect.DeepEqual(order, []int{1, 0, 2}) {
		t.Fatalf("topological order = %v", order)
	}
	view := model.workflowView()
	if model.workflow.rows[3] != 1 || model.workflow.rows[4] != 0 {
		t.Fatalf("mouse mapping lost graph indexes: %v", model.workflow.rows)
	}
	if model.workflow.graphWidth >= model.width || !strings.Contains(view.Content, "Requires: implement") || !strings.Contains(view.Content, "INPUTS") {
		t.Fatalf("wide detail pane missing: %s", view.Content)
	}
	if !strings.Contains(view.Content, "Execution") || strings.Contains(view.Content, "SIMULATED") || strings.Contains(view.Content, "p/b") {
		t.Fatalf("wrong execution affordance: %s", view.Content)
	}
	if !view.AltScreen {
		t.Fatal("workflow view must own its terminal screen")
	}
}

func TestWorkflowViewFitsTinyAndUnicodeTerminals(t *testing.T) {
	model := workflowViewFixture(t)
	model.workflow.graph.Name = strings.Repeat("界", 90) + "\x1b]0;unsafe\a\nname"
	model.workflow.graph.Steps[0].ID = strings.Repeat("界👩‍💻", 30) + "\x1b[2J\nlabel"
	model.workflow.err = "bad\x1b[31m\tstatus\nmessage"
	for _, width := range []int{1, 2, 12, 50, 95, 96, 160} {
		for _, height := range []int{1, 2, 3, 4, 5, 12, 24} {
			model.width, model.height = width, height
			view := model.workflowView()
			lines := strings.Split(view.Content, "\n")
			if len(lines) > height {
				t.Fatalf("%dx%d produced %d rows", width, height, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width || strings.ContainsAny(line, "\x1b\t\r") {
					t.Fatalf("%dx%d unsafe or oversized row: %q", width, height, line)
				}
			}
			if strings.ContainsAny(view.WindowTitle, "\x1b\n\t\a") || ansi.StringWidth(view.WindowTitle) > 160 {
				t.Fatalf("unsafe title: %q", view.WindowTitle)
			}
			for row, index := range model.workflow.rows {
				if row >= height || index >= len(model.workflow.graph.Steps) {
					t.Fatalf("off-screen mouse target: %d=%d", row, index)
				}
			}
		}
	}
}

func TestWorkflowViewScrollKeepsSelectedStepVisible(t *testing.T) {
	model := workflowViewFixture(t)
	model.width, model.height = 60, 16
	model.workflow.graph.Steps = nil
	for index := range 30 {
		model.workflow.graph.Steps = append(model.workflow.graph.Steps, workflow.Step{ID: fmt.Sprintf("step-%02d", index), Kind: "agent"})
	}
	model.workflow.selected = 25
	model.workflow.detailTop = 10000
	view := model.workflowView()
	visible := false
	for _, index := range model.workflow.rows {
		visible = visible || index == 25
	}
	if !visible || !strings.Contains(view.Content, "step-25") || !strings.Contains(view.Content, "STEP DETAILS") {
		t.Fatalf("narrow selection not visible: %s", view.Content)
	}
	if model.workflow.top <= 0 || model.workflow.detailTop == 10000 {
		t.Fatal("scroll offsets were not bounded")
	}
}

func TestWorkflowViewDistinguishesApprovalAndNegativeCheck(t *testing.T) {
	state := workflow.State{"verify": {Status: "completed", Outcome: "failed"}}
	_, status := workflowNodeLabel(workflow.Step{ID: "verify", Kind: "repeat_check"}, state, false)
	if status != "completed · failed check" {
		t.Fatalf("negative check displayed as %q", status)
	}
	_, status = workflowNodeLabel(workflow.Step{ID: "approval", Kind: "approval"}, state, false)
	if status != "approval needed" {
		t.Fatalf("approval displayed as %q", status)
	}
	model := workflowViewFixture(t)
	model.workflow.loading = true
	model.workflow.stage = "Compiling Python"
	if view := model.workflowView(); !strings.Contains(view.Content, "Compiling Python") {
		t.Fatal("loading stage was not visible")
	}
}

func TestWorkflowViewShowsInterruptedDispatchAndWorkspace(t *testing.T) {
	model := workflowViewFixture(t)
	model.workflow.state["review"] = workflow.Node{Status: "running", DispatchStarted: true, Workspace: "/tmp/review-workspace"}
	view := model.workflowView()
	if !strings.Contains(view.Content, "reconciliation needed") || !strings.Contains(view.Content, "Workspace: /tmp/review-workspace") {
		t.Fatalf("interrupted status/workspace missing: %s", view.Content)
	}
	model.workflow.loading = true
	view = model.workflowView()
	if strings.Contains(view.Content, "reconciliation needed") || !strings.Contains(view.Content, "Status: running") {
		t.Fatal("live dispatched operation displayed as interrupted")
	}
	if !model.workflow.state["review"].DispatchStarted || model.workflow.state["review"].Status != "running" {
		t.Fatal("rendering changed durable execution state")
	}
}
