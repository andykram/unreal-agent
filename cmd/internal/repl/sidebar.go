package repl

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

type sidebarThread struct {
	id, name string
	depth    int
	selected bool
}

func (model *uiModel) syncThreadQuestion() tea.Cmd {
	model.question = nil
	model.showQuestion = false
	model.dismissQuestion = false
	return model.syncQuestion()
}

func (model *uiModel) sidebarThreads() []sidebarThread {
	var threads []sidebarThread
	for _, choice := range model.forkChoices {
		selected := model.state != nil && choice.Metadata.SessionID == model.state.Current.SessionID
		threads = append(threads, sidebarThread{id: choice.Metadata.SessionID, name: choice.Metadata.Name, depth: choice.Depth, selected: selected})
	}
	return threads
}

func (model *uiModel) selectSidebarThread(id string) (tea.Model, tea.Cmd) {
	if model.state == nil {
		return model, nil
	}
	if id == model.state.Current.SessionID {
		return model, model.syncThreadQuestion()
	}
	command, err := model.switchSession(id)
	if err != nil {
		model.message = err.Error()
		return model, nil
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

// handleSidebarClick routes session navigation before transcript clicks.
func (model *uiModel) handleSidebarClick(mouse tea.MouseClickMsg) (tea.Cmd, bool) {
	if model.powerBar != nil || model.rename != nil || model.settings != nil || model.contextReport != nil || model.showQuestion || model.resumePopup != nil || model.sessionMenu != nil {
		return nil, false
	}
	if model.runtime != nil && model.runtime.options.Approvals != nil && model.runtime.options.Approvals.Pending() != nil {
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
		model.sessionMenu = &sessionContextMenu{id: id, row: mouse.Y}
		return nil, true
	}
	if mouse.Button == tea.MouseLeft {
		_, command := model.selectSidebarThread(id)
		return command, true
	}
	return nil, true
}
