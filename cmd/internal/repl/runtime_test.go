package repl

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type runtimeCall struct {
	ctx      context.Context
	request  llm.Request
	response chan llm.Response
}

type runtimeAdapter struct{ calls chan runtimeCall }

type heldOperationManager struct {
	adds    chan operation.Operation
	updates chan operation.Operation
}

func (manager *heldOperationManager) Add(value operation.Operation) error {
	manager.adds <- value
	return nil
}
func (manager *heldOperationManager) Cancel(operation.ID, string) error   { return nil }
func (manager *heldOperationManager) Updates() <-chan operation.Operation { return manager.updates }

type heldToolTranslator struct{}

func (heldToolTranslator) Translate(ctx tool.Context, _ llm.ToolCall) tool.CallStatus {
	spec, _ := operation.NewValueSpec(jsontext.Value(`1`))
	id := ctx.Submit(spec)
	return tool.CallStatus{WaitingFor: []operation.ID{id}}
}
func (heldToolTranslator) TranslateResult(callID string, _ tool.CallStatus, values []operation.Operation) (llm.ToolResult, error) {
	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: string(values[0].Status)}}}, nil
}

func (adapter *runtimeAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	call := runtimeCall{ctx: ctx, request: request, response: make(chan llm.Response, 1)}
	adapter.calls <- call
	select {
	case response := <-call.response:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func newRuntimeTestHost(t *testing.T) (*Runtime, *runtimeAdapter, sessionstore.Store, session.ID) {
	t.Helper()
	store, err := localfile.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID(uuid.New().String())
	if _, err := store.Create(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeAdapter{calls: make(chan runtimeCall, 10)}
	manager := operation.NewLocalOperationManager(t.Context())
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		SessionID: id, Sessions: store, LLM: adapter, Operations: manager,
		Tools: tool.NewRegistry(tool.StaticTranslators{}),
		NewBuilder: func(string) (contextbuilder.Builder, func() bool, error) {
			builder := contextbuilder.NewBuilder()
			builder.SetModel(llm.Model{ID: "test-model"})
			return builder, nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime, adapter, store, id
}

func nextRuntimeCall(t *testing.T, adapter *runtimeAdapter) runtimeCall {
	t.Helper()
	select {
	case call := <-adapter.calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
		return runtimeCall{}
	}
}

func answerRuntimeCall(call runtimeCall) {
	call.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
}

func waitRuntimeTasks(t *testing.T, runtime *Runtime, count int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for count > 0 {
		for _, event := range runtime.DrainEvents() {
			if event.Kind == EventTaskError {
				t.Fatalf("task error: %v", event.Err)
			}
			if event.Kind == EventTaskIdle {
				count--
			}
		}
		if count == 0 {
			return
		}
		select {
		case <-runtime.Events():
		case <-deadline:
			t.Fatal("tasks did not reach idle")
		}
	}
}

func TestRuntimeQueuesPromptsUntilCoordinatorIdle(t *testing.T) {
	runtime, adapter, store, id := newRuntimeTestHost(t)
	if _, admission, err := runtime.Submit(t.Context(), "first", false); err != nil || admission != AdmissionSent {
		t.Fatalf("first admission = %q, %v", admission, err)
	}
	first := nextRuntimeCall(t, adapter)
	for _, prompt := range []string{"second", "third"} {
		if _, admission, err := runtime.Submit(t.Context(), prompt, false); err != nil || admission != AdmissionQueued {
			t.Fatalf("queue %q = %q, %v", prompt, admission, err)
		}
	}
	if runtime.QueueLength() != 2 {
		t.Fatalf("queue length = %d", runtime.QueueLength())
	}
	select {
	case <-adapter.calls:
		t.Fatal("queued prompt started a model request while first task was active")
	default:
	}
	answerRuntimeCall(first)
	second := nextRuntimeCall(t, adapter)
	if !requestContains(second.request, "second") || requestContains(first.request, "second") {
		t.Fatal("second prompt was sent before the first task reached idle")
	}
	answerRuntimeCall(second)
	third := nextRuntimeCall(t, adapter)
	if !requestContains(third.request, "third") {
		t.Fatal("third prompt was not sent in FIFO order")
	}
	answerRuntimeCall(third)
	waitRuntimeTasks(t, runtime, 3)
	state, err := store.Resume(t.Context(), id)
	if err != nil || len(state.ExternalInputIDs) != 3 {
		t.Fatalf("stored input IDs = %#v, %v", state.ExternalInputIDs, err)
	}
}

func TestRuntimeSteeringInterruptsCurrentModel(t *testing.T) {
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	if _, _, err := runtime.Submit(t.Context(), "first", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	if _, admission, err := runtime.Submit(t.Context(), "steer now", true); err != nil || admission != AdmissionSent {
		t.Fatalf("steering admission = %q, %v", admission, err)
	}
	second := nextRuntimeCall(t, adapter)
	if first.ctx.Err() == nil || !requestContains(second.request, "steer now") {
		t.Fatal("steering did not cancel and replace the active request")
	}
	answerRuntimeCall(second)
	waitRuntimeTasks(t, runtime, 1)
}

func TestRuntimeQueueWaitsForToolAndFollowup(t *testing.T) {
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	manager := &heldOperationManager{adds: make(chan operation.Operation, 1), updates: make(chan operation.Operation, 1)}
	runtime.options.Operations = manager
	runtime.options.Tools = tool.NewRegistry(tool.StaticTranslators{ViewImage: heldToolTranslator{}}, tool.ViewImageName)
	if _, _, err := runtime.Submit(t.Context(), "first", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	if _, admission, err := runtime.Submit(t.Context(), "queued second", false); err != nil || admission != AdmissionQueued {
		t.Fatalf("queue admission = %q, %v", admission, err)
	}
	first.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "held", Name: tool.ViewImageName, Arguments: `{}`}}}}
	var held operation.Operation
	select {
	case held = <-manager.adds:
	case <-time.After(3 * time.Second):
		t.Fatal("tool operation did not start")
	}
	select {
	case <-adapter.calls:
		t.Fatal("model request started while tool was still running")
	default:
	}
	held.Status = operation.StatusCompleted
	manager.updates <- held
	followup := nextRuntimeCall(t, adapter)
	if requestContains(followup.request, "queued second") {
		t.Fatal("queued prompt entered tool followup request")
	}
	answerRuntimeCall(followup)
	queued := nextRuntimeCall(t, adapter)
	if !requestContains(queued.request, "queued second") {
		t.Fatal("queued prompt did not start after tool followup reached idle")
	}
	answerRuntimeCall(queued)
	waitRuntimeTasks(t, runtime, 2)
}

func requestContains(request llm.Request, content string) bool {
	for _, item := range request.Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message := item.Data.(llm.Message)
		if message.Role == llm.RoleUser && strings.Contains(message.Text, content) {
			return true
		}
	}
	return false
}

func TestRuntimeHardStopRetainsQueue(t *testing.T) {
	runtime, adapter, store, id := newRuntimeTestHost(t)
	if _, _, err := runtime.Submit(t.Context(), "first", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	if _, _, err := runtime.Submit(t.Context(), "queued", false); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if first.ctx.Err() == nil {
		select {
		case <-first.ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("hard stop did not cancel model")
		}
	}
	waitRuntimeTasks(t, runtime, 1)
	if runtime.QueueLength() != 1 {
		t.Fatalf("queue after stop = %d, want 1", runtime.QueueLength())
	}
	select {
	case <-adapter.calls:
		t.Fatal("hard stop started queued prompt")
	default:
	}
	state, err := store.Resume(t.Context(), id)
	if err != nil || len(state.ExternalInputIDs) != 1 {
		t.Fatalf("stored input IDs after stop = %#v, %v", state.ExternalInputIDs, err)
	}
}
