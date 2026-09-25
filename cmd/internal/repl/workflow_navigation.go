package repl

func (model *uiModel) hasRunningWorkflow() bool {
	for _, panel := range model.workflowPanels() {
		if panel.loading || panel.auto {
			return true
		}
	}
	return false
}
