package repl

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WorkflowWorktreeBackend isolates provisioning from workflow execution policy.
// branch is a stable workflow-owned name, reused for retries of the same step.
type WorkflowWorktreeBackend interface {
	Create(ctx context.Context, repo, branch, base string) (string, error)
}

type worktrunkWorkflowBackend struct {
	run func(context.Context, string, ...string) ([]byte, error)
}

func (backend worktrunkWorkflowBackend) Create(ctx context.Context, repo, branch, base string) (string, error) {
	run := backend.run
	if run == nil {
		run = runWorkflowWorktreeCommand
	}
	if repo == "" || branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("worktree requires a repository and a plain workflow branch name")
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, "git", "-C", repo, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("invalid workflow branch %q: %w", branch, err)
	}
	find := func() (string, error) {
		output, err := run(ctx, "git", "-C", repo, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return "", fmt.Errorf("list workflow worktrees: %w", err)
		}
		return workflowBranchPath(output, branch)
	}
	existing, err := find()
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	if base == "" || base == "current" {
		base = "HEAD"
	}
	resolved, err := run(ctx, "git", "-C", repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve worktree base %q: %w", base, err)
	}
	commit := strings.TrimSpace(string(resolved))
	if !workflowCommitID(commit) {
		return "", fmt.Errorf("git returned an invalid worktree base commit")
	}
	// Workflows run setup as explicit steps. Suppress shell navigation and hooks;
	// otherwise asynchronous project hooks can race the first workflow operation.
	output, err := run(ctx, "wt", "-C", repo, "switch", "--create", branch, "--base", commit, "--no-cd", "--no-hooks")
	if err != nil {
		// Creation can succeed before a competing invocation reports a conflict.
		if ctx.Err() == nil {
			if existing, inspectErr := find(); inspectErr == nil && existing != "" {
				return existing, nil
			}
		}
		return "", fmt.Errorf("create workflow worktree with Worktrunk: %w: %s", err, strings.TrimSpace(string(output)))
	}
	created, err := find()
	if err != nil {
		return "", err
	}
	if created == "" {
		return "", fmt.Errorf("worktree missing after Worktrunk returned for branch %q; inspect wt list", branch)
	}
	return created, nil
}

func workflowCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func workflowBranchPath(output []byte, branch string) (string, error) {
	var current, found string
	flush := func(ref string) error {
		if ref != "refs/heads/"+branch {
			return nil
		}
		if current == "" || !filepath.IsAbs(current) {
			return fmt.Errorf("invalid path for workflow branch %q", branch)
		}
		if found != "" && found != current {
			return fmt.Errorf("multiple worktrees use workflow branch %q", branch)
		}
		found = current
		return nil
	}
	for _, field := range bytes.Split(output, []byte{0}) {
		text := string(field)
		switch {
		case text == "":
			current = ""
		case strings.HasPrefix(text, "worktree "):
			current = strings.TrimPrefix(text, "worktree ")
		case strings.HasPrefix(text, "branch "):
			if err := flush(strings.TrimPrefix(text, "branch ")); err != nil {
				return "", err
			}
		}
	}
	return found, nil
}

type workflowCommandOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *workflowCommandOutput) Write(p []byte) (int, error) {
	const limit = 1 << 20
	available := limit - b.Len()
	if available < len(p) {
		b.truncated = true
	} else {
		available = len(p)
	}
	if available > 0 {
		b.Buffer.Write(p[:available])
	}
	return len(p), nil
}
func runWorkflowWorktreeCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	var output workflowCommandOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return output.Bytes(), ctx.Err()
	}
	if output.truncated {
		return output.Bytes(), fmt.Errorf("%s output exceeded 1 MiB", name)
	}
	return output.Bytes(), err
}
