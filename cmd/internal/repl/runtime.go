package repl

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/coordinator"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type Admission string

const (
	AdmissionSent   Admission = "sent"
	AdmissionQueued Admission = "queued"
)

type RuntimeEventKind string

const (
	EventItem        RuntimeEventKind = "item"
	EventTaskStarted RuntimeEventKind = "task_started"
	EventTaskIdle    RuntimeEventKind = "task_idle"
	EventTaskError   RuntimeEventKind = "task_error"
	EventQuestion    RuntimeEventKind = "question"
	EventApproval    RuntimeEventKind = "approval"
)

type RuntimeEvent struct {
	Kind       RuntimeEventKind
	Generation uint64
	Item       sessionstore.Item
	Err        error
}

type runtimeMailbox struct {
	mu     sync.Mutex
	events []RuntimeEvent
	wake   chan struct{}
}

func (mailbox *runtimeMailbox) publish(event RuntimeEvent) {
	mailbox.mu.Lock()
	mailbox.events = append(mailbox.events, event)
	mailbox.mu.Unlock()
	select {
	case mailbox.wake <- struct{}{}:
	default:
	}
}

func (mailbox *runtimeMailbox) drain() []RuntimeEvent {
	mailbox.mu.Lock()
	defer mailbox.mu.Unlock()
	events := mailbox.events
	mailbox.events = nil
	return events
}

type BuilderFactory func(string) (contextbuilder.Builder, func() bool, error)

type RuntimeOptions struct {
	SessionID             session.ID
	Sessions              sessionstore.Store
	LLM                   llm.Adapter
	Tools                 tool.Registry
	Operations            operation.Manager
	NewBuilder            BuilderFactory
	ToolHeartbeatInterval time.Duration
	Questions             *askUserHandler
	Approvals             *approvalGate
	CloseOperations       func()
	OnSteer               func(string) error
	ValidateInput         func(string) error
}

type submission struct {
	input inbox.Input
	text  string
}

type activeTask struct {
	generation uint64
	inputs     *inbox.Inbox
	cancel     context.CancelFunc
	observer   sessionstore.ObserverID
	pending    []submission
	stopped    bool
}

// Runtime serializes task admission while each coordinator owns canonical
// writes. Plain submissions enter the inbox only after the previous task idles.
type Runtime struct {
	mu           sync.Mutex
	systemPrompt string
	ctx          context.Context
	options      RuntimeOptions
	mailbox      runtimeMailbox
	done         chan struct{}
	generation   uint64
	active       *activeTask
	queue        []submission
	failed       error
	closed       bool
}

func NewRuntime(ctx context.Context, options RuntimeOptions) (*Runtime, error) {
	if options.SessionID == "" || options.Sessions == nil || options.LLM == nil || options.Tools == nil || options.Operations == nil || options.NewBuilder == nil {
		return nil, errors.New("runtime requires session, store, model, tools, operations, and builder factory")
	}
	return &Runtime{
		ctx: ctx, options: options,
		mailbox: runtimeMailbox{wake: make(chan struct{}, 1)},
		done:    make(chan struct{}),
	}, nil
}

func (runtime *Runtime) Events() <-chan struct{}     { return runtime.mailbox.wake }
func (runtime *Runtime) Done() <-chan struct{}       { return runtime.done }
func (runtime *Runtime) DrainEvents() []RuntimeEvent { return runtime.mailbox.drain() }

func (runtime *Runtime) QueueLength() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.queue)
}

// QueuedPrompts snapshots unsent prompts in admission order for the composer.
func (runtime *Runtime) QueuedPrompts() []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	result := make([]string, len(runtime.queue))
	for i, value := range runtime.queue {
		result[i] = value.text
	}
	return result
}

func (runtime *Runtime) IsBusy() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.active != nil
}

func (runtime *Runtime) Questions() []PendingQuestion {
	if runtime.options.Questions == nil {
		return nil
	}
	return runtime.options.Questions.Questions()
}

func (runtime *Runtime) AnswerQuestion(id operation.ID, result AskUserResult) error {
	if runtime.options.Questions == nil {
		return errors.New("question handler is unavailable")
	}
	return runtime.options.Questions.Submit(id, result)
}

func (runtime *Runtime) Submit(ctx context.Context, text string, steer bool) (inbox.ID, Admission, error) {
	if text == "" {
		return "", "", errors.New("prompt is empty")
	}
	if runtime.options.ValidateInput != nil {
		if err := runtime.options.ValidateInput(text); err != nil {
			return "", "", err
		}
	}
	payload, err := json.Marshal(text)
	if err != nil {
		return "", "", err
	}
	value := submission{input: inbox.Input{ID: inbox.ID(uuid.New().String()), Kind: inbox.InputExternal, Payload: payload}, text: text}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return "", "", errors.New("runtime is closed")
	}
	if runtime.failed != nil {
		return "", "", fmt.Errorf("runtime needs recovery: %w", runtime.failed)
	}
	if runtime.active == nil {
		if err := runtime.startLocked(value); err != nil {
			return "", "", err
		}
		return value.input.ID, AdmissionSent, nil
	}
	if !steer {
		runtime.queue = append(runtime.queue, value)
		return value.input.ID, AdmissionQueued, nil
	}
	if runtime.options.OnSteer != nil {
		if err := runtime.options.OnSteer(text); err != nil {
			return "", "", err
		}
	}
	if err := runtime.active.inputs.Submit(ctx, value.input); err != nil {
		return "", "", err
	}
	runtime.active.pending = append(runtime.active.pending, value)
	return value.input.ID, AdmissionSent, nil
}

func (runtime *Runtime) startLocked(value submission) error {
	restored, err := runtime.options.Sessions.Resume(runtime.ctx, runtime.options.SessionID)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	return runtime.startWithRestoredLocked(restored, &value)
}

// Recover restarts unfinished canonical operations without inventing a new
// external input. Completed sessions stay idle until the user submits text.
func (runtime *Runtime) Recover() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != nil || runtime.closed {
		return errors.New("runtime is not idle")
	}
	restored, err := runtime.options.Sessions.Resume(runtime.ctx, runtime.options.SessionID)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	if len(restored.Operations) == 0 {
		return nil
	}
	return runtime.startWithRestoredLocked(restored, nil)
}

func (runtime *Runtime) startWithRestoredLocked(restored sessionstore.ResumeState, value *submission) error {
	runCtx, cancel := context.WithCancel(runtime.ctx)
	inputs, err := inbox.New(runCtx, restored.ExternalInputIDs)
	if err != nil {
		cancel()
		return err
	}
	if value != nil {
		if err := inputs.Submit(runCtx, value.input); err != nil {
			cancel()
			return err
		}
	}
	stop, err := controlInput(inbox.StopWhenIdle, "finish when idle")
	if err != nil {
		cancel()
		return err
	}
	if err := inputs.Submit(runCtx, stop); err != nil {
		cancel()
		return err
	}
	prompt := ""
	if value != nil {
		prompt = value.text
	}
	builder, canRequest, err := runtime.options.NewBuilder(prompt)
	if err != nil {
		cancel()
		return fmt.Errorf("prepare model context: %w", err)
	}
	runtime.systemPrompt = basePromptText(builder)
	runtime.generation++
	generation := runtime.generation
	observer := runtime.options.Sessions.AddObserver(func(id session.ID, item sessionstore.Item) {
		if id == runtime.options.SessionID {
			if runtime.options.Questions != nil {
				runtime.options.Questions.Ack(item)
			}
			runtime.mailbox.publish(RuntimeEvent{Kind: EventItem, Generation: generation, Item: item})
		}
	})
	task := &activeTask{generation: generation, inputs: inputs, cancel: cancel, observer: observer}
	if value != nil {
		task.pending = []submission{*value}
	}
	runtime.active = task
	runtime.mailbox.publish(RuntimeEvent{Kind: EventTaskStarted, Generation: generation})
	current := coordinator.New(coordinator.Dependencies{
		ToolHeartbeatInterval: runtime.options.ToolHeartbeatInterval,
		SessionID:             runtime.options.SessionID, Inbox: inputs, Restored: restored,
		Sessions: runtime.options.Sessions, ContextBuilder: builder, LLM: runtime.options.LLM,
		Tools: runtime.options.Tools, Operations: runtime.options.Operations,
		CanRequestModel: canRequest,
	})
	go runtime.runTask(task, current, runCtx)
	return nil
}

func (runtime *Runtime) runTask(task *activeTask, current coordinator.Coordinator, ctx context.Context) {
	err := current.Run(ctx)
	task.cancel()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.options.Sessions.RemoveObserver(task.observer)
	if runtime.active != task {
		return
	}
	runtime.active = nil
	// An input can be accepted by the inbox just as the coordinator exits.
	// Keep only IDs that did not reach the canonical store.
	restored, resumeErr := runtime.options.Sessions.Resume(runtime.ctx, runtime.options.SessionID)
	if resumeErr != nil {
		runtime.queue = append(append([]submission(nil), task.pending...), runtime.queue...)
		runtime.failed = errors.Join(err, resumeErr)
		runtime.mailbox.publish(RuntimeEvent{Kind: EventTaskError, Generation: task.generation, Err: runtime.failed})
		return
	}
	seen := make(map[inbox.ID]struct{}, len(restored.ExternalInputIDs))
	for _, id := range restored.ExternalInputIDs {
		seen[id] = struct{}{}
	}
	for index := len(task.pending) - 1; index >= 0; index-- {
		value := task.pending[index]
		if _, stored := seen[value.input.ID]; !stored {
			runtime.queue = append([]submission{value}, runtime.queue...)
		}
	}
	if err != nil {
		runtime.failed = err
		runtime.mailbox.publish(RuntimeEvent{Kind: EventTaskError, Generation: task.generation, Err: err})
		return
	}
	runtime.mailbox.publish(RuntimeEvent{Kind: EventTaskIdle, Generation: task.generation})
	if len(runtime.queue) == 0 || runtime.closed || task.stopped {
		return
	}
	next := runtime.queue[0]
	if err := runtime.startLocked(next); err != nil {
		runtime.failed = err
		runtime.mailbox.publish(RuntimeEvent{Kind: EventTaskError, Generation: task.generation, Err: err})
		return
	}
	runtime.queue = runtime.queue[1:]
}

func (runtime *Runtime) Stop(ctx context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active == nil {
		return nil
	}
	input, err := controlInput(inbox.StopHard, "user requested stop")
	if err != nil {
		return err
	}
	if err := runtime.active.inputs.Submit(ctx, input); err != nil {
		return err
	}
	runtime.active.stopped = true
	if runtime.options.Approvals != nil {
		runtime.options.Approvals.Cancel()
	}
	return nil
}

func (runtime *Runtime) Close() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return
	}
	runtime.closed = true
	if runtime.options.Approvals != nil {
		runtime.options.Approvals.Cancel()
	}
	close(runtime.done)
	if runtime.active != nil {
		runtime.active.cancel()
	}
	if runtime.options.CloseOperations != nil {
		runtime.options.CloseOperations()
	}
}

func controlInput(mode inbox.ControlMode, reason string) (inbox.Input, error) {
	payload, err := json.Marshal(inbox.ControlMessage{Mode: mode, Reason: reason})
	if err != nil {
		return inbox.Input{}, err
	}
	return inbox.Input{ID: inbox.ID(uuid.New().String()), Kind: inbox.InputControl, Payload: payload}, nil
}

// SystemPrompt returns the effective prompt captured when the current task loaded.
func (runtime *Runtime) SystemPrompt() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.systemPrompt
}
