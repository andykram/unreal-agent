package repl

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowResultCannotRegressFromQueuedDispatchNotice(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.loading = true
	panel.executor = &replWorkflowExecutor{notices: make(chan workflowAgentNotice, 2)}
	closedAgent := &Runtime{}
	panel.agent = closedAgent
	panel.executor.notices <- workflowAgentNotice{
		runtime: closedAgent,
		state: workflow.State{
			"implement": {Status: "running", DispatchStarted: true},
		},
	}
	completed := workflow.State{"implement": {Status: "completed", Outcome: "passed"}}
	model.acceptWorkflowResult(workflowResultMsg{
		panel: panel, graph: panel.graph, state: completed, runID: panel.runID, revision: 8,
	})
	model.advanceWorkflowTick(workflowTickMsg{panel: panel})
	if panel.state["implement"].Status != "completed" || panel.revision != 8 {
		t.Fatalf("queued dispatch notice replaced completed checkpoint: revision %d, state %#v", panel.revision, panel.state)
	}
	if panel.agent != nil {
		t.Fatal("finished workflow action retained its closed agent interaction target")
	}
}

func TestWorkflowStalePanelMessagesDoNotChangeCurrentPanel(t *testing.T) {
	model := workflowViewFixture(t)
	current := model.workflow
	old := &workflowPanel{}
	model.acceptWorkflowResult(workflowResultMsg{panel: old, runID: "old", revision: 100, state: workflow.State{}})
	model.advanceWorkflowTick(workflowTickMsg{panel: old})
	if model.workflow != current || current.runID != "run-example" || current.revision != 7 {
		t.Fatal("stale panel event changed current workflow")
	}
}

func TestWorkflowArrowNavigationFollowsDisplayedDependencies(t *testing.T) {
	model := workflowViewFixture(t)
	model.workflow.visible = true
	model.workflow.selected = 1 // implement is the first displayed node.
	model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.workflow.selected != 0 { // review follows implement in display order.
		t.Fatalf("down selected source index %d, want review index 0", model.workflow.selected)
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if model.workflow.selected != 1 {
		t.Fatalf("up selected source index %d, want implement index 1", model.workflow.selected)
	}
}

func TestWorkflowCtrlDExitsDuringAgentApproval(t *testing.T) {
	model := workflowViewFixture(t)
	request := &approvalRequest{answer: make(chan bool, 1)}
	gate := &approvalGate{pending: []*approvalRequest{request}}
	model.workflow.agent = &Runtime{options: RuntimeOptions{Approvals: gate}}
	_, command := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("pending agent approval swallowed Ctrl+D")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+D did not exit")
	}
	if gate.Pending() != request {
		t.Fatal("exit input silently approved or denied a tool call")
	}
}

func TestWorkflowIgnoredInputStillDisarmsConsecutiveQuit(t *testing.T) {
	for _, message := range []tea.Msg{
		tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft},
		tea.MouseWheelMsg{Button: tea.MouseWheelDown},
		tea.PasteMsg{Content: "ignored by graph"},
	} {
		model := workflowViewFixture(t)
		model.workflow.visible = true
		model.quitArmed = true
		model.Update(message)
		if model.quitArmed {
			t.Fatalf("%T did not disarm consecutive Ctrl+C exit", message)
		}
	}
}

func TestWorkflowReopenCannotDispatchAlongsideMainTask(t *testing.T) {
	model := workflowViewFixture(t)
	model.workflow.auto = true
	model.busy = true
	if command := model.workflowAction("next", ""); command != nil {
		t.Fatal("paused workflow dispatched alongside the main task")
	}
	if model.workflow.auto || model.workflow.loading || !strings.Contains(model.workflow.err, "current agent task") {
		t.Fatal("main-task admission did not pause with an actionable error")
	}
}

func TestWorkflowRestoredDispatchRequiresVisibleReconciliation(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.loading, panel.auto = true, true
	restored := workflow.State{"implement": {Status: "running", DispatchStarted: true}}
	model.acceptWorkflowResult(workflowResultMsg{panel: panel, graph: panel.graph, state: restored, runID: panel.runID, revision: 9})
	if panel.auto || !strings.Contains(panel.err, "reconciliation") {
		t.Fatal("restored interrupted dispatch did not pause with reconciliation guidance")
	}
}

func TestWorkflowRejectedResumeCannotDispatch(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.runID = ""
	panel.loading = true
	model.acceptWorkflowResult(workflowResultMsg{panel: panel, initial: true, runID: "wrong-workspace", graph: panel.graph, state: workflow.State{}, revision: 3, err: fmt.Errorf("different workspace")})
	if panel.runID != "" || panel.auto {
		t.Fatal("rejected resume became actionable")
	}
	if cmd := model.workflowAction("next", ""); cmd != nil {
		t.Fatal("rejected resume dispatched work")
	}
}
