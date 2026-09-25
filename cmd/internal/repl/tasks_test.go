package repl

import (
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func taskTestRuntime(t *testing.T, store sessionstore.Store, id session.ID) (*Runtime, *runtimeAdapter) {
	t.Helper()
	adapter := &runtimeAdapter{calls: make(chan runtimeCall, 10)}
	registry := taskRegistry{Registry: tool.NewRegistry(tool.StaticTranslators{})}
	handler := &taskHandler{ctx: t.Context(), store: store, sessionID: id, updates: make(chan operation.Operation, 128)}
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{SessionID: id, Sessions: store, LLM: adapter, Tools: registry, Operations: operation.NewLocalOperationManager(t.Context(), handler), NewBuilder: func(string) (contextbuilder.Builder, func() bool, error) {
		builder := contextbuilder.NewBuilder()
		builder.SetModel(llm.Model{ID: "test"})
		for _, definition := range registry.StaticDefinitions() {
			builder.AddTool(definition.Tool)
		}
		return builder, nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime, adapter
}

func TestTaskToolsPersistAndForkIndependently(t *testing.T) {
	directory := t.TempDir()
	store, err := localfile.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), "parent"); err != nil {
		t.Fatal(err)
	}
	runtime, adapter := taskTestRuntime(t, store, "parent")
	if _, _, err := runtime.Submit(t.Context(), "plan the work", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	list := taskList{Tasks: []taskItem{{ID: "inspect", Title: "Inspect the code", Status: "completed", DependsOn: []string{}, Note: "Read the relevant files"}, {ID: "fix", Title: "Fix and verify", Status: "in_progress", DependsOn: []string{"inspect"}}}}
	encoded, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	first.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "write", Name: "TaskWrite", Arguments: string(encoded)}}}}
	next := nextRuntimeCall(t, adapter)
	answerRuntimeCall(next)
	waitRuntimeTasks(t, runtime, 1)
	runtime.Close()
	page, err := store.Items(t.Context(), "parent", sessionstore.BeforeFirst, 100)
	if err != nil {
		t.Fatal(err)
	}
	var turn session.TurnID
	for _, item := range page.Items {
		if item.Kind == sessionstore.ItemTurn {
			turn = item.Data.(session.Turn).ID
		}
	}
	if _, err := store.Fork(t.Context(), "child", "parent", turn); err != nil {
		t.Fatal(err)
	}
	reopened, err := localfile.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	child, childAdapter := taskTestRuntime(t, reopened, "child")
	if _, _, err := child.Submit(t.Context(), "continue", false); err != nil {
		t.Fatal(err)
	}
	read := nextRuntimeCall(t, childAdapter)
	read.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "read", Name: "TaskRead", Arguments: `{}`}}}}
	resumed := nextRuntimeCall(t, childAdapter)
	found := false
	for _, item := range resumed.request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result := item.Data.(llm.ToolResult)
		if result.CallID != "read" {
			continue
		}
		var got taskList
		if err := json.Unmarshal([]byte(result.Output[0].Value), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, list) {
			t.Fatalf("resumed task list = %#v", got)
		}
		found = true
	}
	if !found {
		t.Fatal("TaskRead did not return the saved list")
	}
	list.Tasks[1].Status = "completed"
	list.Tasks[1].Note = "Regression test passes"
	encoded, err = json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	resumed.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "child-write", Name: "TaskWrite", Arguments: string(encoded)}}}}
	answerRuntimeCall(nextRuntimeCall(t, childAdapter))
	waitRuntimeTasks(t, child, 1)
	parentList, err := readSessionTasks(t.Context(), reopened, "parent")
	if err != nil {
		t.Fatal(err)
	}
	childList, err := readSessionTasks(t.Context(), reopened, "child")
	if err != nil {
		t.Fatal(err)
	}
	if parentList.Tasks[1].Status != "in_progress" || childList.Tasks[1].Status != "completed" {
		t.Fatal("child task update leaked to parent or was lost")
	}
	if !taskListActive(parentList) || taskListActive(childList) {
		t.Fatal("stored active state did not survive fork and child completion")
	}
	uiState, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer uiState.Close()
	uiState.Store = reopened
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		active bool
	}{{"parent", true}, {"child", false}} {
		uiState.Current = SessionMetadata{SessionID: tc.id, Workspace: uiState.Workspace, Name: tc.id}
		ui := newUI(t.Context(), getenv, 0, uiState, config, nil, nil, nil, nil)
		ui.noColor = true
		ui.width, ui.height = 80, 24
		if taskListActive(ui.tasks) != tc.active || !ui.taskVisible() || !strings.Contains(ui.View().Content, "◇  TASKS") {
			t.Fatalf("reopened %s active=%t visible=%t view=%q", tc.id, taskListActive(ui.tasks), ui.taskVisible(), ui.View().Content)
		}
		if tc.id == "child" {
			ui.restoreTaskList(ui.tasks, time.Now().Add(-taskHold-taskFrame*taskFadeFrames))
			if ui.taskVisible() {
				t.Fatal("expired completed list reappeared")
			}
		}
	}
}

func TestTaskOrderingAndCompletionEvidence(t *testing.T) {
	for _, list := range []taskList{
		{Tasks: []taskItem{{ID: "b", Title: "Later", Status: "pending", DependsOn: []string{"a"}}, {ID: "a", Title: "First", Status: "pending"}}},
		{Tasks: []taskItem{{ID: "a", Title: "First", Status: "pending"}, {ID: "b", Title: "Later", Status: "in_progress", DependsOn: []string{"a"}}}},
		{Tasks: []taskItem{{ID: "a", Title: "Done", Status: "completed"}}},
		{Tasks: []taskItem{{ID: "a", Title: "Blocked", Status: "blocked"}}},
		{Tasks: []taskItem{{ID: "a", Title: "First", Status: "pending"}, {ID: "a", Title: "Duplicate", Status: "pending"}}},
	} {
		if err := validateTasks(list); err == nil {
			t.Fatalf("accepted invalid list %#v", list)
		}
	}
}

func TestTaskActiveIsDerivedFromStoredList(t *testing.T) {
	for _, tc := range []struct {
		list   taskList
		active bool
	}{
		{taskList{}, false},
		{taskList{Tasks: []taskItem{{Status: "pending"}}}, true},
		{taskList{Tasks: []taskItem{{Status: "in_progress"}}}, true},
		{taskList{Tasks: []taskItem{{Status: "blocked"}}}, true},
		{taskList{Tasks: []taskItem{{Status: "completed"}}}, false},
	} {
		if got := taskListActive(tc.list); got != tc.active {
			t.Fatalf("active for %#v = %t", tc.list, got)
		}
	}
}

func TestCompletedTaskWriteRecognizesOnlyCommittedWrites(t *testing.T) {
	plan := taskPlan{Action: "TaskWrite", List: taskList{Tasks: []taskItem{{ID: "one", Status: "pending"}}}}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: taskPlanType, Version: 1, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	current := operation.Operation{ID: "write", Type: spec.Type, Version: spec.Version, State: spec.State, MaxOutputLength: spec.MaxOutputLength, Status: operation.StatusReady}
	if _, ok, err := completedTaskWrite(current); err != nil || ok {
		t.Fatalf("uncommitted write = %t %v", ok, err)
	}
	current.Status = operation.StatusCompleted
	list, ok, err := completedTaskWrite(current)
	if err != nil || !ok || !taskListActive(list) {
		t.Fatalf("committed write = %#v %t %v", list, ok, err)
	}
}

func TestTaskRailUpdatesFromCompletedRuntimeToolCall(t *testing.T) {
	directory := t.TempDir()
	store, err := localfile.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	runtime, adapter := taskTestRuntime(t, store, "session")
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	state.Store = store
	state.Current = SessionMetadata{SessionID: "session", Workspace: state.Workspace, Name: "session"}
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	ui := newUI(t.Context(), getenv, 0, state, config, nil, nil, runtime, nil)
	ui.noColor, ui.width, ui.height = true, 80, 24
	if _, _, err := runtime.Submit(t.Context(), "make a list", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	list := taskList{Tasks: []taskItem{{ID: "one", Title: "Running check", Status: "in_progress"}}}
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	first.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "write", Name: "TaskWrite", Arguments: string(data)}}}}
	followup := nextRuntimeCall(t, adapter)
	events := runtime.DrainEvents()
	ui.Update(runtimeEventsMsg{runtime: runtime, events: events})
	if !ui.taskVisible() || !strings.Contains(ui.View().Content, "Running check") {
		t.Fatalf("task rail did not update: %q", ui.View().Content)
	}
	generation := ui.taskGeneration
	for _, event := range events {
		if event.Kind == EventItem && event.Item.Kind == sessionstore.ItemToolCallStatus {
			ui.appendItem(event.Item)
		}
	}
	if ui.taskGeneration != generation {
		t.Fatal("duplicate status restarted task animation")
	}
	answerRuntimeCall(followup)
	waitRuntimeTasks(t, runtime, 1)
}
