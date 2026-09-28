package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func uncertainFixture(t *testing.T, step Step) (*Store, string, int64, Node) {
	t.Helper()
	store, id, rev := liveFixture(t, []Step{step}, State{})
	_, state, rev, err := Next(t.Context(), store, id, rev, executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("lost connection")
	}))
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatal(err)
	}
	return store, id, rev, state[step.ID]
}

func TestReconcileRetryPreservesAttemptAndInputs(t *testing.T) {
	step := Step{ID: "run", Kind: "command", Spec: map[string]any{"inputs": map[string]any{"count": json.Number("9007199254740993")}}}
	store, id, rev, before := uncertainFixture(t, step)
	_, state, next, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "retry", Reason: "confirmed executor never received request"})
	if err != nil {
		t.Fatal(err)
	}
	node := state["run"]
	if node.DispatchStarted || node.Status != "running" || node.AttemptKey != before.AttemptKey || node.ExternalKey != before.ExternalKey || !reflect.DeepEqual(node.Inputs, before.Inputs) {
		t.Fatalf("changed attempt: %+v", node)
	}
	if len(node.Reconciliations) != 1 || node.Reconciliations[0].Phase != "execute" || node.Reconciliations[0].ExternalKey != before.ExternalKey || node.Reconciliations[0].At.IsZero() || len(node.History) != len(before.History)+1 {
		t.Fatalf("missing audit: %+v", node)
	}
	calls := 0
	_, state, _, err = Next(t.Context(), store, id, next, executionFunc(func(_ context.Context, r ExecutionRequest) (ExecutionResult, error) {
		calls++
		if r.ExternalKey != before.ExternalKey || r.Node.AttemptKey != before.AttemptKey || !reflect.DeepEqual(r.Node.Inputs, before.Inputs) {
			t.Fatalf("retry changed identity: %+v", r)
		}
		return ExecutionResult{}, nil
	}))
	if err != nil || calls != 1 || state["run"].Status != "completed" || len(state["run"].AttemptKeys) != 1 {
		t.Fatalf("retry: %v %+v", err, state)
	}
}

func TestReconcileRejectsInvalidAndStaleWithoutWrites(t *testing.T) {
	schema := map[string]any{"type": "integer"}
	store, id, rev, _ := uncertainFixture(t, Step{ID: "run", Kind: "agent", Spec: map[string]any{"output_schema": schema}})
	_, before, _, _ := store.Load(id)
	decisions := []Reconciliation{{Action: "accept", Reason: "verified", Result: ExecutionResult{Output: "wrong"}}, {Action: "accept", Reason: "verified", Result: ExecutionResult{Output: make(chan int)}}, {Action: "retry"}, {Action: "unknown", Reason: "verified"}}
	for _, decision := range decisions {
		if _, _, _, err := Reconcile(store, id, rev, "run", decision); err == nil {
			t.Fatalf("accepted %+v", decision)
		}
		_, after, current, _ := store.Load(id)
		if current != rev || !sameState(before, after) {
			t.Fatal("invalid decision wrote checkpoint")
		}
	}
	if _, _, _, err := Reconcile(store, id, rev-1, "run", Reconciliation{Action: "fail", Reason: "verified"}); !errors.Is(err, ErrStaleRunRevision) {
		t.Fatal(err)
	}
	_, state, next, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "verified", Result: ExecutionResult{Output: json.Number("9007199254740993")}})
	if err != nil || state["run"].Output != json.Number("9007199254740993") || state["run"].Status != "completed" {
		t.Fatalf("accept: %v %+v", err, state)
	}
	if _, _, _, err := Reconcile(store, id, next, "run", Reconciliation{Action: "retry", Reason: "again"}); err == nil {
		t.Fatal("reconciled completed operation")
	}
}

func TestReconcileWorktreeRequiresAbsolutePath(t *testing.T) {
	store, id, rev, _ := uncertainFixture(t, Step{ID: "run", Kind: "worktree"})
	for _, workspace := range []string{"", "relative/path"} {
		if _, _, _, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "verified", Result: ExecutionResult{Workspace: workspace}}); err == nil {
			t.Fatal("accepted invalid workspace")
		}
	}
	_, state, _, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "verified", Result: ExecutionResult{Workspace: t.TempDir()}})
	if err != nil || state["run"].Status != "completed" {
		t.Fatal(err, state)
	}
}

func TestReconcileRepairUsesNormalCompletion(t *testing.T) {
	store, id, rev, before := uncertainFixture(t, Step{ID: "run", Kind: "repeat_check", Spec: map[string]any{"max_repairs": float64(2)}})
	_, state, rev, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "check failed", Result: ExecutionResult{ExitCode: 1}})
	if err != nil || state["run"].Phase != "repair" || state["run"].Repairs != 0 {
		t.Fatal(err, state)
	}
	_, state, rev, err = Next(t.Context(), store, id, rev, executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("lost repair")
	}))
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatal(err)
	}
	repairKey := state["run"].ExternalKey
	_, state, rev, err = Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "repair verified"})
	node := state["run"]
	if err != nil || node.Phase != "check" || node.Repairs != 1 || len(node.Reconciliations) != 2 || node.Reconciliations[1].ExternalKey != repairKey || repairKey == before.ExternalKey {
		t.Fatal(err, state)
	}
	_, state, _, err = Next(t.Context(), store, id, rev, executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) { return ExecutionResult{}, nil }))
	if err != nil || state["run"].Status != "completed" || state["run"].Repairs != 1 || len(state["run"].AttemptKeys) != 3 {
		t.Fatal(err, state)
	}
}

func TestReconcileFailureAndAtomicCheckpoint(t *testing.T) {
	store, id, rev, before := uncertainFixture(t, Step{ID: "run", Kind: "repeat_check", Spec: map[string]any{"max_repairs": float64(2)}})
	_, err := store.db.Exec("CREATE TRIGGER reject_save BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT, 'injected write failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	_, state, current, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "fail", Reason: "operator verified unusable result"})
	if err == nil || current != rev || !reflect.DeepEqual(state["run"], before) {
		t.Fatal("acknowledged failed save", err, state)
	}
	_, saved, savedRev, _ := store.Load(id)
	if savedRev != rev || !reflect.DeepEqual(saved["run"], before) {
		t.Fatal("partial reconciliation persisted")
	}
	if _, err = store.db.Exec("DROP TRIGGER reject_save"); err != nil {
		t.Fatal(err)
	}
	_, state, next, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "fail", Reason: "operator verified unusable result"})
	if err != nil || state["run"].Status != "failed" || state["run"].Phase != "" || state["run"].DispatchStarted || state["run"].Repairs != 0 {
		t.Fatal(err, state)
	}
	_, _, _, err = Next(t.Context(), store, id, next, executionFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
		t.Fatal("failed operation replayed")
		return ExecutionResult{}, nil
	}))
	if !errors.Is(err, ErrNoReadyStep) {
		t.Fatal(err)
	}
}

func TestReconcileAcceptFailedCommandRecordsKnownOutcome(t *testing.T) {
	store, id, rev, _ := uncertainFixture(t, Step{ID: "run", Kind: "command"})
	_, state, next, err := Reconcile(store, id, rev, "run", Reconciliation{Action: "accept", Reason: "observed process exit", Result: ExecutionResult{ExitCode: 7, Output: "stderr evidence"}})
	if err != nil || next != rev+1 || state["run"].Status != "failed" || state["run"].DispatchStarted || state["run"].Output != "stderr evidence" || len(state["run"].Reconciliations) != 1 {
		t.Fatal(err, state)
	}
}
