package repl

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowRecoveryOpenIsReadOnly(t *testing.T) {
	model := recoveryViewFixture(t)
	panel := model.workflow
	panel.recovery = nil
	panel.auto = true
	panel.selected = 1
	before := panel.state["review"]
	model.openWorkflowRecovery()
	if panel.recovery == nil || panel.recovery.stage != "inspect" || panel.recovery.action != 0 || panel.auto || panel.recovery.stepID != "review" {
		t.Fatal("interruption did not open default evidence inspector")
	}
	model.selectRecoveryAction()
	if !reflect.DeepEqual(before, panel.state["review"]) {
		t.Fatal("evidence inspector changed node")
	}
	panel.loading = true
	previous := panel.recovery
	model.openWorkflowRecovery()
	if panel.recovery != previous {
		t.Fatal("opened recovery during active execution")
	}
}

func TestWorkflowRecoveryValidatesResultAndReason(t *testing.T) {
	model := recoveryViewFixture(t)
	for i := range model.workflow.graph.Steps {
		if model.workflow.graph.Steps[i].ID == "review" {
			model.workflow.graph.Steps[i].Kind = "command"
		}
	}
	recovery := model.workflow.recovery
	recovery.action = 1
	model.selectRecoveryAction()
	_ = recovery.result.Set(`{"output":"ok","exit_code":0}`)
	if _, err := model.recoveryDecision(); err == nil {
		t.Fatal("empty reason accepted")
	}
	_ = recovery.note.Set("Confirmed saved result")
	for _, raw := range []string{`{`, `{}`, `{"output":"ok"}`, `{"exit_code":null}`, `{} {}`, `[]`, `null`, `{"unknown":0}`, `{"exit_code":"zero"}`} {
		_ = recovery.result.Set(raw)
		if _, err := model.recoveryDecision(); err == nil {
			t.Fatalf("invalid envelope accepted: %s", raw)
		}
	}
	_ = recovery.result.Set(`{"output":{"count":9007199254740993},"exit_code":0}`)
	model.updateWorkflowRecovery(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if recovery.stage != "confirm" {
		t.Fatalf("valid review rejected: %s", recovery.err)
	}
	model.updateWorkflowRecovery(tea.KeyPressMsg{Code: tea.KeyEscape})
	if recovery.stage != "edit" || recovery.note.Source() != "Confirmed saved result" {
		t.Fatal("Escape did not preserve the editable decision")
	}
}

func durableRecoveryFixture(t *testing.T) (*uiModel, *workflow.Store) {
	t.Helper()
	model := recoveryViewFixture(t)
	panel := model.workflow
	panel.directory = t.TempDir()
	store, err := workflow.OpenStore(filepath.Join(panel.directory, "workflows.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	graph := workflow.Graph{Version: 1, Name: "recover", ExecutionMode: "live", Steps: []workflow.Step{{ID: "review", Kind: "command", Spec: map[string]any{"argv": []any{"unused"}}}}}
	state := workflow.State{"review": {Status: "running", Phase: "execute", DispatchStarted: true, AttemptKey: "attempt-1", ExternalKey: "external-1"}}
	id, revision, err := store.Create(graph, state)
	if err != nil {
		t.Fatal(err)
	}
	panel.graph, panel.state, panel.runID, panel.revision = graph, state, id, revision
	return model, store
}

func TestWorkflowRecoveryDecisionsPersistWithoutDispatch(t *testing.T) {
	for _, action := range []int{1, 2, 3} {
		t.Run(workflowRecoveryActions[action], func(t *testing.T) {
			model, store := durableRecoveryFixture(t)
			panel := model.workflow
			recovery := panel.recovery
			recovery.action = action
			model.selectRecoveryAction()
			_ = recovery.note.Set("Verified executor stopped; inspected saved evidence")
			_ = recovery.result.Set(`{"output":"verified output","exit_code":0}`)
			recovery.stage = "confirm"
			if action == 3 {
				for _, phrase := range []string{"", "retry", "retry other", "retry review "} {
					_ = recovery.confirm.Set(phrase)
					if model.saveWorkflowRecovery() != nil || panel.loading {
						t.Fatalf("unauthorized retry accepted: %q", phrase)
					}
				}
				_ = recovery.confirm.Set("retry review")
			}
			command := model.saveWorkflowRecovery()
			if command == nil {
				t.Fatalf("save rejected: %s", recovery.err)
			}
			message, ok := command().(workflowReconciledMsg)
			if !ok {
				t.Fatal("wrong result")
			}
			if message.result.err != nil {
				t.Fatal(message.result.err)
			}
			model.acceptWorkflowReconciliation(message)
			_, state, revision, err := store.Load(panel.runID)
			if err != nil {
				t.Fatal(err)
			}
			node := state["review"]
			if revision <= 1 || len(node.Reconciliations) != 1 || panel.auto || panel.recovery != nil || panel.loading {
				t.Fatalf("decision not persisted and paused: %+v", node)
			}
			expected := map[int]string{1: "completed", 2: "failed", 3: "running"}[action]
			if node.Status != expected || node.DispatchStarted {
				t.Fatalf("wrong node after decision: %+v", node)
			}
			if action == 3 && !strings.Contains(panel.err, "Retry authorized") {
				t.Fatal("retry instructions missing")
			}
		})
	}
}

func TestWorkflowRecoveryIgnoresStaleResults(t *testing.T) {
	model := recoveryViewFixture(t)
	panel := model.workflow
	old := panel.recovery
	replacement := &workflowRecovery{stepID: "review", stage: "inspect"}
	panel.recovery = replacement
	model.acceptWorkflowEvidence(workflowEvidenceMsg{panel: panel, recovery: old, evidence: "stale"})
	model.acceptWorkflowReconciliation(workflowReconciledMsg{panel: panel, recovery: old, result: workflowResultMsg{revision: 999}})
	if panel.recovery != replacement || replacement.evidence != "" || panel.revision == 999 {
		t.Fatal("stale recovery result replaced current inspector")
	}
}

func TestWorkflowRecoveryAutomaticallyOpensForInterruptedResult(t *testing.T) {
	model := recoveryViewFixture(t)
	panel := model.workflow
	panel.visible = true
	panel.recovery = nil
	panel.auto, panel.loading = true, true
	model.acceptWorkflowResult(workflowResultMsg{panel: panel, graph: panel.graph, state: panel.state, runID: panel.runID, revision: panel.revision})
	if panel.recovery == nil || panel.recovery.stage != "inspect" || panel.recovery.stepID != "review" || panel.auto || panel.loading {
		t.Fatal("interrupted checkpoint did not open paused inspector")
	}
}
