package repl

import "github.com/google/uuid"

// workflowPanels includes the selected legacy fixture without duplicating it.
func (model *uiModel) workflowPanels() []*workflowPanel {
	panels := make([]*workflowPanel, 0, len(model.workflows)+1)
	seen := map[*workflowPanel]bool{}
	for _, panel := range append(append([]*workflowPanel(nil), model.workflows...), model.workflow) {
		if panel != nil && !seen[panel] {
			panels = append(panels, panel)
			seen[panel] = true
		}
	}
	return panels
}
func (model *uiModel) hasWorkflowPanel(panel *workflowPanel) bool {
	if panel == nil {
		return false
	}
	for _, candidate := range model.workflowPanels() {
		if candidate == panel {
			return true
		}
	}
	return false
}
func (model *uiModel) registerWorkflowPanel(panel *workflowPanel) {
	if panel == nil {
		return
	}
	panels := model.workflowPanels()
	if !model.hasWorkflowPanel(panel) {
		panels = append(panels, panel)
	}
	for _, candidate := range panels {
		candidate.visible = candidate == panel
		if candidate.threadID == "" {
			candidate.threadID = uuid.NewString()
		}
	}
	model.workflows = panels
	model.workflow = panel
	model.question = nil
	model.showQuestion = false
	model.dismissQuestion = false
}
func (model *uiModel) workflowRunIDs() []string {
	var ids []string
	for _, panel := range model.workflowPanels() {
		if panel.runID != "" {
			ids = append(ids, panel.runID)
		}
	}
	return ids
}
