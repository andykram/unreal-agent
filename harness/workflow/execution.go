package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

var (
	// ErrNotDispatched identifies a definite rejection before external effects.
	// Executors must not use it for transport errors or uncertain cancellation.
	ErrNotDispatched          = errors.New("external operation was not dispatched")
	ErrReconciliationRequired = errors.New("interrupted external operation requires reconciliation before continuing")
	ErrAwaitingApproval       = errors.New("workflow is awaiting approval")
	ErrNoReadyStep            = errors.New("workflow has no ready step")
)

// Executor performs one operation. A returned error has ambiguous external
// effects, so the runner retains its dispatched checkpoint and never retries it.
type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

type ExecutionRequest struct {
	RunID       string
	Step        Step
	Node        Node
	State       State
	ExternalKey string
}

type ExecutionResult struct {
	Output    any
	Workspace string
	ExitCode  int
}

// Next performs at most one external operation. Set ExecutionMode="live" before
// Store.Create. Resume uses the immutable stored graph; simulator runs are rejected.
// A dispatched running node blocks execution until explicit reconciliation
// establishes the external outcome. Successful return exposes only saved state.
func Next(ctx context.Context, store *Store, runID string, expectedRevision int64, executor Executor) (Graph, State, int64, error) {
	graph, state, revision, err := loadLive(store, runID, expectedRevision)
	if err != nil {
		return graph, state, revision, err
	}
	if err = ctx.Err(); err != nil {
		return graph, state, revision, err
	}
	for _, step := range graph.Steps {
		node := state[step.ID]
		if node.Status == "running" && node.DispatchStarted {
			return graph, state, revision, fmt.Errorf("%s: %w", step.ID, ErrReconciliationRequired)
		}
	}
	settled, err := copyState(state)
	if err != nil {
		return graph, state, revision, err
	}
	Settle(graph, settled)
	if !sameState(state, settled) {
		next, err := store.Save(runID, revision, settled)
		if err != nil {
			return graph, state, revision, err
		}
		state, revision = settled, next
	}
	awaiting := false
	for _, step := range graph.Steps {
		node := state[step.ID]
		pendingExecution := node.Status == "running" && !node.DispatchStarted && (node.Phase == "execute" || (step.Kind == "repeat_check" && (node.Phase == "check" || node.Phase == "repair")))
		if !Ready(step, state) && !pendingExecution {
			continue
		}
		if step.Kind == "approval" {
			awaiting = true
			continue
		}
		updated, err := copyState(state)
		if err != nil {
			return graph, state, revision, err
		}
		node = updated[step.ID]
		if step.Kind == "join" {
			node.Status = "completed"
			node.Outcome = "passed"
			updated[step.ID] = node
			AssignExecutionKeys(graph, updated, runID)
			next, err := store.Save(runID, revision, updated)
			if err != nil {
				return graph, state, revision, err
			}
			return graph, updated, next, nil
		}
		if executor == nil {
			return graph, state, revision, fmt.Errorf("step %s requires an executor", step.ID)
		}
		if node.Status == "" {
			inputs, err := resolveInputs(step.Spec["inputs"], state)
			if err != nil {
				return graph, state, revision, fmt.Errorf("step %s inputs: %w", step.ID, err)
			}
			node.Inputs = inputs
			node.Status = "running"
			node.Phase = "execute"
			if step.Kind == "repeat_check" {
				node.Phase = "check"
			}
		}
		node.DispatchStarted = true
		updated[step.ID] = node
		AssignExecutionKeys(graph, updated, runID)
		node = updated[step.ID]
		key, err := ExecutorKey(node, ExecutorKeyOptions{Executor: step.Kind, Operation: node.Phase})
		if err != nil {
			return graph, state, revision, err
		}
		node.ExternalKey = key
		updated[step.ID] = node
		next, err := store.Save(runID, revision, updated)
		if err != nil {
			return graph, state, revision, err
		}
		state, revision = updated, next
		// The executor receives copies so mutation cannot rewrite acknowledged state.
		executionState, err := copyState(state)
		if err != nil {
			return graph, state, revision, err
		}
		encodedStep, err := json.Marshal(step)
		if err != nil {
			return graph, state, revision, err
		}
		var executionStep Step
		if err := json.Unmarshal(encodedStep, &executionStep); err != nil {
			return graph, state, revision, err
		}
		result, executionErr := executor.Execute(ctx, ExecutionRequest{RunID: runID, Step: executionStep, Node: executionState[step.ID], State: executionState, ExternalKey: key})
		updated, err = copyState(state)
		if err != nil {
			return graph, state, revision, err
		}
		node = updated[step.ID]
		if executionErr != nil {
			if errors.Is(executionErr, ErrNotDispatched) {
				node.Status = "failed"
				node.DispatchStarted = false
				node.Phase = ""
				node.History = append(node.History, "external operation rejected before dispatch: "+executionErr.Error())
				updated[step.ID] = node
				next, saveErr := store.Save(runID, revision, updated)
				if saveErr != nil {
					return graph, state, revision, errors.Join(executionErr, saveErr)
				}
				return graph, updated, next, fmt.Errorf("step %s: %w", step.ID, executionErr)
			}
			node.History = append(node.History, "external operation outcome unknown: "+executionErr.Error())
			updated[step.ID] = node
			next, saveErr := store.Save(runID, revision, updated)
			if saveErr != nil {
				return graph, state, revision, errors.Join(executionErr, saveErr)
			}
			return graph, updated, next, fmt.Errorf("step %s: %w: %v", step.ID, ErrReconciliationRequired, executionErr)
		}
		output, err := normalizeOutput(result.Output)
		if err != nil {
			// The operation returned, but its result cannot be checkpointed safely.
			return graph, state, revision, fmt.Errorf("step %s returned an unencodable result: %w", step.ID, err)
		}
		result.Output = output
		resultErr := completeExecution(graph, updated, step, result)
		AssignExecutionKeys(graph, updated, runID)
		Settle(graph, updated)
		next, err = store.Save(runID, revision, updated)
		if err != nil {
			return graph, state, revision, err
		}
		return graph, updated, next, resultErr
	}
	if CompletedRun(graph, state) {
		return graph, state, revision, nil
	}
	if awaiting {
		return graph, state, revision, ErrAwaitingApproval
	}
	return graph, state, revision, ErrNoReadyStep
}

// Approve records an explicitly accepted, ready approval node without invoking
// the executor. It does not bypass dependencies or approve other nodes.
func Approve(store *Store, runID string, expectedRevision int64, stepID string) (Graph, State, int64, error) {
	graph, state, revision, err := loadLive(store, runID, expectedRevision)
	if err != nil {
		return graph, state, revision, err
	}
	for _, step := range graph.Steps {
		if step.ID != stepID {
			continue
		}
		if step.Kind != "approval" || !Ready(step, state) {
			return graph, state, revision, fmt.Errorf("step %s is not awaiting approval", stepID)
		}
		updated, err := copyState(state)
		if err != nil {
			return graph, state, revision, err
		}
		updated[stepID] = Node{Status: "completed", Outcome: "approved"}
		AssignExecutionKeys(graph, updated, runID)
		Settle(graph, updated)
		next, err := store.Save(runID, revision, updated)
		if err != nil {
			return graph, state, revision, err
		}
		return graph, updated, next, nil
	}
	return graph, state, revision, fmt.Errorf("unknown approval step %s", stepID)
}

func loadLive(store *Store, id string, expected int64) (Graph, State, int64, error) {
	graph, state, revision, err := store.Load(id)
	if err != nil {
		return graph, state, revision, err
	}
	if expected != revision {
		return graph, state, revision, ErrStaleRunRevision
	}
	if graph.ExecutionMode != "live" {
		return graph, state, revision, fmt.Errorf("run execution mode %q is not live", graph.ExecutionMode)
	}
	return graph, state, revision, nil
}
func copyState(state State) (State, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var copied State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err = decoder.Decode(&copied)
	if copied == nil {
		copied = State{}
	}
	return copied, err
}
func sameState(a, b State) bool {
	aa, ea := json.Marshal(a)
	bb, eb := json.Marshal(b)
	return ea == nil && eb == nil && bytes.Equal(aa, bb)
}
func normalizeOutput(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var output any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err = decoder.Decode(&output)
	return output, err
}

// completeExecution applies known external outcomes for normal execution and recovery.
// Output must already be JSON-normalized.
func completeExecution(graph Graph, updated State, step Step, result ExecutionResult) error {
	node := updated[step.ID]
	node.Output = result.Output
	node.Workspace = result.Workspace
	node.DispatchStarted = false
	var resultErr error
	switch step.Kind {
	case "repeat_check":
		if node.Phase == "repair" {
			if result.ExitCode != 0 {
				node.Status = "failed"
				node.Phase = ""
				resultErr = fmt.Errorf("repair exited with code %d", result.ExitCode)
			} else {
				node.Repairs++
				node.Phase = "check"
				node.History = append(node.History, fmt.Sprintf("repair %d completed; check %d ready", node.Repairs, node.Repairs+1))
			}
		} else {
			updated[step.ID] = node
			FinishCheck(graph, updated, step.ID, result.ExitCode == 0)
			node = updated[step.ID]
		}
	case "command":
		if result.ExitCode != 0 {
			node.Status = "failed"
			resultErr = fmt.Errorf("command exited with code %d", result.ExitCode)
		} else {
			node.Status = "completed"
			node.Outcome = "passed"
		}
		node.Phase = ""
	default:
		if result.ExitCode != 0 {
			resultErr = fmt.Errorf("operation exited with code %d", result.ExitCode)
		} else {
			resultErr = validateCompletion(step, result)
		}
		if resultErr != nil {
			node.Status = "failed"
		} else {
			node.Status = "completed"
			node.Outcome = "passed"
		}
		node.Phase = ""
	}
	if resultErr != nil {
		node.History = append(node.History, resultErr.Error())
	}
	updated[step.ID] = node
	return resultErr
}

func validateCompletion(step Step, result ExecutionResult) error {
	if result.ExitCode != 0 {
		return nil
	}
	if step.Kind == "worktree" && !filepath.IsAbs(result.Workspace) {
		return fmt.Errorf("worktree result requires an absolute workspace path")
	}
	if schema := step.Spec["output_schema"]; schema != nil {
		return validateOutput(result.Output, schema, "$")
	}
	return nil
}
