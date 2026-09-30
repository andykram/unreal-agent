package repl

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowWorktreeCreateAndResume(t *testing.T) {
	const repo = "/repo"
	const branch = "workflow/run-step"
	commit := strings.Repeat("a", 40)
	var calls [][]string
	lists := 0
	backend := worktrunkWorkflowBackend{run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case name == "git" && args[2] == "check-ref-format":
			return nil, nil
		case name == "git" && args[2] == "rev-parse":
			return []byte(commit + "\n"), nil
		case name == "wt":
			return nil, nil
		default:
			lists++
			if lists == 1 {
				return []byte("worktree /repo\x00branch refs/heads/main\x00\x00"), nil
			}
			return []byte("worktree /repo fork\x00branch refs/heads/" + branch + "\x00\x00"), nil
		}
	}}
	path, err := backend.Create(context.Background(), repo, branch, "current")
	if err != nil || path != "/repo fork" {
		t.Fatalf("create:%q %v", path, err)
	}
	want := []string{"wt", "-C", repo, "switch", "--create", branch, "--base", commit, "--no-cd", "--no-hooks"}
	if !reflect.DeepEqual(calls[3], want) {
		t.Fatalf("wrong Worktrunk invocation: %v", calls)
	}
	before := len(calls)
	path, err = backend.Create(context.Background(), repo, branch, "changed-base")
	if err != nil || path != "/repo fork" || len(calls) != before+2 {
		t.Fatalf("resume recreated worktree: %s %v %v", path, err, calls[before:])
	}
}
func TestWorkflowWorktreeFailureAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runWorkflowWorktreeCommand(ctx, "git", "version")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	backend := worktrunkWorkflowBackend{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "wt" {
			return []byte("wt is unavailable"), errors.New("missing executable")
		}
		if args[2] == "rev-parse" {
			return []byte(strings.Repeat("a", 40)), nil
		}
		return nil, nil
	}}
	if _, err := backend.Create(context.Background(), "/repo", "workflow/test", "HEAD"); err == nil || !strings.Contains(err.Error(), "wt is unavailable") {
		t.Fatalf("unhelpful error:%v", err)
	}
}
func TestWorkflowWorktreePorcelainPaths(t *testing.T) {
	raw := []byte("worktree /a path\nwith newline\x00HEAD abc\x00branch refs/heads/workflow/test\x00\x00worktree /other\x00branch refs/heads/main\x00\x00")
	path, err := workflowBranchPath(raw, "workflow/test")
	if err != nil || path != "/a path\nwith newline" {
		t.Fatalf("path:%q %v", path, err)
	}
	if path, err := workflowBranchPath(raw, "missing"); err != nil || path != "" {
		t.Fatalf("unrelated branch selected:%q %v", path, err)
	}
	if _, err := workflowBranchPath([]byte("worktree relative\x00branch refs/heads/x\x00"), "x"); err == nil {
		t.Fatal("relative path accepted")
	}
}
