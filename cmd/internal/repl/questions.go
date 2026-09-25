package repl

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const askUserName = "AskUser"
const askUserPlanType operation.RemoteJobPlanType = "unreal-agent-repl.ask-user"
const askUserPlanVersion operation.RemoteJobPlanVersion = 1

type AskUserArgs struct {
	Title     string     `json:"title"`
	Questions []Question `json:"questions"`
}

type Question struct {
	ID          string           `json:"id"`
	Prompt      string           `json:"prompt"`
	Description string           `json:"description,omitempty"`
	Kind        string           `json:"kind"`
	Options     []QuestionOption `json:"options,omitempty"`
	Recommended []string         `json:"recommended,omitempty"`
	Required    bool             `json:"required"`
	AllowOther  bool             `json:"allow_other"`
}

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AskUserPlan struct {
	CallID string      `json:"call_id"`
	Form   AskUserArgs `json:"form"`
}

type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected,omitempty"`
	Text       string   `json:"text,omitempty"`
	Skipped    bool     `json:"skipped,omitempty"`
}

type AskUserResult struct {
	Status  string           `json:"status"`
	Answers []QuestionAnswer `json:"answers,omitempty"`
}

func validateAskUserForm(form AskUserArgs) error {
	if strings.TrimSpace(form.Title) == "" {
		return errors.New("title is required")
	}
	if len(form.Questions) == 0 || len(form.Questions) > 12 {
		return errors.New("questions must contain 1 to 12 steps")
	}
	seen := map[string]bool{}
	for _, question := range form.Questions {
		if strings.TrimSpace(question.ID) == "" || seen[question.ID] {
			return fmt.Errorf("question ID %q is empty or repeated", question.ID)
		}
		seen[question.ID] = true
		if strings.TrimSpace(question.Prompt) == "" {
			return fmt.Errorf("question %q has no prompt", question.ID)
		}
		if question.Kind != "text" && question.Kind != "single_select" && question.Kind != "multi_select" {
			return fmt.Errorf("question %q has invalid kind %q", question.ID, question.Kind)
		}
		if len(question.Options) > 20 {
			return fmt.Errorf("question %q has more than 20 options", question.ID)
		}
		if question.Kind == "text" && (len(question.Options) != 0 || len(question.Recommended) != 0 || question.AllowOther) {
			return fmt.Errorf("text question %q cannot have select options", question.ID)
		}
		if question.Kind != "text" && len(question.Options) == 0 {
			return fmt.Errorf("select question %q needs options", question.ID)
		}
		options := map[string]bool{}
		for _, option := range question.Options {
			if strings.TrimSpace(option.ID) == "" || option.ID == skipOptionID || strings.TrimSpace(option.Label) == "" || options[option.ID] {
				return fmt.Errorf("question %q has an empty or repeated option", question.ID)
			}
			options[option.ID] = true
		}
		recommended := map[string]bool{}
		for _, id := range question.Recommended {
			if !options[id] || recommended[id] {
				return fmt.Errorf("question %q has an invalid recommended option %q", question.ID, id)
			}
			recommended[id] = true
		}
	}
	return nil
}

func validateAskUserResult(form AskUserArgs, result AskUserResult) error {
	if result.Status == "dismissed" {
		if len(result.Answers) != 0 {
			return errors.New("dismissed form cannot contain answers")
		}
		return nil
	}
	if result.Status != "answered" {
		return fmt.Errorf("invalid form status %q", result.Status)
	}
	questions := map[string]Question{}
	for _, question := range form.Questions {
		questions[question.ID] = question
	}
	seen := map[string]bool{}
	for _, answer := range result.Answers {
		question, exists := questions[answer.QuestionID]
		if !exists || seen[answer.QuestionID] {
			return fmt.Errorf("unknown or repeated answer %q", answer.QuestionID)
		}
		seen[answer.QuestionID] = true
		if answer.Skipped {
			if question.Required || len(answer.Selected) != 0 || answer.Text != "" {
				return fmt.Errorf("question %q cannot be skipped", question.ID)
			}
			continue
		}
		if question.Kind == "text" {
			if len(answer.Selected) != 0 || (question.Required && strings.TrimSpace(answer.Text) == "") {
				return fmt.Errorf("question %q requires text", question.ID)
			}
			continue
		}
		if question.Kind == "single_select" && len(answer.Selected) > 1 {
			return fmt.Errorf("question %q allows one choice", question.ID)
		}
		if question.Kind == "single_select" && len(answer.Selected) != 0 && answer.Text != "" {
			return fmt.Errorf("question %q requires either a choice or other text", question.ID)
		}
		if question.Required && len(answer.Selected) == 0 && strings.TrimSpace(answer.Text) == "" {
			return fmt.Errorf("question %q requires a choice", question.ID)
		}
		if !question.AllowOther && answer.Text != "" {
			return fmt.Errorf("question %q does not allow other text", question.ID)
		}
		options := map[string]bool{}
		for _, option := range question.Options {
			options[option.ID] = true
		}
		selected := map[string]bool{}
		for _, id := range answer.Selected {
			if !options[id] || selected[id] {
				return fmt.Errorf("question %q has invalid selection %q", question.ID, id)
			}
			selected[id] = true
		}
	}
	for _, question := range form.Questions {
		if !seen[question.ID] {
			return fmt.Errorf("question %q has no answer or explicit skip", question.ID)
		}
	}
	return nil
}

type askUserTranslator struct{}

func (askUserTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	var form AskUserArgs
	if err := json.Unmarshal([]byte(call.Arguments), &form, json.RejectUnknownMembers(true)); err != nil {
		return tool.ErrorStatus("decode AskUser arguments: "+err.Error(), operation.DefaultMaxOutputLength)
	}
	if err := validateAskUserForm(form); err != nil {
		return tool.ErrorStatus("invalid AskUser form: "+err.Error(), operation.DefaultMaxOutputLength)
	}
	encoded, err := json.Marshal(AskUserPlan{CallID: call.CallID, Form: form})
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: askUserPlanType, Version: askUserPlanVersion, Data: jsontext.Value(encoded)})
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}

func (askUserTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	result := llm.ToolResult{CallID: callID}
	if status.Error != "" {
		result.Output = []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Error: " + status.Error}}
		return result, nil
	}
	if len(operations) != 1 {
		return result, fmt.Errorf("AskUser call %q has %d operations, want 1", callID, len(operations))
	}
	state, err := operation.DecodeRemoteJobState(operations[0])
	if err != nil {
		return result, err
	}
	content := "Question awaits an answer."
	switch operations[0].Status {
	case operation.StatusCompleted:
		content = state.TerminalResult
	case operation.StatusFailed:
		content = "Error: " + state.TerminalError
	case operation.StatusCanceled:
		content = "Question canceled."
	}
	result.Output = []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: content}}
	return result, nil
}

type askUserRegistry struct{ tool.Registry }

func (registry askUserRegistry) StaticDefinitions() []tool.Definition {
	return append(registry.Registry.StaticDefinitions(), tool.Definition{Tool: askUserDefinition()})
}

func (registry askUserRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == askUserName {
		return askUserTranslator{}, true
	}
	return registry.Registry.Resolve(name)
}

func askUserDefinition() llm.Tool {
	option := map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
	}, "required": []any{"id", "label"}, "additionalProperties": false}
	question := map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
		"kind":        map[string]any{"type": "string", "enum": []any{"text", "single_select", "multi_select"}},
		"options":     map[string]any{"type": "array", "items": option},
		"recommended": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"required":    map[string]any{"type": "boolean"}, "allow_other": map[string]any{"type": "boolean"},
	}, "required": []any{"id", "prompt", "kind", "required", "allow_other"}, "additionalProperties": false}
	return llm.Tool{Type: llm.ToolFunction, Name: askUserName, Description: "Ask the user one form with one or more text or selection questions, then wait for an answer.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{
			"title": map[string]any{"type": "string"}, "questions": map[string]any{"type": "array", "items": question},
		}, "required": []any{"title", "questions"}, "additionalProperties": false,
	}}
}

type askUserHandler struct {
	mu         sync.Mutex
	jobs       map[operation.ID]operation.Operation
	order      []operation.ID
	updates    chan operation.Operation
	submitting map[operation.ID]bool
	notify     func()
}

func newAskUserHandler() *askUserHandler {
	return &askUserHandler{jobs: make(map[operation.ID]operation.Operation), submitting: make(map[operation.ID]bool), updates: make(chan operation.Operation, 128)}
}

func (*askUserHandler) RemoteJobPlanType() operation.RemoteJobPlanType { return askUserPlanType }
func (*askUserHandler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion {
	return askUserPlanVersion
}
func (handler *askUserHandler) RemoteJobUpdates() <-chan operation.Operation { return handler.updates }

func (handler *askUserHandler) SetNotify(notify func()) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.notify = notify
}

func decodeAskUserPlan(current operation.Operation) (AskUserPlan, error) {
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		return AskUserPlan{}, err
	}
	if state.Plan.Type != askUserPlanType || state.Plan.Version != askUserPlanVersion {
		return AskUserPlan{}, errors.New("unsupported AskUser plan")
	}
	var plan AskUserPlan
	if err := json.Unmarshal(state.Plan.Data, &plan, json.RejectUnknownMembers(true)); err != nil {
		return AskUserPlan{}, err
	}
	return plan, validateAskUserForm(plan.Form)
}

func (handler *askUserHandler) AddRemoteJob(current operation.Operation) error {
	if _, err := decodeAskUserPlan(current); err != nil {
		return err
	}
	handler.mu.Lock()
	if _, exists := handler.jobs[current.ID]; exists {
		handler.mu.Unlock()
		return nil
	}
	if current.Status == operation.StatusCompleted || current.Status == operation.StatusFailed || current.Status == operation.StatusCanceled {
		handler.mu.Unlock()
		return nil
	}
	handler.jobs[current.ID] = current
	handler.order = append(handler.order, current.ID)
	notify := handler.notify
	handler.mu.Unlock()
	if notify != nil {
		notify()
	}
	return nil
}

func (handler *askUserHandler) Pending() int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return len(handler.jobs)
}

type PendingQuestion struct {
	OperationID operation.ID
	Plan        AskUserPlan
	Submitting  bool
}

func (handler *askUserHandler) Questions() []PendingQuestion {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	var pending []PendingQuestion
	for _, id := range handler.order {
		current, exists := handler.jobs[id]
		if !exists {
			continue
		}
		plan, err := decodeAskUserPlan(current)
		if err == nil {
			pending = append(pending, PendingQuestion{OperationID: id, Plan: plan, Submitting: handler.submitting[id]})
		}
	}
	return pending
}

func (handler *askUserHandler) Submit(id operation.ID, result AskUserResult) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	current, exists := handler.jobs[id]
	if !exists || handler.submitting[id] {
		return fmt.Errorf("question %q is no longer awaiting an answer", id)
	}
	plan, err := decodeAskUserPlan(current)
	if err != nil {
		return err
	}
	if err := validateAskUserResult(plan.Form, result); err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		return err
	}
	state.TerminalResult = string(encoded)
	step, err := operation.UpdateRemoteJob(current, state, operation.StatusCompleted)
	if err != nil {
		return err
	}
	select {
	case handler.updates <- *step.Operation:
		handler.submitting[id] = true
		return nil
	default:
		return errors.New("question update queue is full")
	}
}

func (handler *askUserHandler) CancelRemoteJob(id operation.ID, reason string) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	current, exists := handler.jobs[id]
	if !exists || handler.submitting[id] {
		return nil
	}
	step, err := operation.CancelRemoteJob(current)
	if err != nil {
		return err
	}
	select {
	case handler.updates <- *step.Operation:
		handler.submitting[id] = true
		return nil
	default:
		return fmt.Errorf("cancel question %q (%s): update queue is full", id, reason)
	}
}

// Ack observes the canonical tool status after persistence. The gate opens only
// once the answer or dismissal has reached the session store.
func (handler *askUserHandler) Ack(item sessionstore.Item) {
	if item.Kind != sessionstore.ItemToolCallStatus {
		return
	}
	status, ok := item.Data.(sessionstore.ToolCallStatus)
	if !ok {
		return
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	for _, current := range status.Operations {
		if _, exists := handler.jobs[current.ID]; !exists {
			continue
		}
		if current.Status == operation.StatusCompleted || current.Status == operation.StatusCanceled || current.Status == operation.StatusFailed {
			delete(handler.jobs, current.ID)
			delete(handler.submitting, current.ID)
		}
	}
}
