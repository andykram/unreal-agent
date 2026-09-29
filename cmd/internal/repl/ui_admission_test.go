package repl

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestUIEnterQueuesWhileAltEnterSteers(t *testing.T) {
	runtime, adapter, stored, id := newRuntimeTestHost(t)
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state.Store = stored.(*localfile.Store)
	state.Current = SessionMetadata{SessionID: string(id), Workspace: state.Workspace, Name: "test"}
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(context.Background(), getenv, time.Minute, state, config, nil, nil, runtime, nil)
	if err := model.draft.Set("first"); err != nil {
		t.Fatal(err)
	}
	model.submit(false)
	first := nextRuntimeCall(t, adapter)
	model.Update(runtimeEventsMsg{runtime: runtime, events: runtime.DrainEvents()})
	if model.pending != nil {
		t.Fatal("first accepted input did not release the composer")
	}
	if err := model.draft.Set("queued second"); err != nil {
		t.Fatal(err)
	}
	model.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if runtime.QueueLength() != 1 || model.draft.Source() != "" {
		t.Fatalf("Enter did not queue and clear draft: queue=%d draft=%q", runtime.QueueLength(), model.draft.Source())
	}
	if err := model.draft.Set("steer now"); err != nil {
		t.Fatal(err)
	}
	model.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	steered := nextRuntimeCall(t, adapter)
	if first.ctx.Err() == nil || !requestContains(steered.request, "steer now") || runtime.QueueLength() != 1 {
		t.Fatalf("Alt+Enter did not steer while retaining the queue: first=%v request=%#v queue=%d", first.ctx.Err(), steered.request, runtime.QueueLength())
	}
	answerRuntimeCall(steered)
	queued := nextRuntimeCall(t, adapter)
	if !requestContains(queued.request, "queued second") {
		t.Fatal("queued prompt did not run after steered task")
	}
	answerRuntimeCall(queued)
	waitRuntimeTasks(t, runtime, 2)
}

func TestQueuedPromptsStayGroupedAboveComposerUntilSent(t *testing.T) {
	runtime, adapter, stored, id := newRuntimeTestHost(t)
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	state.Store = stored.(*localfile.Store)
	state.Current = SessionMetadata{SessionID: string(id), Workspace: state.Workspace, Name: "test"}
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, runtime, nil)
	model.noColor, model.width, model.height = true, 80, 24
	if _, _, err := runtime.Submit(t.Context(), "first", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	for _, prompt := range []string{"second in queue", "third in queue", "fourth in queue"} {
		if _, admission, err := runtime.Submit(t.Context(), prompt, false); err != nil || admission != AdmissionQueued {
			t.Fatalf("queue %q = %q %v", prompt, admission, err)
		}
	}
	view := model.View()
	second, third, fourth, composer := strings.Index(view.Content, "second in queue"), strings.Index(view.Content, "third in queue"), strings.Index(view.Content, "fourth in queue"), strings.Index(view.Content, "Compose")
	if second < 0 || third <= second || fourth <= third || composer <= fourth || !strings.Contains(view.Content, "QUEUED") {
		t.Fatalf("queue not together above composer: %q", view.Content)
	}
	if len(model.transcript) != 0 {
		t.Fatal("unsent prompts were added to transcript")
	}
	answerRuntimeCall(first)
	secondCall := nextRuntimeCall(t, adapter)
	if queue := runtime.QueuedPrompts(); len(queue) != 2 || queue[0] != "third in queue" || queue[1] != "fourth in queue" {
		t.Fatalf("queue after send = %#v", queue)
	}
	view = model.View()
	if strings.Contains(view.Content, "↳ second in queue") || !strings.Contains(view.Content, "↳ third in queue") {
		t.Fatalf("sent item remained in queue: %q", view.Content)
	}
	answerRuntimeCall(secondCall)
	answerRuntimeCall(nextRuntimeCall(t, adapter))
	answerRuntimeCall(nextRuntimeCall(t, adapter))
	waitRuntimeTasks(t, runtime, 4)
	if runtime.QueueLength() != 0 || strings.Contains(model.View().Content, "QUEUED") {
		t.Fatal("queue remained after all prompts sent")
	}
}
