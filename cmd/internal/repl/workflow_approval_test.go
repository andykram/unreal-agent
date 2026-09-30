package repl

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

func TestWorkflowApproveAllIsRunScopedAndIncludesChildCommands(t *testing.T) {
	gate := &approvalGate{}
	child := &approvalGate{parent: gate}
	first := &approvalRequest{answer: make(chan bool, 1)}
	child.enqueue(first)
	if gate.Pending() != first || child.Pending() != first {
		t.Fatal("child approval not visible through run")
	}
	gate.AllowAll()
	if !<-first.answer || child.Pending() != nil {
		t.Fatal("pending command not approved")
	}
	next := &approvalRequest{answer: make(chan bool, 1)}
	child.enqueue(next)
	if !<-next.answer {
		t.Fatal("later child command did not inherit consent")
	}
	fresh := &approvalGate{}
	fresh.enqueue(&approvalRequest{answer: make(chan bool, 1)})
	if fresh.Pending() == nil {
		t.Fatal("consent leaked to another run")
	}
}
func TestWorkflowPermissionChoiceDoesNotDispatchUntilSelected(t *testing.T) {
	model := workflowViewFixture(t)
	panel := model.workflow
	panel.approvalChoice = true
	panel.auto = false
	panel.executor = &replWorkflowExecutor{approvals: &approvalGate{}}
	if !strings.Contains(model.workflowApprovalView().Content, "Approve all commands") {
		t.Fatal("choice missing")
	}
	model.updateWorkflowApproval(tea.KeyPressMsg{Code: tea.KeyEnter})
	if panel.auto || !panel.approvalChoice {
		t.Fatal("Enter silently approved")
	}
	_, cmd := model.updateWorkflowApproval(tea.KeyPressMsg{Code: 'a'})
	if cmd == nil || !panel.auto || panel.approvalChoice || !panel.executor.approvals.approveAll {
		t.Fatal("choice not applied")
	}
}
