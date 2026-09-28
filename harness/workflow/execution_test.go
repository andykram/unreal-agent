package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

type executionFunc func(context.Context, ExecutionRequest) (ExecutionResult, error)

func (f executionFunc) Execute(ctx context.Context, r ExecutionRequest) (ExecutionResult, error) {
	return f(ctx, r)
}
func liveFixture(t *testing.T, steps []Step, state State) (*Store, string, int64) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "runs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	id, rev, err := store.Create(Graph{Version: 1, Name: "execution", ExecutionMode: "live", Steps: steps}, state)
	if err != nil {
		t.Fatal(err)
	}
	return store, id, rev
}
func TestNextPersistsIntentAndResolvedOutputs(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "required": []any{"count"}, "additionalProperties": false}
	steps := []Step{{ID: "analyze", Kind: "agent", Spec: map[string]any{"output_schema": schema}}, {ID: "consume", Kind: "agent", Needs: []string{"analyze"}, Spec: map[string]any{"inputs": map[string]any{"previous": map[string]any{"$output": map[string]any{"step": "analyze", "path": []any{}}}}}}}
	store, id, rev := liveFixture(t, steps, State{})
	calls := []string{}
	executor := executionFunc(func(ctx context.Context, r ExecutionRequest) (ExecutionResult, error) {
		_, saved, current, err := store.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if current <= rev || saved[r.Step.ID].Status != "running" || !saved[r.Step.ID].DispatchStarted || saved[r.Step.ID].ExternalKey != r.ExternalKey || r.ExternalKey == "" {
			t.Fatalf("execution before persisted intent: %+v", saved)
		}
		calls = append(calls, r.Step.ID)
		if r.Step.ID == "consume" {
			if r.Node.Inputs.(map[string]any)["previous"].(map[string]any)["count"] != json.Number("9007199254740993") {
				t.Fatalf("resolved inputs: %#v", r.Node.Inputs)
			}
		}
		return ExecutionResult{Output: map[string]any{"count": json.Number("9007199254740993")}}, nil
	})
	_, state, next, err := Next(t.Context(), store, id, rev, executor)
	if err != nil {
		t.Fatal(err)
	}
	if state["analyze"].Status != "completed" || state["analyze"].DispatchStarted {
		t.Fatalf("bad completed state: %+v", state)
	}
	rev = next
	_, state, next, err = Next(t.Context(), store, id, rev, executor)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"analyze", "consume"}) || state["consume"].Status != "completed" {
		t.Fatalf("calls %v state %+v", calls, state)
	}
	_, _, _, err = Next(t.Context(), store, id, next, executor)
	if err != nil || len(calls) != 2 {
		t.Fatalf("completed run executed again: %v %v", calls, err)
	}
}
func TestNextLeavesAmbiguousAttemptForReconciliation(t *testing.T) {
	store, id, rev := liveFixture(t, []Step{{ID: "work", Kind: "command"}}, State{})
	calls := 0
	executor := executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		calls++
		return ExecutionResult{}, context.Canceled
	})
	_, state, rev, err := Next(t.Context(), store, id, rev, executor)
	if !errors.Is(err, ErrReconciliationRequired) || state["work"].Status != "running" || !state["work"].DispatchStarted {
		t.Fatalf("ambiguous outcome: %+v %v", state, err)
	}
	_, again, next, err := Next(t.Context(), store, id, rev, executor)
	if !errors.Is(err, ErrReconciliationRequired) || calls != 1 || next != rev || !reflect.DeepEqual(state, again) {
		t.Fatalf("ambiguous attempt retried: %d %v", calls, err)
	}
}
func TestNextPersistenceFailuresDoNotAcknowledgeOrRedispatch(t *testing.T) {
	for _, when := range []string{"before dispatch", "after execution"} {
		t.Run(when, func(t *testing.T) {
			store, id, rev := liveFixture(t, []Step{{ID: "work", Kind: "command"}}, State{})
			fail := func() {
				_, err := store.db.Exec("CREATE TRIGGER fail_checkpoint BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT,'injected write failure'); END")
				if err != nil {
					t.Fatal(err)
				}
			}
			if when == "before dispatch" {
				fail()
			}
			calls := 0
			executor := executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
				calls++
				fail()
				return ExecutionResult{Output: "done"}, nil
			})
			_, returned, current, err := Next(t.Context(), store, id, rev, executor)
			if err == nil {
				t.Fatal("expected checkpoint error")
			}
			_, saved, savedRevision, loadErr := store.Load(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if current != savedRevision || !reflect.DeepEqual(returned, saved) {
				t.Fatalf("returned uncommitted state: returned=%+v saved=%+v", returned, saved)
			}
			if when == "before dispatch" {
				if calls != 0 || len(saved) != 0 {
					t.Fatalf("dispatched without durable intent: %d %+v", calls, saved)
				}
			} else {
				if calls != 1 || saved["work"].Status != "running" || !saved["work"].DispatchStarted {
					t.Fatalf("lost ambiguous dispatched state: %d %+v", calls, saved)
				}
			}
		})
	}
}
func TestNextRunsBoundedRepairAttempts(t *testing.T) {
	store, id, rev := liveFixture(t, []Step{{ID: "verify", Kind: "repeat_check", Spec: map[string]any{"max_repairs": float64(1)}}}, State{})
	var phases, keys []string
	executor := executionFunc(func(_ context.Context, r ExecutionRequest) (ExecutionResult, error) {
		phases = append(phases, r.Node.Phase)
		keys = append(keys, r.ExternalKey)
		code := 0
		if r.Node.Phase == "check" {
			code = 1
		}
		return ExecutionResult{ExitCode: code}, nil
	})
	var state State
	for range 3 {
		var err error
		_, state, rev, err = Next(t.Context(), store, id, rev, executor)
		if err != nil {
			t.Fatal(err)
		}
	}
	node := state["verify"]
	if !reflect.DeepEqual(phases, []string{"check", "repair", "check"}) || node.Status != "completed" || node.Outcome != "failed" || node.Repairs != 1 {
		t.Fatalf("repair result %+v phases %v", node, phases)
	}
	if keys[0] == keys[1] || keys[1] == keys[2] || keys[0] == keys[2] {
		t.Fatalf("attempt key reuse: %v", keys)
	}
}
func TestApproveAndJoinNeverInvokeExecutor(t *testing.T) {
	steps := []Step{{ID: "check", Kind: "repeat_check", Spec: map[string]any{"max_repairs": float64(0)}}, {ID: "yes", Kind: "approval", Needs: []string{"check"}, When: &Condition{Step: "check", Outcome: "passed"}}, {ID: "no", Kind: "approval", Needs: []string{"check"}, When: &Condition{Step: "check", Outcome: "failed"}}, {ID: "merge", Kind: "join", Needs: []string{"yes", "no"}}}
	store, id, rev := liveFixture(t, steps, State{"check": {Status: "completed", Outcome: "passed"}})
	_, state, rev, err := Next(t.Context(), store, id, rev, nil)
	if !errors.Is(err, ErrAwaitingApproval) || state["no"].Status != "skipped" {
		t.Fatalf("approval gate: %+v %v", state, err)
	}
	_, _, rev, err = Approve(store, id, rev, "yes")
	if err != nil {
		t.Fatal(err)
	}
	_, state, _, err = Next(t.Context(), store, id, rev, nil)
	if err != nil || state["merge"].Status != "completed" {
		t.Fatalf("join failed %+v %v", state, err)
	}
}
func TestNextRejectsStaleAndSimulationRuns(t *testing.T) {
	store, id, rev := liveFixture(t, []Step{{ID: "work", Kind: "command"}}, State{})
	executor := executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		t.Fatal("executor called")
		return ExecutionResult{}, nil
	})
	if _, _, _, err := Next(t.Context(), store, id, rev+1, executor); !errors.Is(err, ErrStaleRunRevision) {
		t.Fatalf("stale revision: %v", err)
	}
	id, rev, err := store.Create(Graph{Version: 1, Name: "simulation", Steps: []Step{{ID: "work", Kind: "command"}}}, State{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Next(t.Context(), store, id, rev, executor); err == nil {
		t.Fatal("simulation accepted as live")
	}
}
func TestNextRejectsBadOutputAndCommandFailure(t *testing.T) {
	for _, kind := range []string{"agent", "command"} {
		t.Run(kind, func(t *testing.T) {
			spec := map[string]any{}
			if kind == "agent" {
				spec["output_schema"] = map[string]any{"type": "integer"}
			}
			store, id, rev := liveFixture(t, []Step{{ID: "work", Kind: kind, Spec: spec}}, State{})
			executor := executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
				if kind == "command" {
					return ExecutionResult{ExitCode: 2}, nil
				}
				return ExecutionResult{Output: "not an integer"}, nil
			})
			_, state, _, err := Next(t.Context(), store, id, rev, executor)
			if err == nil || state["work"].Status != "failed" || state["work"].DispatchStarted {
				t.Fatalf("failed result not persisted: %+v %v", state, err)
			}
		})
	}
}

func TestDefiniteRejectionDoesNotRepairOrReplay(t *testing.T) {
	store, id, revision := liveFixture(t, []Step{{ID: "verify", Kind: "repeat_check", Spec: map[string]any{"max_repairs": float64(2)}}}, State{})
	calls := 0
	executor := executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		calls++
		return ExecutionResult{}, errors.Join(ErrNotDispatched, errors.New("user denied command approval"))
	})
	_, state, revision, err := Next(t.Context(), store, id, revision, executor)
	node := state["verify"]
	if !errors.Is(err, ErrNotDispatched) || errors.Is(err, ErrReconciliationRequired) || node.Status != "failed" || node.DispatchStarted || node.Phase != "" || node.Repairs != 0 {
		t.Fatalf("definite rejection was treated as ambiguous or repairable: %+v %v", node, err)
	}
	_, _, _, err = Next(t.Context(), store, id, revision, executor)
	if calls != 1 || !errors.Is(err, ErrNoReadyStep) {
		t.Fatalf("denied operation replayed: calls=%d error=%v", calls, err)
	}
}
