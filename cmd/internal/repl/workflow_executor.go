package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

type workflowAgentNotice struct {
	stepID   string
	prompt   *workflowPromptSnapshot
	runtime  *Runtime
	text     string
	finished bool
	state    workflow.State
}
type replWorkflowExecutor struct {
	workspace, directory string
	config               *ConfigStore
	getenv               func(string) string
	heartbeat            time.Duration
	worktrees            WorkflowWorktreeBackend
	notices              chan workflowAgentNotice
	approvals            *approvalGate
}

func (executor *replWorkflowExecutor) Execute(ctx context.Context, request workflow.ExecutionRequest) (workflow.ExecutionResult, error) {
	fingerprint, err := workflowFingerprint(request, executor.config.Current())
	if err != nil {
		return workflow.ExecutionResult{}, fmt.Errorf("%w: %v", workflow.ErrNotDispatched, err)
	}
	receipt := &workflowReceipt{ExternalKey: request.ExternalKey, Fingerprint: fingerprint, StartedAt: time.Now().UTC()}
	if err = saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
		return workflow.ExecutionResult{}, fmt.Errorf("%w: save execution receipt: %v", workflow.ErrNotDispatched, err)
	}
	result, executionErr := executor.executeOperation(ctx, request)
	if saved, err := readWorkflowReceipt(executor.directory, request, executor.config.Current()); err == nil {
		receipt = saved
	} else {
		return result, fmt.Errorf("execution receipt could not be verified: %w", err)
	}
	if executionErr == nil {
		receipt.Result = &result
		receipt.CompletedAt = time.Now().UTC()
	} else {
		receipt.Error = executionErr.Error()
	}
	if err = saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
		return result, fmt.Errorf("execution receipt could not be saved: %w", err)
	}
	return result, executionErr
}

func (executor *replWorkflowExecutor) executeOperation(ctx context.Context, request workflow.ExecutionRequest) (workflow.ExecutionResult, error) {
	select {
	case executor.notices <- workflowAgentNotice{state: request.State}:
	case <-ctx.Done():
		return workflow.ExecutionResult{}, ctx.Err()
	}
	step := request.Step
	if executor.config.Current().Mode == "plan" {
		return workflow.ExecutionResult{}, fmt.Errorf("%w: workflow execution is disabled in plan mode", workflow.ErrNotDispatched)
	}
	if step.Kind == "worktree" {
		base, _ := step.Spec["base"].(string)
		if base == "current" || base == "" {
			base = "HEAD"
		}
		branch := "workflow/" + request.ExternalKey
		if err := executor.approve(ctx, "Create Worktrunk workspace "+branch+" from "+base); err != nil {
			return workflow.ExecutionResult{}, err
		}
		path, err := executor.worktrees.Create(ctx, executor.workspace, branch, base)
		return workflow.ExecutionResult{Workspace: path}, err
	}
	workspaceID, _ := step.Spec["workspace"].(string)
	workspace := request.State[workspaceID].Workspace
	if workspace == "" {
		return workflow.ExecutionResult{}, fmt.Errorf("%w: step %s has no provisioned workspace %s", workflow.ErrNotDispatched, step.ID, workspaceID)
	}
	if step.Kind == "agent" || step.Kind == "repeat_check" && request.Node.Phase == "repair" {
		return executor.agent(ctx, request, workspace)
	}
	argv, ok := step.Spec["argv"].([]any)
	if !ok || len(argv) == 0 {
		return workflow.ExecutionResult{}, fmt.Errorf("%w: step %s has no command argv", workflow.ErrNotDispatched, step.ID)
	}
	args := make([]string, len(argv))
	for i, v := range argv {
		var ok bool
		args[i], ok = v.(string)
		if !ok {
			return workflow.ExecutionResult{}, fmt.Errorf("%w: command argv must contain strings", workflow.ErrNotDispatched)
		}
	}
	if err := executor.approve(ctx, strings.Join(args, " ")+"\nWorkspace: "+workspace); err != nil {
		return workflow.ExecutionResult{}, err
	}
	result, err := runWorkflowCommand(ctx, workspace, args, request.ExternalKey)
	return result, err
}

// Workflow commands get only the external key. No internal state IDs are exported.
func runWorkflowCommand(ctx context.Context, workspace string, args []string, key string) (workflow.ExecutionResult, error) {
	if len(args) == 0 {
		return workflow.ExecutionResult{}, fmt.Errorf("empty command")
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Dir = workspace
	command.Env = append(os.Environ(), "UNREAL_WORKFLOW_IDEMPOTENCY_KEY="+key)
	command.WaitDelay = 2 * time.Second
	prepareWorkflowProcess(command)
	output := &workflowOutputBuffer{limit: 128 << 10}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	result := workflow.ExecutionResult{Output: output.String()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exit.ExitCode()
			return result, nil
		}
		return result, err
	}
	return result, nil
}

type workflowOutputBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *workflowOutputBuffer) Write(p []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	n := len(p)
	remaining := max(0, buffer.limit-buffer.Len())
	if n > remaining {
		buffer.truncated = true
		p = p[:remaining]
	}
	_, err := buffer.Buffer.Write(p)
	return n, err
}
func (buffer *workflowOutputBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	text := buffer.Buffer.String()
	if buffer.truncated {
		text += "\n[output truncated]"
	}
	return text
}

func (executor *replWorkflowExecutor) agent(ctx context.Context, request workflow.ExecutionRequest, workspace string) (workflow.ExecutionResult, error) {
	state, err := OpenSessionState(filepath.Join(executor.directory, "agent-sessions"), workspace)
	if err != nil {
		return workflow.ExecutionResult{}, err
	}
	defer state.Close()
	if _, err = state.New(ctx); err != nil {
		return workflow.ExecutionResult{}, err
	}
	if request.ExternalKey != "" {
		receipt, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
		if err != nil {
			return workflow.ExecutionResult{}, fmt.Errorf("%w: %v", workflow.ErrNotDispatched, err)
		}
		receipt.AgentSessionID = state.Current.SessionID
		receipt.Workspace = workspace
		if err = saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
			return workflow.ExecutionResult{}, fmt.Errorf("%w: %v", workflow.ErrNotDispatched, err)
		}
	}
	router, err := NewModelRouter(executor.config, executor.getenv)
	if err != nil {
		return workflow.ExecutionResult{}, err
	}
	defer router.Close()
	var format *llm.OutputFormat
	if schema, ok := request.Step.Spec["output_schema"].(map[string]any); ok && request.Node.Phase != "repair" {
		format = &llm.OutputFormat{Name: "workflow_result", Schema: schema}
	}
	appendField := "system_prompt_append"
	if request.Node.Phase == "repair" {
		appendField = "repair_system_prompt_append"
	}
	systemAppend, _ := request.Step.Spec[appendField].(string)
	runtime, err := newAppRuntimeWithFormat(ctx, state, router, executor.getenv, executor.heartbeat, format, systemAppend)
	if err != nil {
		return workflow.ExecutionResult{}, err
	}
	defer runtime.Close()
	if executor.approvals != nil {
		runtime.options.Approvals.parent = executor.approvals
	}
	select {
	case executor.notices <- workflowAgentNotice{runtime: runtime, text: "Agent session " + state.Current.SessionID}:
	case <-ctx.Done():
		return workflow.ExecutionResult{}, ctx.Err()
	}
	defer func() {
		select {
		case executor.notices <- workflowAgentNotice{finished: true}:
		default:
		}
	}()
	prompt, _ := request.Step.Spec["prompt"].(string)
	skills := request.Step.Spec["skills"]
	if request.Node.Phase == "repair" {
		prompt, _ = request.Step.Spec["repair_prompt"].(string)
		skills = request.Step.Spec["repair_skills"]
		prompt += "\n\nCheck history:\n" + strings.Join(workflowRepairHistory(request.Node.History), "\n")
		checkOutput, _ := json.Marshal(request.Node.Output)
		prompt += "\n\nFailed check output:\n" + string(checkOutput)
	}
	input, _ := json.Marshal(request.Node.Inputs)
	prompt += "\n\nWorkflow inputs (JSON):\n" + string(input)
	if items, ok := skills.([]any); ok {
		for _, item := range items {
			switch value := item.(type) {
			case string:
				prompt += "\n$" + value
			case map[string]any:
				name, _ := value["name"].(string)
				arguments, _ := value["arguments"].(string)
				prompt += "\n$" + name + " " + arguments
			}
		}
	}
	if _, _, err = runtime.Submit(ctx, prompt, false); err != nil {
		return workflow.ExecutionResult{}, err
	}
	snapshot := &workflowPromptSnapshot{StepID: request.Step.ID, Phase: request.Node.Phase, System: runtime.SystemPrompt(), User: prompt}
	if request.ExternalKey != "" {
		receipt, readErr := readWorkflowReceipt(executor.directory, request, executor.config.Current())
		if readErr != nil {
			return workflow.ExecutionResult{}, fmt.Errorf("read prompt receipt: %w", readErr)
		}
		receipt.Prompt = snapshot
		if saveErr := saveWorkflowReceipt(executor.directory, request, receipt); saveErr != nil {
			return workflow.ExecutionResult{}, fmt.Errorf("save prompt receipt: %w", saveErr)
		}
	}
	select {
	case executor.notices <- workflowAgentNotice{stepID: request.Step.ID, prompt: snapshot}:
	case <-ctx.Done():
		return workflow.ExecutionResult{}, ctx.Err()
	}
	var final llm.Response
	for {
		select {
		case <-ctx.Done():
			return workflow.ExecutionResult{}, ctx.Err()
		case <-runtime.Events():
			for _, event := range runtime.DrainEvents() {
				if activity := workflowAgentActivity(event); activity != "" {
					select {
					case executor.notices <- workflowAgentNotice{text: activity}:
					default:
					}
				}
				switch event.Kind {
				case EventItem:
					if event.Item.Kind == sessionstore.ItemModelResponse {
						if response, ok := event.Item.Data.(sessionstore.ModelResponse); ok {
							final = response.Response
						}
					}
				case EventTaskError:
					return workflow.ExecutionResult{}, event.Err
				case EventTaskIdle:
					if final.Failure != nil {
						return workflow.ExecutionResult{}, fmt.Errorf("agent failed: %s", final.Failure.Message)
					}
					if final.Stop != llm.StopComplete {
						return workflow.ExecutionResult{}, fmt.Errorf("agent stopped without a complete result: %s", final.Stop)
					}
					var parts []string
					for _, item := range final.Output {
						if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleAssistant {
							parts = append(parts, message.Text)
						}
					}
					text := strings.Join(parts, "\n")
					if strings.TrimSpace(text) == "" {
						return workflow.ExecutionResult{}, fmt.Errorf("agent returned no final text")
					}
					if format == nil {
						return workflow.ExecutionResult{Output: text}, nil
					}
					if !json.Valid([]byte(text)) {
						return workflow.ExecutionResult{}, fmt.Errorf("agent output is not one valid JSON value")
					}
					var output any
					decoder := json.NewDecoder(strings.NewReader(text))
					decoder.UseNumber()
					if err := decoder.Decode(&output); err != nil {
						return workflow.ExecutionResult{}, fmt.Errorf("agent output is not JSON: %w", err)
					}
					// The shared runner validates the exported schema before checkpointing.
					return workflow.ExecutionResult{Output: output}, nil
				}
			}
		}
	}
}

func (executor *replWorkflowExecutor) approve(ctx context.Context, command string) error {
	if executor.config.Current().Mode != "edit" && (executor.approvals == nil || !executor.approvals.workflow) {
		return nil
	}
	if executor.approvals == nil {
		return fmt.Errorf("%w: workflow command approval is unavailable", workflow.ErrNotDispatched)
	}
	arguments, _ := json.Marshal(map[string]string{"command": command})
	request := &approvalRequest{call: llm.ToolCall{Name: "Bash", Arguments: string(arguments)}, answer: make(chan bool, 1)}
	gate := executor.approvals
	gate.enqueue(request)
	select {
	case allow := <-request.answer:
		if !allow {
			return fmt.Errorf("%w: workflow operation was not approved", workflow.ErrNotDispatched)
		}
		return nil
	case <-ctx.Done():
		gate.Decide(request, false)
		return ctx.Err()
	}
}

// Recovery diagnostics do not change the payload of a retried repair attempt.
func workflowRepairHistory(history []string) []string {
	result := make([]string, 0, len(history))
	for _, line := range history {
		if strings.HasPrefix(line, "reconciliation ") || strings.HasPrefix(line, "external operation outcome unknown: ") {
			continue
		}
		result = append(result, line)
	}
	return result
}

// Activity notices describe durable runtime events without exposing prompt/tool arguments.
func workflowAgentActivity(event RuntimeEvent) string {
	switch event.Kind {
	case EventTaskStarted:
		return "Agent waiting for model response"
	case EventApproval:
		return "Agent waiting for command approval"
	case EventQuestion:
		return "Agent waiting for your answer"
	case EventItem:
		switch event.Item.Kind {
		case sessionstore.ItemTurn:
			return "Agent waiting for model response"
		case sessionstore.ItemToolCallStatus:
			return "Agent processing tool execution"
		case sessionstore.ItemModelResponse:
			if response, ok := event.Item.Data.(sessionstore.ModelResponse); ok {
				var names []string
				for _, item := range response.Response.Output {
					if call, ok := item.Data.(llm.ToolCall); ok {
						names = append(names, call.Name)
					}
				}
				if len(names) > 0 {
					return "Agent requested " + strings.Join(names, ", ")
				}
				return "Agent received model response"
			}
		}
	}
	return ""
}
