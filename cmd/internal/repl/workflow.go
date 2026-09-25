package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

type workflowPanel struct {
	fullPrompt                           bool
	prompts                              map[string]workflowPromptSnapshot
	approvalRows                         map[int]string
	threadID                             string
	rawDetails                           bool
	approvalChoice                       bool
	recovery                             *workflowRecovery
	graph                                workflow.Graph
	state                                workflow.State
	runID                                string
	revision                             int64
	selected, top, detailTop, graphWidth int
	loading, visible, auto               bool
	err, mode, stage                     string
	frame                                int
	rows                                 map[int]int
	directory                            string
	cancel                               context.CancelFunc
	executor                             *replWorkflowExecutor
	agent                                *Runtime
	ticking                              bool
}
type workflowResultMsg struct {
	prompts  map[string]workflowPromptSnapshot
	panel    *workflowPanel
	graph    workflow.Graph
	state    workflow.State
	runID    string
	revision int64
	err      error
	initial  bool
}
type workflowTickMsg struct{ panel *workflowPanel }

func (model *uiModel) runWorkflow(argument, raw string) (tea.Model, tea.Cmd) {
	colonPath := strings.HasPrefix(raw, "/workflow:")
	if argument == "" {
		if model.workflow != nil && !colonPath {
			model.workflow.visible = true
			model.draft.Clear()
			return model, nil
		}
		_ = model.draft.Set("/workflow ")
		model.suppressCompletion = false
		model.refreshCompletion()
		model.message = "Choose a Python workflow, or use /workflow resume <run-id>."
		return model, nil
	}
	if !colonPath && strings.HasPrefix(argument, "resume ") {
		id := strings.TrimSpace(strings.TrimPrefix(argument, "resume "))
		for _, existing := range model.workflowPanels() {
			if existing.runID == id && id != "" {
				model.registerWorkflowPanel(existing)
				existing.visible = true
				model.draft.Clear()
				model.completion = nil
				return model, nil
			}
		}
	}
	if model.busy || model.runtime != nil && model.runtime.QueueLength() > 0 {
		model.message = "Wait for the current agent task before starting a workflow."
		return model, nil
	}
	if model.config.Current().Mode == "plan" {
		model.message = "Switch out of plan mode before executing a workflow."
		return model, nil
	}
	var script, resume string
	var err error
	if !colonPath && strings.HasPrefix(argument, "resume ") {
		resume = strings.TrimSpace(strings.TrimPrefix(argument, "resume "))
		if resume == "" || strings.ContainsAny(resume, " /\\\t\n") {
			model.message = "Usage: /workflow resume <run-id>"
			return model, nil
		}
	} else {
		if colonPath {
			var path string
			path, err = workflowArgument(argument)
			if err == nil {
				if !filepath.IsAbs(path) {
					path = filepath.Join(model.state.Workspace, path)
				}
				script, err = resolveWorkflowScript(model.state.Workspace, path)
			}
		} else {
			script, err = resolveWorkflowScript(model.state.Workspace, argument)
		}
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
	}
	cache, err := workflowCacheDirectory(model.getenv)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	stateRoot, err := workflowStateDirectory(model.getenv)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	ctx, cancel := context.WithCancel(model.ctx)
	panel := &workflowPanel{visible: true, loading: true, mode: "Live execution", auto: true, stage: "Compiling Python workflow", runID: resume, directory: stateRoot, cancel: cancel}
	if resume != "" {
		panel.stage = "Restoring saved workflow"
	}
	settings := &ConfigStore{active: cloneConfig(model.config.Current()), getenv: model.getenv, path: model.config.Path()}
	panel.executor = &replWorkflowExecutor{workspace: model.state.Workspace, directory: stateRoot, config: settings, getenv: model.getenv, heartbeat: model.heartbeat, worktrees: worktrunkWorkflowBackend{}, notices: make(chan workflowAgentNotice, 16), approvals: &approvalGate{config: settings, ctx: model.ctx, workflow: true}}
	model.registerWorkflowPanel(panel)
	openedRunIDs := model.workflowRunIDs()
	model.draft.Clear()
	model.completion = nil
	return model, tea.Batch(workflowTick(panel), func() tea.Msg {
		defer cancel()
		result := workflowResultMsg{panel: panel, initial: true}
		if resume == "" {
			runtime, err := workflow.EnsureRuntime(ctx, cache)
			if err != nil {
				result.err = err
				return result
			}
			skillsModule := ""
			candidate := filepath.Join(filepath.Dir(script), "generated_skills.py")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				skillsModule = candidate
			}
			result.graph, result.err = workflow.Compile(ctx, script, runtime, skillsModule)
			result.graph.ExecutionMode = "live"
			result.graph.Workspace = panel.executor.workspace
			result.graph.ExecutionConfig, _ = json.Marshal(settings.Current())
			if result.err != nil {
				return result
			}
		}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		store, err := workflow.OpenStore(filepath.Join(stateRoot, "workflows.sqlite"))
		if err != nil {
			result.err = err
			return result
		}
		defer store.Close()
		if resume != "" {
			result.runID = resume
			result.graph, result.state, result.revision, result.err = store.Load(resume)
			if result.err == nil && result.graph.ExecutionMode != "live" {
				result.err = errors.New("this run belongs to the simulator; resume it with workflow-prototype")
			}
		} else {
			if err := preflightWorkflow(ctx, result.graph, panel.executor); err != nil {
				result.err = err
				return result
			}
			result.state = workflow.State{}
			result.runID, result.revision, result.err = store.Create(result.graph, result.state)
		}
		if result.err == nil && resume != "" {
			if result.graph.Workspace != panel.executor.workspace {
				result.err = fmt.Errorf("resume this run from its original workspace: %s", result.graph.Workspace)
				return result
			}
			var pinned Config
			if len(result.graph.ExecutionConfig) == 0 {
				result.err = errors.New("live run has no saved execution configuration")
				return result
			}
			if err := json.Unmarshal(result.graph.ExecutionConfig, &pinned); err != nil {
				result.err = err
				return result
			}
			panel.executor.config = &ConfigStore{active: pinned, getenv: model.getenv, path: model.config.Path()}
		}
		if result.err == nil {
			result.prompts = loadWorkflowPromptSnapshots(stateRoot, result.runID, result.state)
			_, result.err = store.Cleanup(7*24*time.Hour, 100, append(openedRunIDs, result.runID)...)
			if result.err == nil {
				_, result.err = cleanupWorkflowReceipts(stateRoot, store, result.runID, 7*24*time.Hour, 100)
			}
		}
		return result
	})
}
func workflowCacheDirectory(getenv func(string) string) (string, error) {
	root := getenv("XDG_CACHE_HOME")
	if root == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("set HOME or XDG_CACHE_HOME for the Python runtime")
		}
		root = filepath.Join(home, ".cache")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("XDG_CACHE_HOME must be absolute")
	}
	return filepath.Join(root, "unreal-agent", "workflow-prototype"), nil
}
func workflowStateDirectory(getenv func(string) string) (string, error) {
	root := getenv("XDG_STATE_HOME")
	if root == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("set HOME or XDG_STATE_HOME for workflow state")
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(root, "unreal-agent", "workflows"), nil
}
func workflowTick(panel *workflowPanel) tea.Cmd {
	if panel.ticking {
		return nil
	}
	panel.ticking = true
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return workflowTickMsg{panel} })
}

func (model *uiModel) acceptWorkflowResult(result workflowResultMsg) (tea.Model, tea.Cmd) {
	if !model.hasWorkflowPanel(result.panel) {
		return model, nil
	}
	panel := result.panel
	panel.loading = false
	if result.prompts != nil {
		panel.prompts = result.prompts
	}
	panel.agent = nil
	if panel.executor != nil {
		for len(panel.executor.notices) > 0 {
			retainWorkflowPrompt(panel, <-panel.executor.notices)
		}
	}
	panel.stage = ""
	if result.initial && result.err != nil {
		panel.err = result.err.Error()
		panel.auto = false
		panel.runID = ""
		return model, nil
	}
	if result.runID != "" && result.state != nil {
		panel.graph, panel.state, panel.runID, panel.revision = result.graph, result.state, result.runID, result.revision
	}
	if result.err != nil {
		panel.err = result.err.Error()
		panel.auto = false
		for _, node := range panel.state {
			if node.Status == "running" && node.DispatchStarted {
				if panel == model.workflow && panel.visible {
					return model.openWorkflowRecovery()
				}
				return model, nil
			}
		}
		return model, nil
	}

	panel.err = ""
	for _, node := range panel.state {
		if node.Status == "running" && node.DispatchStarted {
			panel.auto = false
			panel.err = workflow.ErrReconciliationRequired.Error()
			if panel == model.workflow && panel.visible {
				return model.openWorkflowRecovery()
			}
			return model, nil
		}
	}
	if result.initial && panel.executor != nil && workflowCanAdvance(panel) {
		panel.approvalChoice = true
		panel.auto = false
		return model, nil
	}
	panel.selected = min(panel.selected, max(0, len(panel.graph.Steps)-1))
	if panel.auto {
		return model, workflowTick(panel)
	}
	return model, nil
}

// The runner commits the dispatch intent and its outcome before publishing them.
func (model *uiModel) workflowAction(action, payload string) tea.Cmd {
	return model.workflowActionFor(model.workflow, action, payload)
}

func (model *uiModel) workflowActionFor(panel *workflowPanel, action, payload string) tea.Cmd {
	if !model.hasWorkflowPanel(panel) || panel.loading || panel.runID == "" {
		return nil
	}
	if model.busy || model.runtime != nil && (model.runtime.IsBusy() || model.runtime.QueueLength() > 0) {
		panel.auto = false
		panel.err = "Wait for the current agent task before continuing this workflow."
		return nil
	}
	selected := ""
	if len(panel.graph.Steps) > 0 {
		selected = panel.graph.Steps[panel.selected].ID
	}
	runID, revision, directory := panel.runID, panel.revision, panel.directory
	ctx, cancel := context.WithCancel(model.ctx)
	panel.cancel = cancel
	panel.loading = true
	panel.stage = "Executing workflow step"
	executor := panel.executor
	return tea.Batch(workflowTick(panel), func() tea.Msg {
		defer cancel()
		result := workflowResultMsg{panel: panel}
		store, err := workflow.OpenStore(filepath.Join(directory, "workflows.sqlite"))
		if err != nil {
			result.err = err
			return result
		}
		defer store.Close()
		if action == "approve" {
			result.graph, result.state, result.revision, result.err = workflow.Approve(store, runID, revision, selected)
		} else {
			result.graph, result.state, result.revision, result.err = workflow.Next(ctx, store, runID, revision, executor)
		}
		result.runID = runID
		return result
	})
}

func (model *uiModel) updateWorkflow(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := model.workflow
	if panel.recovery != nil {
		return model.updateWorkflowRecovery(key)
	}

	switch key.String() {
	case "p", "P", "shift+p", "shift+P":
		panel.fullPrompt = !panel.fullPrompt
		panel.detailTop = 0
	case "d":
		panel.rawDetails = !panel.rawDetails
		panel.detailTop = 0
	case "esc", "q":
		panel.visible = false
		panel.auto = false
		model.message = "Workflow paused. Use /workflow to reopen."
		if panel.loading {
			model.message = "Current workflow step continues. Use /workflow to reopen or Ctrl+C to stop."
		}
	case "down", "j":
		model.moveWorkflowSelection(1)
	case "up", "k":
		model.moveWorkflowSelection(-1)
	case "pgdown":
		model.moveWorkflowSelection(max(1, model.height-8))
	case "pgup":
		model.moveWorkflowSelection(-max(1, model.height-8))
	case "right":
		panel.detailTop++
	case "left":
		panel.detailTop = max(0, panel.detailTop-1)
	case "space":
		panel.auto = !panel.auto
		if panel.auto {
			return model, workflowTick(panel)
		}
	case "n":
		panel.auto = false
		return model, model.workflowAction("next", "")
	case "r":
		return model.openWorkflowRecovery()
	case "a":
		panel.auto = true
		return model, model.workflowAction("approve", "")

	}
	return model, nil
}

func workflowCanAdvance(panel *workflowPanel) bool {
	for _, step := range panel.graph.Steps {
		node := panel.state[step.ID]
		if step.Kind == "approval" {
			continue
		}
		if workflow.Ready(step, panel.state) || node.Status == "running" && !node.DispatchStarted {
			return true
		}
	}
	return false
}
func (model *uiModel) closeWorkflow() {
	for _, panel := range model.workflowPanels() {
		if panel.recovery != nil && panel.recovery.cancel != nil {
			panel.recovery.cancel()
		}
		if panel.cancel != nil {
			panel.cancel()
		}
	}
}

func (model *uiModel) interactionRuntime() *Runtime {
	if model.workflow != nil && model.workflow.visible && model.workflow.agent != nil {
		return model.workflow.agent
	}
	return model.runtime
}
func (model *uiModel) moveWorkflowSelection(delta int) {
	panel := model.workflow
	order := workflowStepOrder(panel.graph)
	if len(order) == 0 {
		return
	}
	at := 0
	for i, index := range order {
		if index == panel.selected {
			at = i
			break
		}
	}
	panel.selected = order[min(len(order)-1, max(0, at+delta))]
	panel.detailTop = 0
}
func (model *uiModel) advanceWorkflowTick(tick workflowTickMsg) (tea.Model, tea.Cmd) {
	panel := tick.panel
	if !model.hasWorkflowPanel(panel) {
		return model, nil
	}
	panel.ticking = false
	panel.frame = (panel.frame + 1) % len(activityFrames)
	if panel.executor != nil {
		for {
			select {
			case notice := <-panel.executor.notices:
				retainWorkflowPrompt(panel, notice)
				if notice.state != nil {
					panel.state = notice.state
				}
				if notice.runtime != nil {
					panel.agent = notice.runtime
				}
				if notice.finished {
					panel.agent = nil
				}
				if notice.text != "" {
					panel.stage = notice.text
				}
			default:
				goto drained
			}
		}
	}
drained:
	var question tea.Cmd
	if panel == model.workflow && panel.visible {
		question = model.syncQuestion()
	}
	if panel.loading {
		return model, tea.Batch(question, workflowTick(panel))
	}
	if panel.auto {
		if workflowCanAdvance(panel) {
			return model, tea.Batch(question, model.workflowActionFor(panel, "next", ""))
		}
		panel.auto = false
	}
	return model, question
}

func preflightWorkflow(ctx context.Context, graph workflow.Graph, executor *replWorkflowExecutor) error {
	byID := map[string]workflow.Step{}
	for _, step := range graph.Steps {
		byID[step.ID] = step
	}
	for _, step := range graph.Steps {
		if step.Kind == "agent" || step.Kind == "command" || step.Kind == "repeat_check" {
			id, _ := step.Spec["workspace"].(string)
			if byID[id].Kind != "worktree" {
				return fmt.Errorf("step %s needs a worktree workspace", step.ID)
			}
			visited := map[string]bool{}
			var depends func(string) bool
			depends = func(node string) bool {
				if node == id {
					return true
				}
				if visited[node] {
					return false
				}
				visited[node] = true
				for _, parent := range byID[node].Needs {
					if depends(parent) {
						return true
					}
				}
				return false
			}
			if !depends(step.ID) {
				return fmt.Errorf("step %s must depend on workspace %s", step.ID, id)
			}
		}
		if step.Kind == "agent" {
			if prompt, _ := step.Spec["prompt"].(string); strings.TrimSpace(prompt) == "" {
				return fmt.Errorf("agent %s needs a prompt", step.ID)
			}
		}
		if step.Kind == "command" || step.Kind == "repeat_check" {
			argv, ok := step.Spec["argv"].([]any)
			if !ok || len(argv) == 0 {
				return fmt.Errorf("command %s needs argv", step.ID)
			}
			for i, arg := range argv {
				value, ok := arg.(string)
				if !ok || i == 0 && value == "" {
					return fmt.Errorf("command %s has invalid argv", step.ID)
				}
			}
		}
	}
	agentNeeded := false
	for _, step := range graph.Steps {
		switch step.Kind {
		case "worktree":
			if backend, _ := step.Spec["backend"].(string); backend != "" && backend != "worktrunk" {
				return fmt.Errorf("unsupported workspace backend %q", backend)
			}
			for _, binary := range []string{"wt", "git"} {
				if _, err := exec.LookPath(binary); err != nil {
					return fmt.Errorf("workflow workspace requires %s on PATH", binary)
				}
			}
			base, _ := step.Spec["base"].(string)
			if base == "" || base == "current" {
				base = "HEAD"
			}
			pin, err := exec.CommandContext(ctx, "git", "-C", executor.workspace, "rev-parse", "--verify", "--end-of-options", base+"^{commit}").Output()
			if err != nil {
				return fmt.Errorf("workflow workspace %s base %q: %w", step.ID, base, err)
			}
			step.Spec["base"] = strings.TrimSpace(string(pin))
		case "agent", "repeat_check":
			agentNeeded = true
		}
	}
	if agentNeeded {
		router, err := NewModelRouter(executor.config, executor.getenv)
		if err != nil {
			return err
		}
		return router.Close()
	}
	return nil
}

func (model *uiModel) interactionApprovalGate() *approvalGate {
	if model.workflow != nil && model.workflow.visible && model.workflow.executor != nil && model.workflow.executor.approvals != nil && model.workflow.executor.approvals.Pending() != nil {
		return model.workflow.executor.approvals
	}
	if runtime := model.interactionRuntime(); runtime != nil {
		return runtime.options.Approvals
	}
	return nil
}

func retainWorkflowPrompt(panel *workflowPanel, notice workflowAgentNotice) {
	if notice.prompt == nil || notice.stepID == "" {
		return
	}
	if panel.prompts == nil {
		panel.prompts = map[string]workflowPromptSnapshot{}
	}
	panel.prompts[notice.stepID] = *notice.prompt
}
