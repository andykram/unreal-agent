package repl

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func workflowThreadFixture(t *testing.T) *uiModel {
	t.Helper()
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	if _, err = state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, NewModelCatalog(config, getenv), nil, nil)
	model.width = 100
	model.height = 20
	model.noColor = true
	model.workflow = &workflowPanel{visible: true, auto: true, runID: "run-1", graph: workflow.Graph{Name: "Delivery"}, state: workflow.State{"ready": {Status: "completed"}}}
	return model
}
func TestWorkflowThreadSidebarPreservesExecution(t *testing.T) {
	model := workflowThreadFixture(t)
	panel := model.workflow
	threads := model.sidebarThreads()
	if len(threads) != 2 || threads[1].id != workflowPanelID(panel) || !threads[1].selected || threads[0].selected {
		t.Fatalf("threads:%#v", threads)
	}
	lines := make([]string, model.height)
	model.withForkSidebar(lines, model.sidebarSize())
	if !strings.Contains(strings.Join(lines, "\n"), "Delivery") {
		t.Fatal("workflow missing from rail")
	}
	if _, handled := model.handleSidebarClick(tea.MouseClickMsg{X: model.width - 2, Y: 1, Button: tea.MouseLeft}); !handled {
		t.Fatal("session click not handled")
	}
	if model.workflow != panel || panel.visible || !panel.auto || panel.state["ready"].Status != "completed" {
		t.Fatal("navigation discarded or paused execution")
	}
	_, command := model.selectSidebarThread(workflowPanelID(panel))
	if command != nil || !panel.visible {
		t.Fatal("cannot return to workflow")
	}
}
func TestWorkflowThreadPaletteJump(t *testing.T) {
	model := workflowThreadFixture(t)
	model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if model.powerBar == nil || !strings.Contains(model.View().Content, "Workflow thread") {
		t.Fatal("workflow swallowed command palette")
	}
	selected := model.powerBar.filtered()[model.powerBar.selected]
	if selected.kind != "workflow" || selected.Value != workflowPanelID(model.workflow) {
		t.Fatalf("active thread not selected:%#v", selected)
	}
	model.powerBar.query = "session"
	model.powerBar.selected = 0
	// Fork entries are described as forks; choose their explicit kind without a query.
	model.powerBar.query = ""
	for i, choice := range model.powerBar.filtered() {
		if choice.kind == "session" {
			model.powerBar.selected = i
			break
		}
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.workflow.visible {
		t.Fatal("palette session did not show conversation")
	}
	model.openPowerBar()
	model.powerBar.query = "workflow Delivery"
	model.powerBar.selected = 0
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !model.workflow.visible {
		t.Fatal("palette workflow did not reopen graph")
	}
}
func TestWorkflowThreadSidebarNavigatesRecovery(t *testing.T) {
	model := workflowThreadFixture(t)
	model.workflow.recovery = &workflowRecovery{}
	model.withForkSidebar(make([]string, model.height), model.sidebarSize())
	if _, handled := model.handleSidebarClick(tea.MouseClickMsg{X: 99, Y: 1, Button: tea.MouseLeft}); !handled || model.workflow.visible {
		t.Fatal("recovery rail cannot select conversation")
	}
	if model.workflow.recovery == nil {
		t.Fatal("navigation discarded recovery form")
	}
}

func TestWorkflowThreadMultiplePaletteAndSidebar(t *testing.T) {
	model := workflowThreadFixture(t)
	first := model.workflow
	first.threadID = "thread-one"
	second := &workflowPanel{threadID: "thread-two", runID: "run-2", auto: true, graph: workflow.Graph{Name: "Delivery"}, state: workflow.State{"saved": {Status: "completed"}}}
	model.workflows = []*workflowPanel{first, second}
	threads := model.sidebarThreads()
	if len(threads) != 3 || threads[1].id == threads[2].id {
		t.Fatalf("threads: %#v", threads)
	}
	model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	model.powerBar.query = "run-2"
	model.powerBar.selected = 0
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.workflow != second || first.visible || !second.visible || !first.auto || !second.auto {
		t.Fatal("palette lost panel execution or selected wrong run")
	}
	if second.state["saved"].Status != "completed" {
		t.Fatal("panel progress lost")
	}
	model.withForkSidebar(make([]string, model.height), model.sidebarSize())
	for row, id := range model.sidebarRows {
		if id == workflowPanelID(first) {
			model.Update(tea.MouseClickMsg{X: model.width - 2, Y: row, Button: tea.MouseLeft})
			break
		}
	}
	if model.workflow != first || !first.visible || second.visible {
		t.Fatal("sidebar did not select first panel")
	}
}
func TestWorkflowThreadIdentityAndAttention(t *testing.T) {
	panel := &workflowPanel{threadID: "stable", runID: "first", approvalChoice: true, loading: true}
	if workflowPanelID(panel) != "workflow:stable" || workflowThreadStatus(panel) != "approval needed" {
		t.Fatal("unstable identity or hidden approval")
	}
	panel.runID = "second"
	if workflowPanelID(panel) != "workflow:stable" {
		t.Fatal("run update changed thread identity")
	}
}
