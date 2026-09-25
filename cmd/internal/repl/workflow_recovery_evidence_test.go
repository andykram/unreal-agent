package repl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowEvidenceDoesNotReplayUnknownEffects(t *testing.T) {
	for _, kind := range []string{"command", "agent", "repeat_check"} {
		t.Run(kind, func(t *testing.T) {
			executor := &replWorkflowExecutor{worktrees: worktrunkWorkflowBackend{run: func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("nonworktree probe executed a process")
				return nil, nil
			}}}
			evidence, candidate, err := probeWorkflowEvidence(context.Background(), executor, workflow.ExecutionRequest{Step: workflow.Step{Kind: kind}, Node: workflow.Node{Phase: "repair"}})
			if err != nil || candidate != nil || !strings.Contains(evidence, "receipt") {
				t.Fatalf("unexpected evidence: %q %#v %v", evidence, candidate, err)
			}
		})
	}
}
func TestWorkflowWorktreeEvidenceRequiresPinnedCleanCheckout(t *testing.T) {
	base := strings.Repeat("a", 40)
	for _, scenario := range []string{"verified", "missing", "mutable-base", "wrong-head", "dirty", "wrong-branch", "git-error"} {
		t.Run(scenario, func(t *testing.T) {
			request := workflow.ExecutionRequest{ExternalKey: "ext-v1-key", Step: workflow.Step{Kind: "worktree", Spec: map[string]any{"base": base}}}
			if scenario == "mutable-base" {
				request.Step.Spec["base"] = "HEAD"
			}
			executor := &replWorkflowExecutor{workspace: "/repo", worktrees: worktrunkWorkflowBackend{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "git" {
					t.Fatalf("probe invoked mutation tool %s", name)
				}
				switch args[2] {
				case "check-ref-format":
					return nil, nil
				case "worktree":
					if len(args) != 6 || args[3] != "list" {
						t.Fatalf("mutating git call:%v", args)
					}
					if scenario == "missing" {
						return nil, nil
					}
					return []byte("worktree /fork path\x00branch refs/heads/workflow/ext-v1-key\x00\x00"), nil
				case "symbolic-ref":
					if scenario == "wrong-branch" {
						return []byte("refs/heads/other"), nil
					}
					return []byte("refs/heads/workflow/ext-v1-key"), nil
				case "rev-parse":
					if scenario == "git-error" {
						return nil, errors.New("unavailable")
					}
					if args[3] == "--show-toplevel" {
						return []byte("/fork path\n"), nil
					}
					if scenario == "wrong-head" {
						return []byte(strings.Repeat("b", 40)), nil
					}
					return []byte(base), nil
				case "--no-optional-locks":
					if len(args) < 7 || args[4] != "core.fsmonitor=false" || args[5] != "status" {
						t.Fatalf("unsafe status call: %v", args)
					}
					if scenario == "dirty" {
						return []byte(" M file\x00"), nil
					}
					return nil, nil
				default:
					t.Fatalf("unexpected git command:%v", args)
					return nil, nil
				}
			}}}
			evidence, candidate, err := probeWorkflowEvidence(context.Background(), executor, request)
			if scenario == "git-error" {
				if err == nil {
					t.Fatal("inspection error hidden")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (candidate != nil) != (scenario == "verified") {
				t.Fatalf("unsafe candidate %s: %#v; %s", scenario, candidate, evidence)
			}
			if candidate != nil && candidate.Workspace != "/fork path" {
				t.Fatalf("wrong workspace:%#v", candidate)
			}
		})
	}
}
func TestWorkflowEvidenceCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, candidate, err := probeWorkflowEvidence(ctx, nil, workflow.ExecutionRequest{})
	if !errors.Is(err, context.Canceled) || candidate != nil {
		t.Fatalf("cancel:%#v %v", candidate, err)
	}
}

func TestWorkflowEvidenceUsesMatchingTerminalReceipt(t *testing.T) {
	config := DefaultConfig()
	executor := &replWorkflowExecutor{directory: t.TempDir(), config: &ConfigStore{active: config}}
	request := workflow.ExecutionRequest{RunID: "run", ExternalKey: "key", Step: workflow.Step{ID: "command", Kind: "command", Spec: map[string]any{"argv": []any{"test"}}}}
	fingerprint, err := workflowFingerprint(request, config)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &workflowReceipt{ExternalKey: "key", Fingerprint: fingerprint, StartedAt: time.Now(), CompletedAt: time.Now(), Result: &workflow.ExecutionResult{Output: "saved", ExitCode: 0}}
	if err := saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
		t.Fatal(err)
	}
	evidence, candidate, err := probeWorkflowEvidence(context.Background(), executor, request)
	if err != nil || candidate == nil || candidate.Output != "saved" || !strings.Contains(evidence, "Saved terminal result") {
		t.Fatalf("receipt:%q %#v %v", evidence, candidate, err)
	}
	request.Node.Inputs = map[string]any{"changed": true}
	if _, candidate, err := probeWorkflowEvidence(context.Background(), executor, request); err == nil || candidate != nil {
		t.Fatalf("mismatched receipt accepted:%#v %v", candidate, err)
	}
}
func TestWorkflowEvidencePendingAgentReceiptIsNotCompletion(t *testing.T) {
	config := DefaultConfig()
	executor := &replWorkflowExecutor{directory: t.TempDir(), config: &ConfigStore{active: config}}
	request := workflow.ExecutionRequest{RunID: "run", ExternalKey: "agent-key", Step: workflow.Step{ID: "agent", Kind: "agent"}}
	fingerprint, err := workflowFingerprint(request, config)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &workflowReceipt{ExternalKey: request.ExternalKey, Fingerprint: fingerprint, StartedAt: time.Now(), AgentSessionID: "mapped-session"}
	if err := saveWorkflowReceipt(executor.directory, request, receipt); err != nil {
		t.Fatal(err)
	}
	evidence, candidate, err := probeWorkflowEvidence(context.Background(), executor, request)
	if err != nil || candidate != nil || !strings.Contains(evidence, "mapped-session") {
		t.Fatalf("pending:%q %#v %v", evidence, candidate, err)
	}
}
