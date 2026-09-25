package repl

import (
	"context"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowApproveAllKeyFormsExecuteThroughUpdate(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: 'a'}, {Code: 'A', Text: "A"}, {Code: 'a', Mod: tea.ModShift}} {
		t.Run(key.String(), func(t *testing.T) {
			model := workflowViewFixture(t)
			model.ctx = t.Context()
			panel := model.workflow
			panel.visible = true
			panel.approvalChoice = true
			panel.state = workflow.State{}
			panel.directory = t.TempDir()
			panel.graph = workflow.Graph{Version: 1, Name: "key approval", ExecutionMode: "live", Steps: []workflow.Step{{ID: "workspace", Kind: "worktree", Spec: map[string]any{"base": "current"}}}}
			store, err := workflow.OpenStore(filepath.Join(panel.directory, "workflows.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			panel.runID, panel.revision, err = store.Create(panel.graph, panel.state)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			panel.executor = &replWorkflowExecutor{config: &ConfigStore{active: DefaultConfig()}, directory: panel.directory, workspace: t.TempDir(), approvals: &approvalGate{}, notices: make(chan workflowAgentNotice, 8), worktrees: receiptWorktreeFunc(func(context.Context, string, string, string) (string, error) {
				calls++
				return "/tmp/approved-workspace", nil
			})}
			model.registerWorkflowPanel(panel)
			_, tick := model.Update(key)
			if tick == nil || panel.approvalChoice || !panel.auto || !panel.executor.approvals.approveAll {
				t.Fatal("approve-all key ignored")
			}
			_, action := model.Update(tick())
			if action == nil {
				t.Fatal("approval did not dispatch after tick")
			}
			var apply func(tea.Cmd)
			apply = func(command tea.Cmd) {
				if command == nil {
					return
				}
				switch message := command().(type) {
				case tea.BatchMsg:
					for _, child := range message {
						apply(child)
					}
				case workflowResultMsg:
					if message.err != nil {
						t.Fatal(message.err)
					}
					model.Update(message)
				}
			}
			apply(action)
			if calls != 1 || panel.state["workspace"].Status != "completed" {
				t.Fatalf("approved step did not complete: calls=%d state=%+v", calls, panel.state)
			}
		})
	}
}

func TestWorkflowAskEachKeyFormsPreserveCommandGate(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: 'y'}, {Code: 'Y', Text: "Y"}, {Code: 'y', Mod: tea.ModShift}} {
		model := workflowViewFixture(t)
		panel := model.workflow
		panel.visible = true
		panel.approvalChoice = true
		panel.executor = &replWorkflowExecutor{approvals: &approvalGate{}}
		_, command := model.Update(key)
		if command == nil || panel.approvalChoice || !panel.auto || panel.executor.approvals.approveAll {
			t.Fatalf("ask-each key %s not applied", key.String())
		}
	}
}

func TestWorkflowPermissionLabelsAndDescriptionsAreClickable(t *testing.T) {
	for _, action := range []string{"a", "y"} {
		model := workflowViewFixture(t)
		panel := model.workflow
		panel.visible, panel.approvalChoice = true, true
		panel.executor = &replWorkflowExecutor{approvals: &approvalGate{}}
		model.workflowApprovalView()
		rows := []int{}
		for row, mapped := range panel.approvalRows {
			if mapped == action {
				rows = append(rows, row)
			}
		}
		if len(rows) < 2 {
			t.Fatalf("%s label and description not both clickable: %v", action, panel.approvalRows)
		}
		_, command := model.Update(tea.MouseClickMsg{X: 2, Y: rows[0], Button: tea.MouseLeft})
		if command == nil || panel.approvalChoice || !panel.auto || panel.executor.approvals.approveAll != (action == "a") {
			t.Fatalf("click did not apply %s choice", action)
		}
	}
}

func TestWorkflowPermissionMouseTargetsExcludeClippedRows(t *testing.T) {
	model := workflowViewFixture(t)
	for _, height := range []int{1, 5, 10, 24} {
		model.height = height
		model.workflowApprovalView()
		for row := range model.workflow.approvalRows {
			if row < 0 || row >= height-1 {
				t.Fatalf("offscreen/footer choice target row=%d height=%d", row, height)
			}
		}
	}
}
