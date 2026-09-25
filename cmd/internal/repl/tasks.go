package repl

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const taskPlanType operation.RemoteJobPlanType = "unreal-agent-repl.tasks"

type taskItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	DependsOn []string `json:"depends_on"`
	Note      string   `json:"note"`
}

type taskList struct {
	Tasks []taskItem `json:"tasks"`
}
type taskPlan struct {
	Action string   `json:"action"`
	List   taskList `json:"list"`
}

func validateTasks(list taskList) error {
	if len(list.Tasks) > 100 {
		return errors.New("task list must contain at most 100 tasks")
	}
	seen := map[string]string{}
	for _, task := range list.Tasks {
		if strings.TrimSpace(task.ID) == "" || len(task.ID) > 128 || seen[task.ID] != "" {
			return fmt.Errorf("task ID %q must be unique, nonempty, and at most 128 bytes", task.ID)
		}
		if strings.TrimSpace(task.Title) == "" || len(task.Title) > 500 || len(task.Note) > 2000 {
			return fmt.Errorf("task %s needs a title of 1–500 bytes and a note of at most 2000 bytes", task.ID)
		}
		switch task.Status {
		case "pending", "in_progress":
		case "completed", "blocked", "canceled":
			if strings.TrimSpace(task.Note) == "" {
				return fmt.Errorf("task %s needs completion evidence or a reason in its note", task.ID)
			}
		default:
			return fmt.Errorf("task %s has invalid status %q", task.ID, task.Status)
		}
		for _, dependency := range task.DependsOn {
			status, exists := seen[dependency]
			if !exists {
				return fmt.Errorf("task %s must follow its dependency %s", task.ID, dependency)
			}
			if (task.Status == "in_progress" || task.Status == "completed") && status != "completed" {
				return fmt.Errorf("finish dependency %s before starting or completing %s", dependency, task.ID)
			}
		}
		seen[task.ID] = task.Status
	}
	return nil
}

type taskRegistry struct{ tool.Registry }

func (registry taskRegistry) StaticDefinitions() []tool.Definition {
	return append(registry.Registry.StaticDefinitions(), tool.Definition{Tool: taskDefinition("TaskWrite")}, tool.Definition{Tool: taskDefinition("TaskRead")})
}
func (registry taskRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == "TaskRead" || name == "TaskWrite" {
		return taskTranslator{name: name}, true
	}
	return registry.Registry.Resolve(name)
}

func taskDefinition(name string) llm.Tool {
	definition := llm.Tool{Type: llm.ToolFunction, Name: name, Description: "Read the current session task list. Use when resuming and before finishing multi-step work to check for unfinished tasks.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	if name == "TaskRead" {
		return definition
	}
	definition.Description = "Replace the session task list with the complete ordered list. Use before multi-step work and update it as work progresses. Keep unfinished tasks when adding scope. Dependencies must appear earlier and be completed before dependent work starts. Completed tasks need evidence in note; blocked or canceled tasks need a reason. Await the result before another task update or read."
	definition.Parameters = map[string]any{"type": "object", "properties": map[string]any{"tasks": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "object", "properties": map[string]any{
		"id":         map[string]any{"type": "string", "description": "Stable unique task ID."},
		"title":      map[string]any{"type": "string", "description": "Concrete outcome to deliver."},
		"status":     map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "blocked", "canceled"}},
		"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"note":       map[string]any{"type": "string", "description": "Completion evidence, blocker, or cancellation reason. Empty for pending work."},
	}, "required": []string{"id", "title", "status", "depends_on", "note"}, "additionalProperties": false}}}, "required": []string{"tasks"}, "additionalProperties": false}
	return definition
}

type taskTranslator struct{ name string }

func (translator taskTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	plan := taskPlan{Action: translator.name}
	var err error
	if translator.name == "TaskWrite" {
		err = json.Unmarshal([]byte(call.Arguments), &plan.List, json.RejectUnknownMembers(true))
		if err == nil && plan.List.Tasks == nil {
			err = errors.New("tasks must be an array; use [] to clear the list")
		}
		if err == nil {
			err = validateTasks(plan.List)
		}
	} else {
		err = json.Unmarshal([]byte(call.Arguments), &struct{}{}, json.RejectUnknownMembers(true))
	}
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: taskPlanType, Version: 1, Data: encoded})
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	spec.MaxOutputLength = operation.MaxOutputLength
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}
func (taskTranslator) TranslateResult(id string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	text := status.Error
	if text == "" {
		if len(operations) != 1 {
			return llm.ToolResult{}, fmt.Errorf("task call %s needs one operation", id)
		}
		state, err := operation.DecodeRemoteJobState(operations[0])
		if err != nil {
			return llm.ToolResult{}, err
		}
		text = state.TerminalResult
		if state.TerminalError != "" {
			text = "Error: " + state.TerminalError
		}
		if text == "" {
			text = "Task operation: " + string(operations[0].Status)
		}
	}
	return llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
}

type taskHandler struct {
	ctx       context.Context
	store     sessionstore.Store
	sessionID session.ID
	updates   chan operation.Operation
}

func (*taskHandler) RemoteJobPlanType() operation.RemoteJobPlanType       { return taskPlanType }
func (*taskHandler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return 1 }
func (handler *taskHandler) RemoteJobUpdates() <-chan operation.Operation { return handler.updates }
func (*taskHandler) CancelRemoteJob(operation.ID, string) error           { return nil }
func (handler *taskHandler) AddRemoteJob(current operation.Operation) error {
	if current.Status == operation.StatusCompleted || current.Status == operation.StatusFailed || current.Status == operation.StatusCanceled {
		return nil
	}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		return err
	}
	var plan taskPlan
	if err = json.Unmarshal(state.Plan.Data, &plan, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if state.Plan.Type != taskPlanType || state.Plan.Version != 1 {
		return errors.New("unsupported task plan")
	}
	if current.Status == operation.StatusCanceling {
		step, err := operation.CancelRemoteJob(current)
		if err != nil {
			return err
		}
		select {
		case handler.updates <- *step.Operation:
			return nil
		case <-handler.ctx.Done():
			return handler.ctx.Err()
		}
	}
	switch plan.Action {
	case "TaskRead":
		plan.List, err = readSessionTasks(handler.ctx, handler.store, handler.sessionID)
	case "TaskWrite":
		err = validateTasks(plan.List)
	default:
		err = errors.New("unknown task action")
	}
	var step operation.Step
	if err != nil {
		step, err = operation.FailRemoteJob(current, err)
	} else {
		encoded, encodeErr := json.Marshal(plan.List)
		if encodeErr != nil {
			return encodeErr
		}
		state.TerminalResult = string(encoded)
		step, err = operation.UpdateRemoteJob(current, state, operation.StatusCompleted)
	}
	if err != nil {
		return err
	}
	select {
	case handler.updates <- *step.Operation:
		return nil
	case <-handler.ctx.Done():
		return handler.ctx.Err()
	}
}

// A stored TaskWrite is the active-state source. Older sessions need no migration.
func taskListActive(list taskList) bool {
	for _, task := range list.Tasks {
		if task.Status != "completed" {
			return true
		}
	}
	return false
}

func completedTaskWrite(current operation.Operation) (taskList, bool, error) {
	if current.Type != operation.TypeRemoteJob || current.Status != operation.StatusCompleted {
		return taskList{}, false, nil
	}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		return taskList{}, false, err
	}
	if state.Plan.Type != taskPlanType {
		return taskList{}, false, nil
	}
	var plan taskPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return taskList{}, false, err
	}
	if plan.Action != "TaskWrite" {
		return taskList{}, false, nil
	}
	return plan.List, true, nil
}
func readSessionTasks(ctx context.Context, store sessionstore.Store, id session.ID) (taskList, error) {
	result := taskList{Tasks: []taskItem{}}
	after := sessionstore.BeforeFirst
	for {
		page, err := store.Items(ctx, id, after, 256)
		if err != nil {
			return result, err
		}
		for _, item := range page.Items {
			status, ok := item.Data.(sessionstore.ToolCallStatus)
			if !ok {
				continue
			}
			for _, current := range status.Operations {
				list, written, err := completedTaskWrite(current)
				if err != nil {
					return result, err
				}
				if written {
					result = list
				}
			}
		}
		if !page.More {
			return result, nil
		}
		if page.NextAfter <= after {
			return result, errors.New("task history did not advance")
		}
		after = page.NextAfter
	}
}
