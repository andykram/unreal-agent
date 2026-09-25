package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

type receiptWorktreeFunc func(context.Context, string, string, string) (string, error)

func (f receiptWorktreeFunc) Create(ctx context.Context, repo, branch, base string) (string, error) {
	return f(ctx, repo, branch, base)
}

func receiptFixture(t *testing.T) (*replWorkflowExecutor, workflow.ExecutionRequest) {
	t.Helper()
	executor := &replWorkflowExecutor{directory: t.TempDir(), workspace: t.TempDir(), config: &ConfigStore{active: DefaultConfig()}, notices: make(chan workflowAgentNotice, 4)}
	request := workflow.ExecutionRequest{RunID: "run-1", ExternalKey: "external-1", Step: workflow.Step{ID: "workspace", Kind: "worktree", Spec: map[string]any{"base": "current"}}, Node: workflow.Node{Phase: "execute", Inputs: map[string]any{"count": json.Number("9007199254740993")}}, State: workflow.State{}}
	return executor, request
}

func TestWorkflowReceiptPrecedesSideEffectAndRecordsSuccess(t *testing.T) {
	executor, request := receiptFixture(t)
	calls := 0
	workspace := t.TempDir()
	executor.worktrees = receiptWorktreeFunc(func(_ context.Context, repo, branch, base string) (string, error) {
		calls++
		receipt, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
		if err != nil {
			t.Fatal(err)
		}
		if receipt.StartedAt.IsZero() || !receipt.CompletedAt.IsZero() || receipt.Result != nil || receipt.Error != "" {
			t.Fatalf("pending receipt missing before execution: %+v", receipt)
		}
		if repo != executor.workspace || branch != "workflow/external-1" || base != "HEAD" {
			t.Fatalf("wrong dispatch %q %q %q", repo, branch, base)
		}
		return workspace, nil
	})
	result, err := executor.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Workspace != workspace {
		t.Fatal("execution result lost")
	}
	receipt, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Result == nil || receipt.Result.Workspace != workspace || receipt.CompletedAt.Before(receipt.StartedAt) {
		t.Fatalf("terminal receipt missing: %+v", receipt)
	}
	_, candidate, err := probeWorkflowEvidence(t.Context(), executor, request)
	if err != nil || candidate == nil || candidate.Workspace != workspace || calls != 1 {
		t.Fatal("saved success did not become read-only evidence", err)
	}
}

func TestWorkflowReceiptPreservesPrecisionAndRejectsChangedFingerprint(t *testing.T) {
	executor, request := receiptFixture(t)
	fingerprint, err := workflowFingerprint(request, executor.config.Current())
	if err != nil {
		t.Fatal(err)
	}
	receipt := &workflowReceipt{ExternalKey: request.ExternalKey, Fingerprint: fingerprint, StartedAt: time.Now(), CompletedAt: time.Now(), Result: &workflow.ExecutionResult{Output: map[string]any{"count": json.Number("9007199254740993")}}}
	if err := saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
		t.Fatal(err)
	}
	saved, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Result.Output.(map[string]any)["count"] != json.Number("9007199254740993") {
		t.Fatalf("precision lost: %#v", saved.Result.Output)
	}
	changed := request
	changed.Node.Inputs = map[string]any{"count": json.Number("9007199254740992")}
	if _, err := readWorkflowReceipt(executor.directory, changed, executor.config.Current()); err == nil {
		t.Fatal("receipt reused for changed inputs")
	}
	config := executor.config.Current()
	config.Model.ID = "different-model"
	if _, err := readWorkflowReceipt(executor.directory, request, config); err == nil {
		t.Fatal("receipt reused for changed execution config")
	}
	saved.ExternalKey = "other-key"
	if err := saveWorkflowReceipt(executor.directory, request, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowReceipt(executor.directory, request, executor.config.Current()); err == nil {
		t.Fatal("receipt with wrong external key accepted")
	}
}

func TestWorkflowReceiptStorageFailurePreventsExecution(t *testing.T) {
	executor, request := receiptFixture(t)
	if err := os.WriteFile(filepath.Join(executor.directory, "execution-receipts"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	executor.worktrees = receiptWorktreeFunc(func(context.Context, string, string, string) (string, error) { calls++; return "", nil })
	_, err := executor.Execute(t.Context(), request)
	if !errors.Is(err, workflow.ErrNotDispatched) || calls != 0 {
		t.Fatalf("receipt storage failure dispatched work: calls=%d err=%v", calls, err)
	}
}

func TestWorkflowReceiptAmbiguousFailureHasNoCandidate(t *testing.T) {
	executor, request := receiptFixture(t)
	failure := errors.New("connection lost after possible effect")
	executor.worktrees = receiptWorktreeFunc(func(context.Context, string, string, string) (string, error) { return "/possible/worktree", failure })
	_, err := executor.Execute(t.Context(), request)
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	receipt, err := readWorkflowReceipt(executor.directory, request, executor.config.Current())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Result != nil || !receipt.CompletedAt.IsZero() || !strings.Contains(receipt.Error, "possible effect") {
		t.Fatalf("ambiguous error stored as success: %+v", receipt)
	}
	evidence, candidate, err := probeWorkflowEvidence(t.Context(), executor, request)
	if err != nil || candidate != nil || !strings.Contains(evidence, "does not expose") {
		t.Fatalf("ambiguous operation offered a candidate: %s %#v %v", evidence, candidate, err)
	}
}
