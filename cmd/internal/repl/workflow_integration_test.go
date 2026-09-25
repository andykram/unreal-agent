package repl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestWorkflowRetentionFailurePreservesCreatedAndRestoredRuns(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, failure := range []string{"store", "receipts"} {
			t.Run(fmt.Sprintf("resume=%t/%s", resume, failure), func(t *testing.T) {
				model := workflowViewFixture(t)
				panel := model.workflow
				panel.executor = &replWorkflowExecutor{notices: make(chan workflowAgentNotice)}
				directory := t.TempDir()
				database := filepath.Join(directory, "workflows.sqlite")
				store, err := workflow.OpenStore(database)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				graph := workflow.Graph{Version: 1, Name: "retention", ExecutionMode: "live", Steps: []workflow.Step{{ID: "seed", Kind: "approval"}, {ID: "join", Kind: "join", Needs: []string{"seed"}}}}
				state := workflow.State{"seed": {Status: "completed", Outcome: "approved"}}
				id, revision, err := store.Create(graph, state)
				if err != nil {
					t.Fatal(err)
				}
				if resume {
					graph, state, revision, err = store.Load(id)
					if err != nil {
						t.Fatal(err)
					}
				}
				if failure == "store" {
					store.Close()
				} else {
					// A file at the receipt directory path reliably fails cleanup even as root.
					if err := os.WriteFile(filepath.Join(directory, "execution-receipts"), []byte("blocked"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				cleaner := &workflowReceiptCleaner{}
				defer cleaner.Close()
				warning := cleanupWorkflowRetention(store, cleaner, directory, id, nil)
				if warning == "" {
					t.Fatal("expected maintenance failure")
				}
				panel.runID = ""
				panel.loading = true
				model.Update(workflowResultMsg{panel: panel, initial: true, graph: graph, state: state, runID: id, revision: revision, warning: warning})
				if panel.runID != id || panel.revision != revision || panel.loading || panel.err != "" || !panel.approvalChoice {
					t.Fatalf("maintenance failure rejected usable run: %#v", panel)
				}
				if !strings.Contains(model.workflowView().Content, "Warning:") {
					t.Fatal("cleanup warning is invisible")
				}
				model.Update(workflowResultMsg{panel: panel, graph: graph, state: state, runID: id, revision: revision})
				if panel.warning != warning {
					t.Fatal("subsequent progress erased cleanup warning")
				}
				verify, err := workflow.OpenStore(database)
				if err != nil {
					t.Fatal(err)
				}
				defer verify.Close()
				if _, _, _, err := verify.Load(id); err != nil {
					t.Fatal("run no longer resumable:", err)
				}
			})
		}
	}
}

func TestWorkflowResumeContinuesAfterReceiptCleanupFailure(t *testing.T) {
	root := t.TempDir()
	getenv := testEnv(root, "")
	directory, err := workflowStateDirectory(getenv)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workflow.OpenStore(filepath.Join(directory, "workflows.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := DefaultConfig()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	graph := workflow.Graph{Version: 1, Name: "resume", ExecutionMode: "live", Workspace: root, ExecutionConfig: encoded, Steps: []workflow.Step{{ID: "seed", Kind: "approval"}, {ID: "join", Kind: "join", Needs: []string{"seed"}}}}
	id, _, err := store.Create(graph, workflow.State{"seed": {Status: "completed", Outcome: "approved"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "execution-receipts"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	model := &uiModel{ctx: t.Context(), state: &SessionState{Workspace: root}, config: &ConfigStore{active: config}, getenv: getenv}
	_, cmd := model.runWorkflow("resume "+id, "/workflow resume "+id)
	if cmd == nil {
		t.Fatal("resume did not start")
	}
	defer model.receiptCleaner.Close()
	found := false
	for _, part := range cmd().(tea.BatchMsg) {
		if result, ok := part().(workflowResultMsg); ok {
			found = true
			if result.err != nil || !strings.Contains(result.warning, "Receipt retention skipped") {
				t.Fatalf("resume result: %#v", result)
			}
			model.Update(result)
		}
	}
	if !found || model.workflow.runID != id || !model.workflow.approvalChoice || model.workflow.err != "" {
		t.Fatal("cleanup prevented resume")
	}
}
