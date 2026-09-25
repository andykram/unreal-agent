package repl

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
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

func TestAskUserMultipleFormsWaitForCanonicalAcknowledgement(t *testing.T) {
	handler := newAskUserHandler()
	for _, id := range []operation.ID{"first", "second"} {
		plan := AskUserPlan{CallID: string(id), Form: AskUserArgs{Title: "Choose", Questions: []Question{{ID: "answer", Prompt: "Answer?", Kind: "text", Required: true}}}}
		encoded, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: askUserPlanType, Version: askUserPlanVersion, Data: jsontext.Value(encoded)})
		if err != nil {
			t.Fatal(err)
		}
		current := operation.Operation{ID: id, Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: spec.MaxOutputLength}
		if err := handler.AddRemoteJob(current); err != nil {
			t.Fatal(err)
		}
		if err := handler.AddRemoteJob(current); err != nil {
			t.Fatal(err)
		}
	}
	if got := handler.Questions(); len(got) != 2 || got[0].OperationID != "first" || got[1].OperationID != "second" {
		t.Fatalf("pending order = %#v", got)
	}
	answer := AskUserResult{Status: "answered", Answers: []QuestionAnswer{{QuestionID: "answer", Text: "yes"}}}
	if err := handler.Submit("first", answer); err != nil {
		t.Fatal(err)
	}
	if handler.Pending() != 2 {
		t.Fatal("gate opened before canonical acknowledgement")
	}
	if err := handler.Submit("first", answer); err == nil {
		t.Fatal("duplicate answer accepted")
	}
	handler.Ack(sessionstore.Item{Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{Operations: []operation.Operation{{ID: "first", Status: operation.StatusCompleted}}}})
	if handler.Pending() != 1 || handler.Questions()[0].OperationID != "second" {
		t.Fatalf("remaining questions = %#v", handler.Questions())
	}
	if err := handler.Submit("first", answer); err == nil {
		t.Fatal("stale answer accepted")
	}
}

func TestAskUserValidation(t *testing.T) {
	form := AskUserArgs{Title: "Choose", Questions: []Question{{ID: "choice", Prompt: "Which?", Kind: "single_select", Required: true, Options: []QuestionOption{{ID: "a", Label: "A"}}, Recommended: []string{"a"}}}}
	if err := validateAskUserForm(form); err != nil {
		t.Fatal(err)
	}
	for _, result := range []AskUserResult{{Status: "answered"}, {Status: "answered", Answers: []QuestionAnswer{{QuestionID: "choice", Selected: []string{"missing"}}}}, {Status: "dismissed", Answers: []QuestionAnswer{{QuestionID: "choice", Selected: []string{"a"}}}}} {
		if err := validateAskUserResult(form, result); err == nil {
			t.Fatalf("accepted invalid result %#v", result)
		}
	}
	form.Questions[0].Recommended = []string{"missing"}
	if err := validateAskUserForm(form); err == nil {
		t.Fatal("accepted unknown recommended option")
	}
	form.Questions[0].Recommended = nil
	form.Questions = append(form.Questions, form.Questions[0])
	if err := validateAskUserForm(form); err == nil {
		t.Fatal("accepted duplicate question ID")
	}
}

func questionTestRuntime(t *testing.T, store sessionstore.Store, id session.ID) (*Runtime, *runtimeAdapter) {
	t.Helper()
	adapter := &runtimeAdapter{calls: make(chan runtimeCall, 10)}
	questions := newAskUserHandler()
	registry := askUserRegistry{Registry: tool.NewRegistry(tool.StaticTranslators{})}
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		SessionID: id, Sessions: store, LLM: adapter, Tools: registry,
		Operations: operation.NewLocalOperationManager(t.Context(), questions), Questions: questions,
		NewBuilder: func(string) (contextbuilder.Builder, func() bool, error) {
			builder := contextbuilder.NewBuilder()
			builder.SetModel(llm.Model{ID: "test-model"})
			for _, definition := range registry.StaticDefinitions() {
				builder.AddTool(definition.Tool)
			}
			return builder, func() bool { return questions.Pending() == 0 }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime, adapter
}

func TestAskUserRestoresUnansweredForm(t *testing.T) {
	store, err := localfile.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID(uuid.New().String())
	if _, err := store.Create(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	firstRuntime, firstAdapter := questionTestRuntime(t, store, id)
	if _, _, err := firstRuntime.Submit(t.Context(), "start", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, firstAdapter)
	first.response <- llm.Response{ID: "response-question", Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{
		CallID: "question-call", Name: askUserName,
		Arguments: `{"title":"Choose","questions":[{"id":"choice","prompt":"Which?","kind":"single_select","required":true,"allow_other":false,"options":[{"id":"a","label":"A"}]}]}`,
	}}}}
	deadline := time.After(3 * time.Second)
	for len(firstRuntime.Questions()) != 1 {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("first question did not become pending")
		}
	}
	firstRuntime.Close()
	for {
		firstRuntime.mu.Lock()
		active := firstRuntime.active != nil
		firstRuntime.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("first runtime did not stop")
		}
	}
	resumedRuntime, resumedAdapter := questionTestRuntime(t, store, id)
	if err := resumedRuntime.Recover(); err != nil {
		t.Fatal(err)
	}
	for len(resumedRuntime.Questions()) != 1 {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("unanswered form did not restore")
		}
	}
	select {
	case <-resumedAdapter.calls:
		t.Fatal("recovery called model before answer")
	case <-time.After(100 * time.Millisecond):
	}
	pending := resumedRuntime.Questions()[0]
	if err := resumedRuntime.AnswerQuestion(pending.OperationID, AskUserResult{Status: "answered", Answers: []QuestionAnswer{{QuestionID: "choice", Selected: []string{"a"}}}}); err != nil {
		t.Fatal(err)
	}
	followup := nextRuntimeCall(t, resumedAdapter)
	answerRuntimeCall(followup)
	waitRuntimeTasks(t, resumedRuntime, 1)
}

func TestAskUserPausesRealCoordinatorUntilAnswerIsPersisted(t *testing.T) {
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	questions := newAskUserHandler()
	manager := operation.NewLocalOperationManager(t.Context(), questions)
	registry := askUserRegistry{Registry: tool.NewRegistry(tool.StaticTranslators{})}
	runtime.options.Questions = questions
	runtime.options.Operations = manager
	runtime.options.Tools = registry
	runtime.options.NewBuilder = func(string) (contextbuilder.Builder, func() bool, error) {
		builder := contextbuilder.NewBuilder()
		builder.SetModel(llm.Model{ID: "test-model"})
		for _, definition := range registry.StaticDefinitions() {
			builder.AddTool(definition.Tool)
		}
		return builder, func() bool { return questions.Pending() == 0 }, nil
	}
	if _, _, err := runtime.Submit(t.Context(), "start", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	first.response <- llm.Response{ID: "response-question", Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{
		CallID: "question-call", Name: askUserName,
		Arguments: `{"title":"Choose","questions":[{"id":"choice","prompt":"Which?","kind":"single_select","required":true,"allow_other":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`,
	}}}}
	deadline := time.After(3 * time.Second)
	for questions.Pending() != 1 {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("question operation was not registered")
		}
	}
	select {
	case <-adapter.calls:
		t.Fatal("model request started before question answer")
	case <-time.After(100 * time.Millisecond):
	}
	pending := runtime.Questions()
	if len(pending) != 1 || pending[0].Plan.CallID != "question-call" {
		t.Fatalf("pending = %#v", pending)
	}
	if err := runtime.AnswerQuestion(pending[0].OperationID, AskUserResult{Status: "answered", Answers: []QuestionAnswer{{QuestionID: "choice", Selected: []string{"b"}}}}); err != nil {
		t.Fatal(err)
	}
	if questions.Pending() != 1 {
		t.Fatal("gate opened before durable tool status")
	}
	followup := nextRuntimeCall(t, adapter)
	if questions.Pending() != 0 {
		t.Fatal("gate remained closed after durable tool status")
	}
	found := false
	for _, item := range followup.request.Input {
		if item.Type == llm.ItemToolResult && strings.Contains(item.Data.(llm.ToolResult).Output[0].Value, `"selected":["b"]`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("answer missing from first resumed request: %#v", followup.request.Input)
	}
	answerRuntimeCall(followup)
	waitRuntimeTasks(t, runtime, 1)
}
