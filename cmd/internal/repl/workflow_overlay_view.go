package repl

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// workflowOverlayView reserves the shared navigation rail without changing the
// terminal width or translating the content area's cursor and mouse row maps.
func (model *uiModel) workflowOverlayView(render func(*uiModel) tea.View) tea.View {
	contentModel := *model
	sidebarWidth := model.sidebarSize()
	contentModel.width = max(1, model.width-sidebarWidth)
	view := render(&contentModel)
	lines := strings.Split(view.Content, "\n")
	// The rail stays full height even when a permission form has few lines.
	for len(lines) < max(1, model.height) {
		lines = append(lines, "")
	}
	if len(lines) > max(1, model.height) {
		lines = lines[:max(1, model.height)]
	}
	if sidebarWidth > 0 {
		lines = model.withForkSidebar(lines, sidebarWidth)
	}
	view.Content = strings.Join(lines, "\n")
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}
