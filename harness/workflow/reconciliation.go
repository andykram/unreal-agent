package workflow

import (
	"fmt"
	"strings"
	"time"
)

// Reconciliation records an operator's explicit decision about an uncertain effect.
// Retry authorizes another dispatch of the same attempt; it cannot prevent duplicate
// effects in executors that do not honor the external idempotency key.
type Reconciliation struct {
	Action string
	Result ExecutionResult
	Reason string
}

type ReconciliationRecord struct {
	At          time.Time
	Action      string
	Reason      string
	ExternalKey string
	Phase       string
}

// Reconcile never dispatches an operation. Invalid results and stale revisions
// leave the checkpoint untouched. Accepting a known unsuccessful external outcome
// succeeds here while retaining the normal command failure or repair transition.
func Reconcile(store *Store, runID string, expectedRevision int64, stepID string, decision Reconciliation) (Graph, State, int64, error) {
	graph, state, revision, err := loadLive(store, runID, expectedRevision)
	if err != nil {
		return graph, state, revision, err
	}
	decision.Reason = strings.TrimSpace(decision.Reason)
	if decision.Reason == "" {
		return graph, state, revision, fmt.Errorf("reconciliation requires a reason")
	}
	if decision.Action != "accept" && decision.Action != "fail" && decision.Action != "retry" {
		return graph, state, revision, fmt.Errorf("unknown reconciliation action %q", decision.Action)
	}
	var step Step
	for _, candidate := range graph.Steps {
		if candidate.ID == stepID {
			step = candidate
			break
		}
	}
	node := state[stepID]
	if step.ID == "" || node.Status != "running" || !node.DispatchStarted {
		return graph, state, revision, fmt.Errorf("step %s has no dispatched operation to reconcile", stepID)
	}
	if decision.Action == "accept" {
		decision.Result.Output, err = normalizeOutput(decision.Result.Output)
		if err == nil {
			err = validateCompletion(step, decision.Result)
		}
		if err != nil {
			return graph, state, revision, fmt.Errorf("invalid reconciliation result: %w", err)
		}
	}
	updated, err := copyState(state)
	if err != nil {
		return graph, state, revision, err
	}
	node = updated[stepID]
	node.Reconciliations = append(node.Reconciliations, ReconciliationRecord{At: time.Now().UTC(), Action: decision.Action, Reason: decision.Reason, ExternalKey: node.ExternalKey, Phase: node.Phase})
	node.History = append(node.History, "reconciliation "+decision.Action+": "+decision.Reason)
	updated[stepID] = node
	switch decision.Action {
	case "accept":
		_ = completeExecution(graph, updated, step, decision.Result)
		AssignExecutionKeys(graph, updated, runID)
		Settle(graph, updated)
	case "fail":
		node.Status, node.Phase, node.DispatchStarted = "failed", "", false
		updated[stepID] = node
		Settle(graph, updated)
	case "retry":
		node.DispatchStarted = false
		updated[stepID] = node
	}
	next, err := store.Save(runID, revision, updated)
	if err != nil {
		return graph, state, revision, err
	}
	return graph, updated, next, nil
}
