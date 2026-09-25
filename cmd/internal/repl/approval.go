package repl

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// Approval is scoped to one tool call. Replaying a call after a restart asks again.
type approvalRequest struct {
	call   llm.ToolCall
	answer chan bool
}

type approvalGate struct {
	tool.Registry
	config     *ConfigStore
	ctx        context.Context
	mu         sync.Mutex
	pending    []*approvalRequest
	notify     func()
	parent     *approvalGate
	approveAll bool
	workflow   bool
}

func (gate *approvalGate) Resolve(name string) (tool.Translator, bool) {
	translator, ok := gate.Registry.Resolve(name)
	if !ok {
		return nil, false
	}
	return guardedTranslator{Translator: translator, gate: gate, name: name}, true
}

func (gate *approvalGate) Pending() *approvalRequest {
	if gate.parent != nil {
		return gate.parent.Pending()
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.pending) == 0 {
		return nil
	}
	return gate.pending[0]
}

func (gate *approvalGate) Decide(request *approvalRequest, allow bool) {
	if gate.parent != nil {
		gate.parent.Decide(request, allow)
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	for i, pending := range gate.pending {
		if pending == request {
			gate.pending = append(gate.pending[:i], gate.pending[i+1:]...)
			pending.answer <- allow
			break
		}
	}
	if len(gate.pending) > 0 && gate.notify != nil {
		gate.notify()
	}
}

func (gate *approvalGate) Cancel() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	for _, pending := range gate.pending {
		pending.answer <- false
	}
	gate.pending = nil
}

type guardedTranslator struct {
	tool.Translator
	gate *approvalGate
	name string
}

func (current guardedTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	mode := current.gate.config.Current().Mode
	if mode == "plan" && (current.name == tool.BashName || current.name == "TaskWrite") {
		return tool.ErrorStatus(fmt.Sprintf("%s is disabled in read-only plan mode", current.name), operation.DefaultMaxOutputLength)
	}
	if (mode == "edit" || current.gate.parent != nil) && current.name == tool.BashName {
		request := &approvalRequest{call: call, answer: make(chan bool, 1)}
		current.gate.enqueue(request)
		select {
		case approved := <-request.answer:
			if !approved {
				return tool.ErrorStatus("Bash command was not approved", operation.DefaultMaxOutputLength)
			}
		case <-current.gate.ctx.Done():
			current.gate.Decide(request, false)
			return tool.ErrorStatus("Bash approval canceled", operation.DefaultMaxOutputLength)
		}
	}
	return current.Translator.Translate(ctx, call)
}

func (model *uiModel) approvalView(request *approvalRequest) tea.View {
	if model.workflow != nil && model.workflow.executor != nil && model.workflow.executor.approvals != nil && model.workflow.executor.approvals.Pending() == request {
		return model.workflowOverlayView(func(content *uiModel) tea.View { return content.approvalContentView(request) })
	}
	return model.approvalContentView(request)
}
func (model *uiModel) approvalContentView(request *approvalRequest) tea.View {
	var arguments struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(request.call.Arguments), &arguments); err != nil {
		arguments.Command = request.call.Arguments
	}
	width, height := max(1, model.width), max(1, model.height)
	lines := []string{model.ink("◆ UNREAL  /  COMMAND APPROVAL", model.palette().lilac, true), "", "Review this Bash command before it runs:", ""}
	for _, line := range strings.Split(arguments.Command, "\n") {
		lines = append(lines, strings.Split(ansi.Hardwrap(sanitizeTerminal(line), max(1, width-2), true), "\n")...)
	}
	visible := max(0, height-6)
	top := min(model.approvalTop, max(0, len(lines)-4-visible))
	body := append([]string(nil), lines[:min(4, len(lines))]...)
	body = append(body, lines[4+top:min(len(lines), 4+top+visible)]...)
	footer := "↑/↓ scroll   Y approve once   N/Esc deny"
	if model.workflow != nil && model.workflow.executor != nil && model.workflow.executor.approvals != nil && model.workflow.executor.approvals.Pending() == request {
		footer += "   A approve all commands for this run"
	}
	body = append(body, "", footer)
	if len(body) > height {
		body = body[:height]
	}
	for i := range body {
		body[i] = ansi.Truncate(body[i], width, "…")
	}
	view := tea.NewView(strings.Join(body, "\n"))
	view.WindowTitle = "Unreal · Approve command"
	return view
}

// Child workflow agents share the run's permission decision without changing config.
func (gate *approvalGate) enqueue(request *approvalRequest) {
	if gate.parent != nil {
		gate.parent.enqueue(request)
		return
	}
	gate.mu.Lock()
	if gate.approveAll {
		gate.mu.Unlock()
		request.answer <- true
		return
	}
	gate.pending = append(gate.pending, request)
	notify := gate.notify
	gate.mu.Unlock()
	if notify != nil {
		notify()
	}
}
func (gate *approvalGate) AllowAll() {
	if gate.parent != nil {
		gate.parent.AllowAll()
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.approveAll = true
	for _, request := range gate.pending {
		request.answer <- true
	}
	gate.pending = nil
}
