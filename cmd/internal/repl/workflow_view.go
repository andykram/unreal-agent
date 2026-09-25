package repl

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

// workflowStepOrder preserves source order among available steps while placing
// prerequisites first. Selection and mouse maps always use original graph indexes.
func workflowStepOrder(graph workflow.Graph) []int {
	order := make([]int, 0, len(graph.Steps))
	emitted := map[string]bool{}
	for len(order) < len(graph.Steps) {
		before := len(order)
		for index, step := range graph.Steps {
			if emitted[step.ID] {
				continue
			}
			available := true
			for _, dependency := range step.Needs {
				available = available && emitted[dependency]
			}
			if available {
				order = append(order, index)
				emitted[step.ID] = true
			}
		}
		if len(order) == before {
			// Validation owns malformed graphs. Still render them for diagnosis.
			for index, step := range graph.Steps {
				if !emitted[step.ID] {
					order = append(order, index)
				}
			}
			break
		}
	}
	return order
}

func workflowFlat(text string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(sanitizeTerminal(text))
}

func workflowNodeLabel(step workflow.Step, state workflow.State, executing bool) (string, string) {
	node := state[step.ID]
	switch node.Status {
	case "completed":
		if node.Outcome == "failed" {
			return "!", "completed · failed check"
		}
		return "✓", "completed"
	case "skipped":
		return "−", "skipped"
	case "failed":
		return "×", "failed"
	case "running":
		if node.DispatchStarted && !executing {
			return "!", "reconciliation needed"
		}
		return "●", "running"
	case "":
		if workflow.Ready(step, state) {
			if step.Kind == "approval" {
				return "◇", "approval needed"
			}
			return "○", "ready"
		}
		return "·", "blocked"
	default:
		return "·", workflowFlat(node.Status)
	}
}

func (model *uiModel) workflowView() tea.View {
	if model.workflow.recovery != nil {
		return model.workflowRecoveryView()
	}
	sidebarWidth := model.sidebarSize()
	if sidebarWidth == 0 {
		return model.workflowGraphView()
	}
	contentModel := *model
	contentModel.width = max(1, model.width-sidebarWidth)
	view := contentModel.workflowGraphView()
	view.Content = strings.Join(model.withForkSidebar(strings.Split(view.Content, "\n"), sidebarWidth), "\n")
	return view
}

func (model *uiModel) workflowGraphView() tea.View {
	panel := model.workflow
	if panel.recovery != nil {
		return model.workflowRecoveryView()
	}
	width, height := max(1, model.width), max(1, model.height)
	palette := model.palette()
	panel.rows = map[int]int{}
	panel.graphWidth = width
	mode := workflowFlat(panel.mode)
	if mode == "" {
		mode = "Execution mode not selected"
	}
	lines := []string{model.ink("◆ WORKFLOW · "+mode+"  /  "+workflowFlat(panel.graph.Name), palette.lilac, true)}
	completed := 0
	for _, step := range panel.graph.Steps {
		if panel.state[step.ID].Status == "completed" || panel.state[step.ID].Status == "skipped" {
			completed++
		}
	}
	activity := "Paused"
	if panel.auto {
		activity = "Running"
	}
	if len(panel.graph.Steps) > 0 && completed == len(panel.graph.Steps) {
		activity = "Completed"
	}
	metadata := fmt.Sprintf("%s · %d/%d settled · revision %d · %s", activity, completed, len(panel.graph.Steps), panel.revision, workflowFlat(panel.runID))
	if panel.loading {
		frames := []string{"◐", "◓", "◑", "◒"}
		metadata = frames[max(0, panel.frame)%len(frames)] + " " + workflowFlat(panel.stage)
		if strings.TrimSpace(panel.stage) == "" {
			metadata += "Loading workflow"
		}
	}
	if panel.err != "" {
		metadata = "! " + workflowFlat(panel.err)
	}
	lines = append(lines, model.ink(metadata, palette.muted, false))
	footer := "↑↓ select · PgUp/PgDn graph · ←→ details · n step · Space run/pause · a approve · r recover · d raw/details · p full/task prompt · Esc close"
	bodyHeight := max(0, height-4)
	wide := width >= 96
	graphWidth, graphHeight := width, bodyHeight
	detailWidth, detailHeight := width, 0
	if wide {
		graphWidth = min(60, width*45/100)
		detailWidth, detailHeight = width-graphWidth-3, bodyHeight
	} else if bodyHeight >= 6 {
		graphHeight = max(3, bodyHeight/2)
		detailHeight = bodyHeight - graphHeight - 1
	}
	panel.graphWidth = graphWidth
	order := workflowStepOrder(panel.graph)
	panel.selected = min(max(0, panel.selected), max(0, len(panel.graph.Steps)-1))
	selectedPosition := 0
	depths := map[string]int{}
	for position, index := range order {
		if index == panel.selected {
			selectedPosition = position
		}
		step := panel.graph.Steps[index]
		for _, dependency := range step.Needs {
			depths[step.ID] = max(depths[step.ID], depths[dependency]+1)
		}
	}
	panel.top = min(max(0, panel.top), max(0, len(order)-graphHeight))
	if graphHeight > 0 {
		if selectedPosition < panel.top {
			panel.top = selectedPosition
		}
		if selectedPosition >= panel.top+graphHeight {
			panel.top = selectedPosition - graphHeight + 1
		}
	}
	graphLines := make([]string, graphHeight)
	for row := 0; row < graphHeight && panel.top+row < len(order); row++ {
		index := order[panel.top+row]
		step := panel.graph.Steps[index]
		glyph, status := workflowNodeLabel(step, panel.state, panel.loading)
		prefix := "  "
		if index == panel.selected {
			prefix = "› "
		}
		indent := strings.Repeat("  ", min(depths[step.ID], 4))
		if depths[step.ID] > 0 {
			indent += "└ "
		}
		line := prefix + indent + glyph + " " + workflowFlat(step.ID) + "  · " + status
		line = ansi.Truncate(line, graphWidth, "…")
		color := palette.text
		if status == "blocked" || status == "skipped" {
			color = palette.muted
		} else if status == "approval needed" || status == "reconciliation needed" || status == "failed" || panel.state[step.ID].Outcome == "failed" {
			color = palette.accent
		} else if status == "running" {
			color = palette.lilac
		}
		if index == panel.selected && !model.noColor {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.lilac)).Background(lipgloss.Color(palette.chip)).Bold(true).Render(line)
		} else {
			line = model.ink(line, color, index == panel.selected)
		}
		graphLines[row] = line
		panel.rows[3+row] = index
	}
	if len(order) == 0 && graphHeight > 0 {
		graphLines[0] = model.ink("Choose a Python workflow with /workflow.", palette.muted, false)
	}
	details := model.workflowDetails(detailWidth)
	panel.detailTop = min(max(0, panel.detailTop), max(0, len(details)-max(1, detailHeight)))
	if wide {
		lines = append(lines, model.ink(workflowPad("DEPENDENCY DEPTH", graphWidth)+" │ STEP DETAILS", palette.rule, false))
		for row := 0; row < bodyHeight; row++ {
			detail := ""
			if panel.detailTop+row < len(details) {
				detail = details[panel.detailTop+row]
			}
			lines = append(lines, workflowPad(graphLines[row], graphWidth)+model.ink(" │ ", palette.rule, false)+detail)
		}
	} else {
		lines = append(lines, model.ink("DEPENDENCY DEPTH", palette.rule, false))
		lines = append(lines, graphLines...)
		if detailHeight > 0 {
			lines = append(lines, model.ink("─ STEP DETAILS ─", palette.rule, false))
			for row := 0; row < detailHeight; row++ {
				line := ""
				if panel.detailTop+row < len(details) {
					line = details[panel.detailTop+row]
				}
				lines = append(lines, line)
			}
		}
	}
	if len(lines) >= height {
		lines = lines[:height]
	} else {
		for len(lines) < height-1 {
			lines = append(lines, "")
		}
		lines = append(lines, model.ink(footer, palette.muted, false))
	}
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = ansi.Truncate("Unreal · Workflow · "+workflowFlat(panel.graph.Name), 160, "…")
	return view
}

func workflowPad(value string, width int) string {
	value = ansi.Truncate(value, width, "…")
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}
