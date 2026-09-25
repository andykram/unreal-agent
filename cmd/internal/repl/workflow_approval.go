package repl

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

func (model *uiModel) updateWorkflowApproval(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := model.workflow
	switch key.String() {
	case "a", "A", "shift+a", "shift+A":
		panel.executor.approvals.AllowAll()
	case "y", "Y", "shift+y", "shift+Y":
		// Keep the gate's default: ask before each command.
	case "esc":
		panel.visible = false
		model.message = "Workflow paused before execution. Use /workflow to return."
		return model, nil
	default:
		return model, nil
	}
	panel.approvalChoice = false
	panel.auto = true
	return model, workflowTick(panel)
}
func (model *uiModel) workflowApprovalView() tea.View {
	return model.workflowOverlayView((*uiModel).workflowApprovalContentView)
}
func (model *uiModel) workflowApprovalContentView() tea.View {
	palette := model.palette()
	width, height := max(1, model.width), max(1, model.height)
	lines := []string{
		model.ink("◇ WORKFLOW  /  EXECUTION PERMISSION", palette.lilac, true),
		model.ink(workflowFlat(model.workflow.graph.Name), palette.accent, true), "",
		"Approve all commands for this run?", "",
		"A  Approve all commands", "   Includes worktree creation, subprocesses, and agent Bash tools.",
		"   Applies only to this open run; resuming asks again.", "",
		"Y  Ask before each command", "   Review each command before it runs.", "",
		"Explicit workflow review checkpoints still need approval.", "",
		model.ink("A approve all · Y ask each · Esc pause", palette.lilac, true),
	}
	model.workflow.approvalRows = map[int]string{}
	var wrapped []string
	for index, line := range lines {
		rows := strings.Split(ansi.Hardwrap(line, width, true), "\n")
		action := ""
		if index >= 5 && index <= 7 {
			action = "a"
		}
		if index >= 9 && index <= 10 {
			action = "y"
		}
		if action != "" {
			for offset := range rows {
				model.workflow.approvalRows[len(wrapped)+offset] = action
			}
		}
		wrapped = append(wrapped, rows...)
	}
	if len(wrapped) > height {
		wrapped = append(wrapped[:max(0, height-1)], ansi.Truncate("A all · Y ask each · Esc pause", width, "…"))
		for row := range model.workflow.approvalRows {
			if row >= height-1 {
				delete(model.workflow.approvalRows, row)
			}
		}
	}
	for index, line := range wrapped {
		wrapped[index] = ansi.Truncate(line, width, "…")
	}
	return tea.NewView(strings.Join(wrapped, "\n"))
}
