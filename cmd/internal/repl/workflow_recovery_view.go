package repl

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
)

var workflowRecoveryActions = []string{"Check saved evidence", "Record verified result", "Mark failed", "Retry this attempt"}

// workflowRecoveryView presents evidence separately from decisions. Rendering
// updates only viewport/hit-test state; it never changes a durable workflow node.
func (model *uiModel) workflowRecoveryView() tea.View {
	return model.workflowOverlayView((*uiModel).workflowRecoveryContentView)
}
func (model *uiModel) workflowRecoveryContentView() tea.View {
	panel, palette := model.workflow, model.palette()
	recovery := panel.recovery
	width, height := max(1, model.width), max(1, model.height)
	recovery.rows = map[int]int{}
	node := panel.state[recovery.stepID]
	lines := []string{
		model.ink("◇ Recover interrupted step", palette.lilac, true),
		model.ink(workflowFlat(recovery.stepID)+" · outcome unconfirmed · execution paused", palette.accent, false),
		model.ink(fmt.Sprintf("Run %s · revision %d", workflowFlat(panel.runID), panel.revision), palette.muted, false),
	}
	footer := "↑↓ action · Enter open · R check evidence · Tab cycle · PgUp/PgDn evidence · Esc back"
	bodyHeight := max(0, height-5)
	var cursor *tea.Cursor
	wrap := func(text string, columns int) []string {
		return strings.Split(ansi.Hardwrap(strings.ReplaceAll(sanitizeTerminal(text), "\t", "  "), max(1, columns), true), "\n")
	}
	editorLines := func(buffer *editor.Buffer, label string, rows int, active bool) []string {
		if rows <= 0 {
			return nil
		}
		color := palette.muted
		if active {
			color = palette.lilac
		}
		result := []string{model.ink(label, color, active)}
		if rows == 1 {
			return result
		}
		projection := editor.Project(buffer.Source(), buffer.Cursor(), max(1, width-2), false)
		start := max(0, projection.CursorRow-(rows-2))
		for row := 0; row < rows-1; row++ {
			value := ""
			if start+row < len(projection.Lines) {
				value = projection.Lines[start+row]
			}
			result = append(result, "  "+value)
		}
		if active {
			cursor = tea.NewCursor(min(width-1, 2+projection.CursorColumn), len(lines)+1+projection.CursorRow-start)
		}
		return result
	}
	action := min(max(0, recovery.action), len(workflowRecoveryActions)-1)
	switch recovery.stage {
	case "edit":
		footer = "Tab field · Ctrl/Alt+Enter review · Enter: result newline / reason review · Esc back"
		lines = append(lines, model.ink(workflowRecoveryActions[action], palette.lilac, true))
		available := max(0, bodyHeight-1)
		if action == 1 {
			resultRows := max(0, available*2/3)
			lines = append(lines, editorLines(&recovery.result, "RESULT JSON  {output, workspace, exit_code}", resultRows, recovery.focus == 0)...)
			lines = append(lines, editorLines(&recovery.note, "REASON  required · explain how you verified this", available-resultRows, recovery.focus == 1)...)
		} else {
			lines = append(lines, editorLines(&recovery.note, "REASON  required · describe the evidence for this decision", available, true)...)
		}
	case "confirm":
		footer = "Enter confirm · Esc back to edit"
		summary := []string{workflowRecoveryActions[action], "Confirm the prior executor has stopped, including in other processes.", "Reason: " + workflowFlat(recovery.note.Source())}
		if action == 1 {
			summary = append(summary, "Verified result: "+workflowFlat(recovery.result.Source()))
		}
		if action == 2 {
			summary = append(summary, "This records a failed step. Dependent work remains blocked.")
		}
		if action == 3 {
			summary = append(summary, "The previous operation may already have taken effect.", "Retry can repeat external effects. The same key does not guarantee deduplication.")
		}
		maxSummary := bodyHeight
		if action == 3 {
			maxSummary = max(0, bodyHeight-3)
		}
		var summaryLines []string
		for _, line := range summary {
			summaryLines = append(summaryLines, wrap(line, width)...)
		}
		if len(summaryLines) > maxSummary {
			summaryLines = summaryLines[:maxSummary]
		}
		lines = append(lines, summaryLines...)
		if action == 3 {
			lines = append(lines, editorLines(&recovery.confirm, "Type retry "+workflowFlat(recovery.stepID), bodyHeight-len(summaryLines), true)...)
		}
	default:
		descriptions := []string{"Read saved results; no operation is repeated.", "Use a result you independently verified.", "Record failure and keep dependent work blocked.", "Run again; external effects may be repeated."}
		var actions []string
		actionRows := map[int]int{}
		for index, title := range workflowRecoveryActions {
			prefix := "  "
			if index == action {
				prefix = "› "
			}
			line := prefix + title
			if index == action && !model.noColor {
				line = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.lilac)).Background(lipgloss.Color(palette.chip)).Bold(true).Render(line)
			}
			actionRows[len(actions)] = index
			actions = append(actions, line, model.ink("  "+descriptions[index], palette.muted, false))
		}
		// Keep the selected decision visible even when the terminal is short.
		actionStart := max(0, action*2-max(0, bodyHeight-1))
		if bodyHeight >= len(actions) {
			actionStart = 0
		}
		if actionStart > 0 {
			actions = actions[actionStart:]
			shifted := map[int]int{}
			for row, index := range actionRows {
				if row >= actionStart {
					shifted[row-actionStart] = index
				}
			}
			actionRows = shifted
		}
		evidenceWidth := width
		wide := width >= 100
		if wide {
			evidenceWidth = width * 55 / 100
		}
		evidence := []string{"SAVED EVIDENCE", "Phase: " + workflowFlat(node.Phase), "Workspace: " + workflowFlat(node.Workspace), "Attempt: " + workflowFlat(node.AttemptKey), "External key: " + workflowFlat(node.ExternalKey)}
		if recovery.probing {
			evidence = append(evidence, "Checking saved evidence…")
		}
		if recovery.evidence != "" {
			evidence = append(evidence, "", recovery.evidence)
		}
		appendJSON := func(label string, value any) {
			if value == nil {
				return
			}
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return
			}
			text := string(data)
			if len([]rune(text)) > 16000 {
				text = string([]rune(text)[:16000]) + "\n… preview truncated"
			}
			evidence = append(evidence, "", label, text)
		}
		appendJSON("INPUTS", node.Inputs)
		appendJSON("OUTPUT", node.Output)
		if len(node.History) > 0 {
			evidence = append(evidence, "", "HISTORY", strings.Join(node.History, "\n"))
		}
		var evidenceLines []string
		for _, line := range evidence {
			evidenceLines = append(evidenceLines, wrap(line, evidenceWidth)...)
		}
		evidenceHeight := bodyHeight
		if !wide {
			evidenceHeight = max(0, bodyHeight-min(len(actions), bodyHeight))
		}
		recovery.top = min(max(0, recovery.top), max(0, len(evidenceLines)-max(1, evidenceHeight)))
		for row := 0; row < bodyHeight; row++ {
			value := ""
			if wide {
				if recovery.top+row < len(evidenceLines) {
					value = evidenceLines[recovery.top+row]
				}
				right := ""
				if row < len(actions) {
					right = actions[row]
				}
				value = workflowPad(value, evidenceWidth) + model.ink(" │ ", palette.rule, false) + right
				if index, ok := actionRows[row]; ok {
					recovery.rows[len(lines)] = index
				}
			} else if row < len(actions) {
				value = actions[row]
				if index, ok := actionRows[row]; ok {
					recovery.rows[len(lines)] = index
				}
			} else if position := recovery.top + row - len(actions); position < len(evidenceLines) {
				value = evidenceLines[position]
			}
			lines = append(lines, value)
		}
	}
	if len(lines) > max(0, height-2) {
		lines = lines[:max(0, height-2)]
	}
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	if height >= 2 {
		notice := "Before any decision, ensure the prior executor has stopped in every process."
		if recovery.err != "" {
			notice = "! " + workflowFlat(recovery.err)
		}
		lines = append(lines, model.ink(notice, palette.accent, false))
	}
	lines = append(lines, model.ink(footer, palette.muted, false))
	if len(lines) > height {
		lines = lines[:height]
	}
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	for row := range recovery.rows {
		if row >= height-2 {
			delete(recovery.rows, row)
		}
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "Unreal · Workflow recovery"
	if cursor != nil && cursor.Y >= 3 && cursor.Y < height-2 {
		view.Cursor = cursor
	}
	return view
}
