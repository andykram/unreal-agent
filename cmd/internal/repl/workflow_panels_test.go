package repl

import (
	"context"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowPanelsRetainIdentityAndResumeSelection(t *testing.T) {
	model := workflowViewFixture(t)
	first := model.workflow
	canceled := false
	first.cancel = func() { canceled = true }
	first.loading = true
	second := &workflowPanel{runID: "second", loading: true}
	model.registerWorkflowPanel(second)
	if canceled || len(model.workflowPanels()) != 2 || first.threadID == "" || second.threadID == "" || first.threadID == second.threadID {
		t.Fatal("registration lost live panel identity")
	}
	id := first.threadID
	_, cmd := model.runWorkflow("resume "+first.runID, "/workflow resume "+first.runID)
	if cmd != nil || model.workflow != first || first.threadID != id || !first.visible || len(model.workflowPanels()) != 2 || canceled {
		t.Fatal("resume restarted existing panel")
	}
	model.registerWorkflowPanel(first)
	if len(model.workflowPanels()) != 2 {
		t.Fatal("duplicate panel registration")
	}
	ids := model.workflowRunIDs()
	if len(ids) != 2 {
		t.Fatal("cleanup excludes omitted open run", ids)
	}
}

func TestWorkflowBackgroundResultAndRecoveryKeepSelection(t *testing.T) {
	model := workflowViewFixture(t)
	background := model.workflow
	selected := &workflowPanel{visible: true, runID: "selected", state: workflow.State{}}
	model.registerWorkflowPanel(selected)
	background.loading = true
	background.auto = true
	state := workflow.State{"implement": {Status: "running", DispatchStarted: true}}
	model.acceptWorkflowResult(workflowResultMsg{panel: background, graph: background.graph, state: state, runID: background.runID, revision: 11})
	if background.revision != 11 || background.loading || background.auto || background.err == "" || background.recovery != nil || model.workflow != selected || selected.recovery != nil {
		t.Fatal("background recovery stole selection or dropped result")
	}
	background.executor = &replWorkflowExecutor{notices: make(chan workflowAgentNotice, 1)}
	background.executor.notices <- workflowAgentNotice{text: "Agent waiting for model response"}
	background.loading = true
	_, cmd := model.advanceWorkflowTick(workflowTickMsg{panel: background})
	if cmd == nil || background.stage != "Agent waiting for model response" || model.workflow != selected {
		t.Fatal("background heartbeat lost")
	}
}

func TestWorkflowBackgroundActionUsesItsOwnCheckpoint(t *testing.T) {
	directory := t.TempDir()
	store, err := workflow.OpenStore(filepath.Join(directory, "workflows.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	graph := workflow.Graph{Version: 1, Name: "join", ExecutionMode: "live", Steps: []workflow.Step{{ID: "seed", Kind: "approval"}, {ID: "join", Kind: "join", Needs: []string{"seed"}}}}
	initial := workflow.State{"seed": {Status: "completed", Outcome: "approved"}}
	firstID, firstRev, err := store.Create(graph, initial)
	if err != nil {
		t.Fatal(err)
	}
	secondID, secondRev, err := store.Create(graph, initial)
	if err != nil {
		t.Fatal(err)
	}
	first := &workflowPanel{graph: graph, state: initial, runID: firstID, revision: firstRev, directory: directory}
	second := &workflowPanel{graph: graph, state: initial, runID: secondID, revision: secondRev, directory: directory}
	model := &uiModel{ctx: t.Context(), workflow: first}
	model.registerWorkflowPanel(second)
	cmd := model.workflowActionFor(first, "next", "")
	if cmd == nil {
		t.Fatal("no background action")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected dispatch batch")
	}
	for _, part := range batch {
		if result, ok := part().(workflowResultMsg); ok {
			if result.err != nil {
				t.Fatal(result.err)
			}
			model.acceptWorkflowResult(result)
		}
	}
	_, a, _, _ := store.Load(firstID)
	_, b, rev, _ := store.Load(secondID)
	if a["join"].Status != "completed" || b["join"].Status != "" || rev != secondRev || model.workflow != second || first.revision == firstRev {
		t.Fatal("action routed to wrong run")
	}
}

func TestCloseWorkflowCancelsEveryPanelAndProbe(t *testing.T) {
	a, ca := context.WithCancel(t.Context())
	b, cb := context.WithCancel(t.Context())
	probe, cp := context.WithCancel(t.Context())
	first := &workflowPanel{cancel: ca, recovery: &workflowRecovery{cancel: cp}}
	second := &workflowPanel{cancel: cb}
	model := &uiModel{workflow: first}
	model.registerWorkflowPanel(second)
	model.closeWorkflow()
	if a.Err() == nil || b.Err() == nil || probe.Err() == nil {
		t.Fatal("background execution or probe leaked")
	}
}

func TestHiddenWorkflowDoesNotCaptureConversationInteractions(t *testing.T) {
	main := &Runtime{}
	gate := &approvalGate{pending: []*approvalRequest{{}}}
	model := &uiModel{runtime: main, workflow: &workflowPanel{agent: &Runtime{}, executor: &replWorkflowExecutor{approvals: gate}}}
	if model.interactionRuntime() != main || model.interactionApprovalGate() != nil {
		t.Fatal("hidden workflow stole conversation interaction")
	}
	model.workflow.visible = true
	if model.interactionRuntime() != model.workflow.agent || model.interactionApprovalGate() != gate {
		t.Fatal("selected visible workflow lost interaction")
	}
}

func TestWorkflowPromptNoticeSurvivesFinalResultDrain(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.executor = &replWorkflowExecutor{notices: make(chan workflowAgentNotice, 2)}
	panel.executor.notices <- workflowAgentNotice{stepID: "implement", prompt: &workflowPromptSnapshot{}}
	model.acceptWorkflowResult(workflowResultMsg{panel: panel, graph: panel.graph, state: workflow.State{"implement": {Status: "completed"}}, runID: panel.runID, revision: 12})
	if _, ok := panel.prompts["implement"]; !ok {
		t.Fatal("final-result drain discarded prompt snapshot")
	}
	panel.executor.notices <- workflowAgentNotice{stepID: "review", prompt: &workflowPromptSnapshot{}}
	model.advanceWorkflowTick(workflowTickMsg{panel: panel})
	if len(panel.prompts) != 2 {
		t.Fatal("tick replaced previous prompt snapshot")
	}
	panel.detailTop = 7
	model.updateWorkflow(tea.KeyPressMsg{Code: 'P', Text: "P"})
	if !panel.fullPrompt || panel.detailTop != 0 {
		t.Fatal("P did not open prompt details")
	}
	model.updateWorkflow(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if panel.fullPrompt {
		t.Fatal("p did not collapse prompt details")
	}
}
