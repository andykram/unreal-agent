package repl

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type planComment struct {
	line int
	text string
}
type planReview struct {
	lines    []string
	selected int
	top      int
	editing  bool
	comment  string
	comments []planComment
}

func (model *uiModel) openPlan() bool {
	for i := len(model.transcript) - 1; i >= 0; i-- {
		if model.transcript[i].kind == "assistant" && strings.TrimSpace(model.transcript[i].text) != "" {
			text := model.transcript[i].text
			model.plan = &planReview{lines: strings.Split(text, "\n")}
			return true
		}
	}
	model.message = "No assistant plan is available yet."
	return false
}

func (model *uiModel) updatePlan(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	plan := model.plan
	if plan.editing {
		switch key.String() {
		case "esc":
			plan.editing = false
			plan.comment = ""
		case "enter":
			if text := strings.TrimSpace(plan.comment); text != "" {
				plan.comments = append(plan.comments, planComment{line: plan.selected, text: text})
			}
			plan.comment = ""
			plan.editing = false
		case "backspace":
			if len(plan.comment) > 0 {
				plan.comment = string([]rune(plan.comment)[:len([]rune(plan.comment))-1])
			}
		default:
			if key.Text != "" {
				plan.comment += key.Text
			}
		}
		return model, nil
	}
	switch key.String() {
	case "esc", "q":
		model.plan = nil
	case "down", "j":
		plan.selected = min(len(plan.lines)-1, plan.selected+1)
	case "up", "k":
		plan.selected = max(0, plan.selected-1)
	case "pgdown":
		plan.selected = min(len(plan.lines)-1, plan.selected+max(1, model.height-5))
	case "pgup":
		plan.selected = max(0, plan.selected-max(1, model.height-5))
	case "c":
		plan.editing = true
	case "r":
		if model.runtime == nil {
			model.message = "Choose a model before requesting a revision."
			return model, nil
		}
		var feedback strings.Builder
		feedback.WriteString("Revise your plan using these comments. Stay in plan mode; do not implement it yet.\n")
		for _, note := range plan.comments {
			fmt.Fprintf(&feedback, "Line %d (%s): %s\n", note.line+1, strings.TrimSpace(plan.lines[note.line]), note.text)
		}
		if len(plan.comments) == 0 {
			feedback.WriteString("Please improve and clarify the plan.\n")
		}
		prompt := feedback.String()
		_, _, err := model.runtime.Submit(context.Background(), prompt, false)
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		model.plan = nil
		model.message = "Revision requested."
	}
	return model, nil
}

func (model *uiModel) planView() tea.View {
	plan := model.plan
	width, height := max(1, model.width), max(1, model.height)
	header := model.ink("◆ UNREAL  /  PLAN REVIEW", model.palette().lilac, true)
	footer := "↑/↓ select  PgUp/PgDn scroll  C comment  R revise  Esc close"
	if plan.editing {
		footer = "Comment: " + plan.comment + "  [Enter save · Esc cancel]"
	}
	visible := max(0, height-4)
	plan.top = min(plan.top, plan.selected)
	// Keep the selected logical line visible even when earlier lines wrap.
	used := 0
	for i := plan.top; i <= plan.selected; i++ {
		used += len(strings.Split(ansi.Hardwrap(sanitizeTerminal(plan.lines[i]), max(1, width-3), true), "\n"))
	}
	for used > visible && plan.top < plan.selected {
		used -= len(strings.Split(ansi.Hardwrap(sanitizeTerminal(plan.lines[plan.top]), max(1, width-3), true), "\n"))
		plan.top++
	}
	lines := []string{ansi.Truncate(header, width, "…"), model.ink(fmt.Sprintf("%d lines  ·  %d comments", len(plan.lines), len(plan.comments)), model.palette().muted, false), ""}
	for i := plan.top; i < len(plan.lines) && len(lines) < height-1; i++ {
		marker := "  "
		if i == plan.selected {
			marker = "› "
		}
		wrapped := strings.Split(ansi.Hardwrap(sanitizeTerminal(plan.lines[i]), max(1, width-3), true), "\n")
		for j, line := range wrapped {
			if len(lines) >= height-1 {
				break
			}
			prefix := marker
			if j > 0 {
				prefix = "  "
			}
			rendered := ansi.Truncate(prefix+line, width, "…")
			if i == plan.selected {
				rendered = model.ink(rendered, model.palette().accent, true)
			}
			lines = append(lines, rendered)
		}
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, ansi.Truncate(footer, width, "…"))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.WindowTitle = "Unreal · Plan review"
	return view
}
