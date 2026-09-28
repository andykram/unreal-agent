package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

// ExecutorKeyOptions describes one external operation within a durable attempt.
// Executor and Operation must be stable across retries. Different operations
// within an attempt must have different names.
type ExecutorKeyOptions struct {
	Executor     string
	Operation    string
	UnsafeRawKey string // Explicitly bypasses all namespace isolation.
}

// ExecutorKey derives an adapter-facing key without exposing the internal key.
// It never modifies checkpoint identity, including when using UnsafeRawKey.
// This prototype has no live adapters; dispatch must use this boundary when added.
func ExecutorKey(node Node, options ExecutorKeyOptions) (string, error) {
	if node.AttemptKey == "" {
		return "", fmt.Errorf("external operation requires an attempt key")
	}
	if options.Executor == "" || options.Operation == "" {
		return "", fmt.Errorf("external operation requires executor and operation names")
	}
	if options.UnsafeRawKey != "" {
		return options.UnsafeRawKey, nil
	}
	encoded, _ := json.Marshal([]string{"workflow-external-v1", options.Executor, node.AttemptKey, options.Operation})
	digest := sha256.Sum256(encoded)
	// The prefix also separates the external key space from internal hex keys.
	return "ext-v1-" + hex.EncodeToString(digest[:]), nil
}

// AssignExecutionKeys annotates accepted state before its durable checkpoint.
// A key identifies work within a run; a future executor must honor it to dedupe
// side effects. Merely storing these keys does not provide exactly-once execution.
func AssignExecutionKeys(graph Graph, state State, runID string) {
	for _, step := range graph.Steps {
		node := state[step.ID]
		if node.Status == "" || node.Status == "skipped" {
			continue
		}
		node.IdempotencyKey = executionKey(runID, step.ID)
		if step.Kind != "repeat_check" {
			node.AttemptKey = node.IdempotencyKey
		} else if node.Phase == "check" || node.Phase == "repair" {
			// Repairs counts completed repairs: check1 -> repair1 -> check2.
			node.AttemptKey = executionKey(runID, step.ID, node.Phase, strconv.Itoa(node.Repairs+1))
		}
		// Terminal repeat nodes have cleared Phase. Keep their final attempt.
		if node.AttemptKey != "" && !slices.Contains(node.AttemptKeys, node.AttemptKey) {
			node.AttemptKeys = append(node.AttemptKeys, node.AttemptKey)
		}
		state[step.ID] = node
	}
}

func executionKey(parts ...string) string {
	// JSON string arrays prevent ambiguities from delimiters inside IDs.
	encoded, _ := json.Marshal(append([]string{"workflow-execution-v1"}, parts...))
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
