package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

// probeWorkflowEvidence only observes external state. A candidate is evidence for
// an explicit reconciliation decision, never permission to change a checkpoint.
func probeWorkflowEvidence(ctx context.Context, executor *replWorkflowExecutor, request workflow.ExecutionRequest) (string, *workflow.ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	receiptEvidence := ""
	if executor != nil && executor.directory != "" {
		if executor.config == nil {
			return "", nil, fmt.Errorf("receipt evidence requires the executor configuration")
		}
		receipt, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
		if err != nil && !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect execution receipt: %w", err)
		}
		if receipt != nil {
			receiptEvidence = "Matching execution receipt. Started: " + receipt.StartedAt.UTC().Format(time.RFC3339Nano)
			if receipt.AgentSessionID != "" {
				receiptEvidence += "\nAgent session: " + receipt.AgentSessionID
			}
			if receipt.Error != "" {
				receiptEvidence += "\nRecorded error: " + receipt.Error
			}
			if receipt.Result != nil && !receipt.CompletedAt.IsZero() && receipt.Error == "" {
				return receiptEvidence + "\nCompleted: " + receipt.CompletedAt.UTC().Format(time.RFC3339Nano) + "\nSaved terminal result matches this operation and inputs. Accepting it requires an explicit reconciliation decision.", receipt.Result, nil
			}
			receiptEvidence += "\nNo verified terminal result is recorded. "
		}
	}
	switch request.Step.Kind {
	case "agent":
		return receiptEvidence + "No completed agent receipt is available. Use a recorded session ID when present; otherwise the previous session cannot be discovered reliably. Inspect the original session and verify its final result before reconciliation.", nil, nil
	case "command":
		return receiptEvidence + "Command execution has no completed external receipt. Exit status and effects cannot be inferred from processes or files. Verify the operation externally using its idempotency key before reconciliation.", nil, nil
	case "repeat_check":
		if request.Node.Phase == "repair" {
			return receiptEvidence + "No completed repair-agent receipt is available. Verify the recorded original agent session before reconciliation.", nil, nil
		}
		return receiptEvidence + "Check execution has no completed external receipt. Verify its actual result externally; this probe will not rerun the check.", nil, nil
	case "worktree":
	default:
		return fmt.Sprintf("No read-only evidence probe is available for step kind %q.", request.Step.Kind), nil, nil
	}
	if executor == nil {
		return "", nil, fmt.Errorf("worktree evidence requires an executor")
	}
	var backend worktrunkWorkflowBackend
	switch value := executor.worktrees.(type) {
	case worktrunkWorkflowBackend:
		backend = value
	case *worktrunkWorkflowBackend:
		if value == nil {
			return "", nil, fmt.Errorf("worktree backend is nil")
		}
		backend = *value
	default:
		return "The configured worktree backend does not expose a read-only evidence probe. Verify its operation externally.", nil, nil
	}
	if request.ExternalKey == "" || strings.ContainsAny(request.ExternalKey, "\x00\r\n") {
		return "", nil, fmt.Errorf("worktree evidence requires a valid external operation key")
	}
	run := backend.run
	if run == nil {
		run = runWorkflowWorktreeCommand
	}
	branch := "workflow/" + request.ExternalKey
	if _, err := run(ctx, "git", "-C", executor.workspace, "check-ref-format", "--branch", branch); err != nil {
		return "", nil, fmt.Errorf("validate recovery branch: %w", err)
	}
	listing, err := run(ctx, "git", "-C", executor.workspace, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", nil, fmt.Errorf("inspect registered worktrees: %w", err)
	}
	workspace, err := workflowBranchPath(listing, branch)
	if err != nil {
		return "", nil, err
	}
	if workspace == "" {
		return fmt.Sprintf("No registered worktree matches branch %q. Absence from this listing does not prove that the interrupted operation had no effects.", branch), nil, nil
	}
	evidence := receiptEvidence + fmt.Sprintf("Registered worktree: %s\nBranch: %s", workspace, branch)
	base, _ := request.Step.Spec["base"].(string)
	if !workflowCommitID(base) {
		return evidence + "\nThe saved base is not an immutable commit ID. Creation cannot be verified against the original base.", nil, nil
	}
	top, err := run(ctx, "git", "-C", workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return evidence, nil, fmt.Errorf("inspect worktree root: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(string(top))) != filepath.Clean(workspace) {
		return evidence + "\nThe registered path is not the observed repository root. Manual verification is required.", nil, nil
	}
	ref, err := run(ctx, "git", "-C", workspace, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return evidence, nil, fmt.Errorf("inspect worktree branch: %w", err)
	}
	if strings.TrimSpace(string(ref)) != "refs/heads/"+branch {
		return evidence + "\nThe checked-out branch differs from the registered workflow branch.", nil, nil
	}
	head, err := run(ctx, "git", "-C", workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return evidence, nil, fmt.Errorf("inspect worktree commit: %w", err)
	}
	commit := strings.TrimSpace(string(head))
	evidence += "\nObserved HEAD: " + commit + "\nSaved base: " + base
	if !strings.EqualFold(commit, base) {
		return evidence + "\nHEAD differs from the pinned base. Do not infer successful provisioning automatically.", nil, nil
	}
	status, err := run(ctx, "git", "-C", workspace, "--no-optional-locks", "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return evidence, nil, fmt.Errorf("inspect worktree changes: %w", err)
	}
	if len(status) != 0 {
		return evidence + "\nThe worktree has changes. Inspect them before accepting the recovered workspace.", nil, nil
	}
	return evidence + "\nVerified clean worktree at the pinned base; Worktrunk provisioning uses no hooks. This is a candidate for explicit acceptance, not an automatic state change.", &workflow.ExecutionResult{Workspace: workspace}, nil
}
