package repl

import (
	"encoding/json"
	"github.com/unreallabsai/unreal-agent/harness/workflow"
	"os"
)

// A snapshot records the effective task prompt, including loaded instruction and
// skill preambles. It is evidence from execution, never a reconstructed preview.
type workflowPromptSnapshot struct {
	StepID, Phase string
	System, User  string
}

func loadWorkflowPromptSnapshots(directory, runID string, state workflow.State) map[string]workflowPromptSnapshot {
	snapshots := map[string]workflowPromptSnapshot{}
	for stepID, node := range state {
		if node.ExternalKey == "" {
			continue
		}
		data, err := os.ReadFile(workflowReceiptPath(directory, workflow.ExecutionRequest{RunID: runID, ExternalKey: node.ExternalKey}))
		if err != nil {
			continue
		}
		var receipt workflowReceipt
		if json.Unmarshal(data, &receipt) != nil || receipt.RunID != runID || receipt.ExternalKey != node.ExternalKey || receipt.Prompt == nil || receipt.Prompt.StepID != stepID {
			continue
		}
		snapshots[stepID] = *receipt.Prompt
	}
	return snapshots
}
