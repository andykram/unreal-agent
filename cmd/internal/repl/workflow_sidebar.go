package repl

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

const workflowSidebarID = "workflow:active"

type sidebarThread struct {
	id, name string
	depth    int
	selected bool
}

func workflowPanelID(panel *workflowPanel) string {
	if panel.threadID != "" {
		return "workflow:" + panel.threadID
	}
	if panel.runID != "" {
		return "workflow:" + panel.runID
	}
	return workflowSidebarID
}

func workflowThreadStatus(panel *workflowPanel) string {
	if panel.approvalChoice {
		return "approval needed"
	}
	if panel.executor != nil && panel.executor.approvals != nil && panel.executor.approvals.Pending() != nil {
		return "approval needed"
	}
	if panel.agent != nil {
		if len(panel.agent.Questions()) > 0 {
			return "answer needed"
		}
		if panel.agent.options.Approvals != nil && panel.agent.options.Approvals.Pending() != nil {
			return "approval needed"
		}
	}
	if panel.loading {
		return "working"
	}
	complete := len(panel.graph.Steps) > 0
	for _, step := range panel.graph.Steps {
		status := panel.state[step.ID].Status
		if status != "completed" && status != "skipped" {
			complete = false
			break
		}
	}
	if complete {
		return "completed"
	}
	if panel.err != "" {
		return "needs attention"
	}
	if panel.auto {
		return "running"
	}
	return "paused"
}

func (model *uiModel) syncThreadQuestion() tea.Cmd {
	model.question = nil
	model.showQuestion = false
	model.dismissQuestion = false
	return model.syncQuestion()
}

func (model *uiModel) sidebarThreads() []sidebarThread {
	var threads []sidebarThread
	workflowSelected := model.workflow != nil && model.workflow.visible
	for _, choice := range model.forkChoices {
		selected := model.state != nil && choice.Metadata.SessionID == model.state.Current.SessionID && !workflowSelected
		threads = append(threads, sidebarThread{id: choice.Metadata.SessionID, name: choice.Metadata.Name, depth: choice.Depth, selected: selected})
	}
	for _, panel := range model.workflowPanels() {
		name := panel.graph.Name
		if name == "" {
			name = "Loading workflow"
		}
		identity := panel.runID
		if identity == "" {
			identity = panel.threadID
		}
		if identity != "" {
			name += " [" + identity[:min(8, len(identity))] + "]"
		}
		threads = append(threads, sidebarThread{id: workflowPanelID(panel), name: "◇ " + name + " · " + workflowThreadStatus(panel), selected: panel == model.workflow && workflowSelected})
	}
	return threads
}

func (model *uiModel) selectSidebarThread(id string) (tea.Model, tea.Cmd) {
	for _, panel := range model.workflowPanels() {
		if id != workflowPanelID(panel) {
			continue
		}
		if model.workflow != nil {
			model.workflow.visible = false
		}
		model.workflow = panel
		panel.visible = true
		return model, model.syncThreadQuestion()
	}
	if model.state == nil {
		return model, nil
	}
	if id == model.state.Current.SessionID {
		if model.workflow != nil {
			model.workflow.visible = false
		}
		return model, model.syncThreadQuestion()
	}
	command, err := model.switchSession(id)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	if model.workflow != nil {
		model.workflow.visible = false
	}
	return model, tea.Batch(command, model.syncThreadQuestion())
}

func (model *uiModel) withForkSidebar(lines []string, width int) []string {
	inner := width - 2
	model.sidebarRows = make(map[int]string)
	threads := model.sidebarThreads()
	current := 0
	for index, choice := range threads {
		if choice.selected {
			current = index
			break
		}
	}
	visible := max(0, len(lines)-1)
	start := max(0, current-visible/2)
	if start+visible > len(threads) {
		start = max(0, len(threads)-visible)
	}
	for row := range lines {
		label := ""
		if row == 0 {
			label = " ◆ UNREAL  /  THREADS"
			if inner < 20 {
				label = "UNREAL"
				if inner < 6 {
					label = "U"
				}
			}
		}
		if row > 0 && start+row-1 < len(threads) {
			choice := threads[start+row-1]
			model.sidebarRows[row] = choice.id
			marker := "  "
			if choice.selected {
				marker = "› "
			}
			label = marker + strings.Repeat("  ", min(choice.depth, 5)) + sanitizeTerminal(choice.name)
		}
		label = ansi.Truncate(label, inner, "…")
		label += strings.Repeat(" ", max(0, inner-ansi.StringWidth(label)))
		if !model.noColor {
			shade := model.palette().muted
			if row == 0 || strings.HasPrefix(label, "›") {
				shade = model.palette().lilac
			}
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(shade))
			if row == 0 {
				style = style.Bold(true).Background(lipgloss.Color(model.palette().chip))
			}
			label = style.Render(label)
		}
		content := ansi.Truncate(lines[row], model.width-width, "")
		content += strings.Repeat(" ", max(0, model.width-width-ansi.StringWidth(content)))
		lines[row] = content + model.ink("│", model.palette().rule, false) + label + " "
	}
	return lines
}

// handleSidebarClick routes thread navigation before the graph consumes clicks.
func (model *uiModel) handleSidebarClick(mouse tea.MouseClickMsg) (tea.Cmd, bool) {
	if model.powerBar != nil || model.rename != nil || model.settings != nil || model.contextReport != nil || model.showQuestion || model.resumePopup != nil || model.sessionMenu != nil {
		return nil, false
	}
	if gate := model.interactionApprovalGate(); gate != nil && gate.Pending() != nil && (model.workflow == nil || !model.workflow.visible) {
		return nil, false
	}
	width := model.sidebarSize()
	if width == 0 || mouse.X < model.width-width {
		return nil, false
	}
	if mouse.X == model.width-width && mouse.Button == tea.MouseLeft {
		model.sidebarDragging = true
		return nil, true
	}
	id := model.sidebarRows[mouse.Y]
	if id == "" {
		return nil, true
	}
	if mouse.Button == tea.MouseRight {
		if !strings.HasPrefix(id, "workflow:") {
			model.sessionMenu = &sessionContextMenu{id: id, row: mouse.Y}
		}
		return nil, true
	}
	if mouse.Button == tea.MouseLeft {
		_, command := model.selectSidebarThread(id)
		return command, true
	}
	return nil, true
}
