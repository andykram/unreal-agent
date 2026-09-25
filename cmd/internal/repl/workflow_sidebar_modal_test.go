package repl

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWorkflowSidebarMenuAndRenameTakePrecedenceOverApproval(t *testing.T) {
	for _, permissionChoice := range []bool{false, true} {
		model := workflowThreadFixture(t)
		panel := model.workflow
		request := &approvalRequest{answer: make(chan bool, 1)}
		gate := &approvalGate{}
		if !permissionChoice {
			gate.enqueue(request)
		}
		panel.executor = &replWorkflowExecutor{approvals: gate}
		panel.approvalChoice = permissionChoice
		openMenu := func() {
			model.View()
			row := -1
			for y, id := range model.sidebarRows {
				if id == model.state.Current.SessionID {
					row = y
				}
			}
			if row < 0 {
				t.Fatal("conversation missing from approval sidebar")
			}
			model.Update(tea.MouseClickMsg{X: model.width - 2, Y: row, Button: tea.MouseRight})
			if model.sessionMenu == nil || !strings.Contains(model.View().Content, "Session actions") {
				t.Fatal("context menu hidden by approval")
			}
		}
		openMenu()
		model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		if model.sessionMenu != nil || !panel.visible || panel.approvalChoice != permissionChoice || (!permissionChoice && gate.Pending() != request) {
			t.Fatal("menu Escape reached underlying permission handler")
		}
		openMenu()
		model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if model.rename == nil || !strings.Contains(model.View().Content, "Enter save") {
			t.Fatal("rename hidden by approval")
		}
		previous := model.rename.draft.Source()
		model.Update(tea.PasteMsg{Content: " pasted"})
		model.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		if !strings.Contains(model.rename.draft.Source(), " pasted") || model.rename.draft.Source() == previous || gate.approveAll {
			t.Fatal("rename input reached approval or was swallowed")
		}
		model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		if model.rename != nil || panel.approvalChoice != permissionChoice || (!permissionChoice && gate.Pending() != request) {
			t.Fatal("rename Escape changed permission")
		}
		select {
		case <-request.answer:
			t.Fatal("modal interaction answered command approval")
		default:
		}
	}
}
