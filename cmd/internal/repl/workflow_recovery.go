package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

type workflowRecovery struct {
	stepID        string
	action, top   int
	stage         string
	note, result  editor.Buffer
	focus         int
	confirm       editor.Buffer
	err, evidence string
	probing       bool
	candidate     *workflow.ExecutionResult
	rows          map[int]int
	cancel        context.CancelFunc
}
type workflowEvidenceMsg struct {
	panel     *workflowPanel
	recovery  *workflowRecovery
	evidence  string
	candidate *workflow.ExecutionResult
	err       error
}
type workflowReconciledMsg struct {
	panel    *workflowPanel
	recovery *workflowRecovery
	result   workflowResultMsg
	action   string
}

type recoveryResultEnvelope struct {
	Output    any    `json:"output"`
	Workspace string `json:"workspace,omitempty"`
	ExitCode  int    `json:"exit_code"`
}

func (model *uiModel) openWorkflowRecovery() (tea.Model, tea.Cmd) {
	panel := model.workflow
	if panel == nil || panel.loading || panel.runID == "" {
		return model, nil
	}
	if len(panel.graph.Steps) == 0 {
		return model, nil
	}
	panel.selected = max(0, min(panel.selected, len(panel.graph.Steps)-1))
	selected := panel.graph.Steps[panel.selected].ID
	node := panel.state[selected]
	if node.Status != "running" || !node.DispatchStarted {
		selected = ""
		for i, step := range panel.graph.Steps {
			if n := panel.state[step.ID]; n.Status == "running" && n.DispatchStarted {
				selected = step.ID
				panel.selected = i
				break
			}
		}
		if selected == "" {
			panel.err = "No interrupted step needs recovery."
			return model, nil
		}
	}
	panel.auto = false
	panel.visible = true
	panel.recovery = &workflowRecovery{stepID: selected, stage: "inspect", evidence: "Inspect saved evidence before choosing an outcome. No operation will be retried automatically."}
	return model, model.probeWorkflowRecovery()
}
func (model *uiModel) probeWorkflowRecovery() tea.Cmd {
	panel := model.workflow
	recovery := panel.recovery
	if recovery.probing || panel.executor == nil {
		return nil
	}
	var step workflow.Step
	for _, candidate := range panel.graph.Steps {
		if candidate.ID == recovery.stepID {
			step = candidate
			break
		}
	}
	// Copy the input before launching a background reader.
	data, _ := json.Marshal(workflow.ExecutionRequest{RunID: panel.runID, Step: step, Node: panel.state[step.ID], State: panel.state, ExternalKey: panel.state[step.ID].ExternalKey})
	var request workflow.ExecutionRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		recovery.err = err.Error()
		return nil
	}
	ctx, cancel := context.WithCancel(model.ctx)
	recovery.cancel = cancel
	recovery.probing = true
	recovery.err = ""
	executor := panel.executor
	return func() tea.Msg {
		defer cancel()
		evidence, candidate, err := probeWorkflowEvidence(ctx, executor, request)
		return workflowEvidenceMsg{panel, recovery, evidence, candidate, err}
	}
}
func (model *uiModel) acceptWorkflowEvidence(message workflowEvidenceMsg) (tea.Model, tea.Cmd) {
	if !model.hasWorkflowPanel(message.panel) || message.panel.recovery != message.recovery {
		return model, nil
	}
	recovery := message.recovery
	recovery.probing = false
	recovery.evidence = message.evidence
	recovery.candidate = message.candidate
	if message.err != nil {
		recovery.err = message.err.Error()
	}
	return model, nil
}
func (model *uiModel) selectRecoveryAction() (tea.Model, tea.Cmd) {
	panel := model.workflow
	recovery := panel.recovery
	if panel.loading {
		return model, nil
	}
	recovery.err = ""
	if recovery.action == 0 {
		return model, model.probeWorkflowRecovery()
	}
	recovery.stage = "edit"
	recovery.confirm.Clear()
	recovery.note.Clear()
	recovery.focus = 1
	if recovery.action == 1 {
		recovery.focus = 0
		value := recoveryResultEnvelope{Output: nil}
		if recovery.candidate != nil {
			value = recoveryResultEnvelope{recovery.candidate.Output, recovery.candidate.Workspace, recovery.candidate.ExitCode}
		}
		data, _ := json.MarshalIndent(value, "", "  ")
		_ = recovery.result.Set(string(data))
	}
	return model, nil
}
func (model *uiModel) recoveryDecision() (workflow.Reconciliation, error) {
	recovery := model.workflow.recovery
	reason := strings.TrimSpace(recovery.note.Source())
	if reason == "" {
		return workflow.Reconciliation{}, fmt.Errorf("Add how you verified the outcome, or why this action is safe.")
	}
	decision := workflow.Reconciliation{Reason: reason}
	switch recovery.action {
	case 1:
		decision.Action = "accept"
		raw := recovery.result.Source()
		if !json.Valid([]byte(raw)) {
			return decision, fmt.Errorf("Result must be one JSON object containing output, workspace, and exit_code as applicable.")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil || len(fields) == 0 {
			return decision, fmt.Errorf("Result must be a nonempty JSON object.")
		}
		var kind string
		for _, step := range model.workflow.graph.Steps {
			if step.ID == recovery.stepID {
				kind = step.Kind
				break
			}
		}
		phase := model.workflow.state[recovery.stepID].Phase
		if kind == "command" || kind == "repeat_check" && phase != "repair" {
			if value, ok := fields["exit_code"]; !ok || string(value) == "null" {
				return decision, fmt.Errorf("Provide an explicit integer exit_code for the verified command.")
			}
		}
		if kind == "agent" || kind == "repeat_check" && phase == "repair" {
			if _, ok := fields["output"]; !ok {
				return decision, fmt.Errorf("Provide the verified output explicitly.")
			}
		}
		var envelope recoveryResultEnvelope
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			return decision, err
		}
		decision.Result = workflow.ExecutionResult{Output: envelope.Output, Workspace: envelope.Workspace, ExitCode: envelope.ExitCode}
	case 2:
		decision.Action = "fail"
	case 3:
		decision.Action = "retry"
	default:
		return decision, fmt.Errorf("Choose a recovery action.")
	}
	return decision, nil
}
func (model *uiModel) saveWorkflowRecovery() tea.Cmd {
	panel := model.workflow
	recovery := panel.recovery
	if panel.loading {
		return nil
	}
	decision, err := model.recoveryDecision()
	if err != nil {
		recovery.err = err.Error()
		return nil
	}
	if decision.Action == "retry" && recovery.confirm.Source() != "retry "+recovery.stepID {
		recovery.err = "Type retry " + recovery.stepID + " to authorize another execution with the same key."
		return nil
	}
	panel.loading = true
	panel.auto = false
	panel.stage = "Saving recovery decision"
	runID, revision, directory := panel.runID, panel.revision, panel.directory
	return func() tea.Msg {
		result := workflowResultMsg{panel: panel, runID: runID}
		store, err := workflow.OpenStore(filepath.Join(directory, "workflows.sqlite"))
		if err != nil {
			result.err = err
			return workflowReconciledMsg{panel, recovery, result, decision.Action}
		}
		defer store.Close()
		result.graph, result.state, result.revision, result.err = workflow.Reconcile(store, runID, revision, recovery.stepID, decision)
		return workflowReconciledMsg{panel, recovery, result, decision.Action}
	}
}
func (model *uiModel) acceptWorkflowReconciliation(message workflowReconciledMsg) (tea.Model, tea.Cmd) {
	if !model.hasWorkflowPanel(message.panel) || message.panel.recovery != message.recovery {
		return model, nil
	}
	panel := message.panel
	panel.loading = false
	panel.stage = ""
	if message.result.err != nil {
		message.recovery.err = message.result.err.Error()
		return model, nil
	}
	panel.graph, panel.state, panel.revision = message.result.graph, message.result.state, message.result.revision
	panel.recovery = nil
	panel.auto = false
	panel.err = ""
	if message.action == "retry" {
		panel.err = "Retry authorized and saved. Press N or Space to execute this attempt again."
	}
	if message.action == "accept" {
		panel.err = "Verified result saved. Inspect the graph, then press Space to continue."
	}
	if message.action == "fail" {
		panel.err = "Step marked failed. Dependent work remains blocked."
	}
	return model, nil
}
func (model *uiModel) updateWorkflowRecovery(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := model.workflow
	recovery := panel.recovery
	if panel.loading {
		return model, nil
	}
	if key.String() == "esc" {
		recovery.err = ""
		if recovery.stage == "inspect" {
			if recovery.cancel != nil {
				recovery.cancel()
			}
			panel.recovery = nil
		} else if recovery.stage == "confirm" {
			recovery.stage = "edit"
		} else {
			recovery.stage = "inspect"
		}
		return model, nil
	}
	switch recovery.stage {
	case "inspect":
		switch key.String() {
		case "up", "k":
			recovery.action = max(0, recovery.action-1)
		case "down", "j":
			recovery.action = min(3, recovery.action+1)
		case "tab":
			recovery.action = (recovery.action + 1) % 4
		case "shift+tab":
			recovery.action = (recovery.action + 3) % 4
		case "pgdown", "right":
			recovery.top += max(1, model.height/2)
		case "pgup", "left":
			recovery.top = max(0, recovery.top-max(1, model.height/2))
		case "r":
			return model, model.probeWorkflowRecovery()
		case "enter":
			return model.selectRecoveryAction()
		}
	case "edit":
		if key.String() == "tab" && recovery.action == 1 {
			recovery.focus = 1 - recovery.focus
			return model, nil
		}
		if key.String() == "ctrl+enter" || key.String() == "alt+enter" || key.String() == "enter" && recovery.focus == 1 {
			if _, err := model.recoveryDecision(); err != nil {
				recovery.err = err.Error()
			} else {
				recovery.err = ""
				recovery.stage = "confirm"
				recovery.confirm.Clear()
			}
			return model, nil
		}
		buffer := &recovery.note
		if recovery.focus == 0 {
			buffer = &recovery.result
		}
		editRecoveryBuffer(buffer, key)
	case "confirm":
		if key.String() == "enter" {
			return model, model.saveWorkflowRecovery()
		}
		if recovery.action == 3 {
			editRecoveryBuffer(&recovery.confirm, key)
		}
	}
	return model, nil
}
func editRecoveryBuffer(buffer *editor.Buffer, key tea.KeyPressMsg) {
	if handleReadline(buffer, key) {
		return
	}
	switch key.String() {
	case "left", "ctrl+b":
		buffer.Left()
	case "right", "ctrl+f":
		buffer.Right()
	case "up":
		buffer.Up()
	case "down":
		buffer.Down()
	case "home", "ctrl+a":
		buffer.Home()
	case "end", "ctrl+e":
		buffer.End()
	case "backspace", "ctrl+h":
		buffer.Backspace()
	case "delete":
		buffer.Delete()
	case "enter", "ctrl+j":
		_ = buffer.Insert("\n")
	default:
		if key.Text != "" {
			_ = buffer.Insert(key.Text)
		}
	}
}
func (model *uiModel) pasteWorkflowRecovery(text string) {
	recovery := model.workflow.recovery
	if model.workflow.loading {
		return
	}
	if recovery.stage == "edit" {
		buffer := &recovery.note
		if recovery.focus == 0 {
			buffer = &recovery.result
		}
		_ = buffer.Insert(text)
	} else if recovery.stage == "confirm" && recovery.action == 3 {
		_ = recovery.confirm.Insert(strings.ReplaceAll(strings.ReplaceAll(text, "\n", ""), "\r", ""))
	}
}
