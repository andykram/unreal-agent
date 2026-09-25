package repl

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWorkflowOverlaysRetainFullHeightRailAndGeometry(t *testing.T) {
	model := recoveryViewFixture(t)
	request := &approvalRequest{}
	model.workflow.executor = &replWorkflowExecutor{approvals: &approvalGate{pending: []*approvalRequest{request}}}
	for _, render := range []func() tea.View{model.workflowApprovalView, model.workflowRecoveryView, func() tea.View { return model.approvalView(request) }} {
		for _, width := range []int{1, 2, 20, 60, 120, 160} {
			for _, height := range []int{1, 5, 24} {
				model.width, model.height = width, height
				view := render()
				if model.width != width {
					t.Fatal("overlay changed global width")
				}
				lines := strings.Split(view.Content, "\n")
				if len(lines) != height {
					t.Fatalf("%dx%d has %d rows", width, height, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("%dx%d overflow %q", width, height, line)
					}
				}
				if width >= 60 && !strings.Contains(view.Content, "UNREAL") {
					t.Fatal("missing sidebar")
				}
				if !view.AltScreen || view.MouseMode != tea.MouseModeCellMotion {
					t.Fatal("overlay navigation disabled")
				}
			}
		}
	}
}
