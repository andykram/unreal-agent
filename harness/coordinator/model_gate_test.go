package coordinator

import (
	"testing"
	"testing/synctest"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func TestCoordinatorModelGatePausesUntilAllOperationsComplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newStopTestRun(t, 2)
		pendingQuestions := 2
		run.current.dependencies.CanRequestModel = func() bool { return pendingQuestions == 0 }
		run.start(t)
		run.input(t, externalEvent(t, 0, "input", "answer after both questions"))
		run.input(t, stopInput(t, "stop", inbox.StopWhenIdle))
		if len(run.calls) != 0 || len(run.store.appendedTurns) != 0 {
			t.Fatal("gated request called the model or persisted a turn")
		}
		run.assertRunning(t)

		pendingQuestions--
		run.update(t, 0, operation.StatusCompleted)
		if len(run.calls) != 0 || len(run.store.appendedTurns) != 0 || len(run.store.appendedStatuses) == 0 {
			t.Fatal("first completion did not persist while model requests stayed gated")
		}
		run.assertRunning(t)

		pendingQuestions--
		run.update(t, 1, operation.StatusCompleted)
		if len(run.calls) != 1 || len(run.store.appendedTurns) != 1 {
			t.Fatal("final completion did not start exactly one model request")
		}
		assertStopResult(t, run.calls[0].request, "call-0", string(operation.StatusCompleted))
		assertStopResult(t, run.calls[0].request, "call-1", string(operation.StatusCompleted))
		run.respond(t, 0, textResponse("Both answers received."))
		run.assertStopped(t)
	})
}

func TestCoordinatorModelGateDoesNotBlockHardStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newStopTestRun(t, 1)
		run.current.dependencies.CanRequestModel = func() bool { return false }
		run.start(t)
		run.input(t, externalEvent(t, 0, "input", "hello"))
		run.input(t, stopInput(t, "stop", inbox.StopHard))
		run.update(t, 0, operation.StatusCanceled)
		run.assertStopped(t)
		if len(run.calls) != 0 || len(run.store.appendedTurns) != 0 {
			t.Fatal("hard stop started a gated model request")
		}
	})
}
