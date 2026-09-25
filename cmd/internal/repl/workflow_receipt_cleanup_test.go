package repl

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowReceiptCleanupPreservesRecoveryEvidence(t *testing.T) {
	directory := t.TempDir()
	old := time.Now().Add(-8 * 24 * time.Hour)
	cases := []string{"expired", "active", "existing", "failed", "nonzero", "pending", "young", "legacy", "malformed"}
	paths := map[string]string{}
	for _, name := range cases {
		request := workflow.ExecutionRequest{RunID: name, ExternalKey: "key"}
		receipt := &workflowReceipt{ExternalKey: "key", Fingerprint: "fingerprint", StartedAt: old.Add(-time.Hour), CompletedAt: old, Result: &workflow.ExecutionResult{Output: "saved"}}
		switch name {
		case "failed":
			receipt.Error = "unknown"
		case "nonzero":
			receipt.Result.ExitCode = 1
		case "pending":
			receipt.Result = nil
			receipt.CompletedAt = time.Time{}
		case "young":
			receipt.CompletedAt = time.Now()
		}
		if err := saveWorkflowReceipt(directory, request, receipt); err != nil {
			t.Fatal(err)
		}
		paths[name] = workflowReceiptPath(directory, request)
		if name == "legacy" {

			if err := os.WriteFile(paths[name], []byte(`{"ExternalKey":"key"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if name == "malformed" {
			if err := os.WriteFile(paths[name], []byte("not json"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	removed, err := cleanupWorkflowReceiptFiles(directory, "active", 7*24*time.Hour, 100, func(id string) error {
		if id == "existing" {
			return nil
		}
		return fmt.Errorf("load: %w", sql.ErrNoRows)
	})
	if err != nil || removed != 1 {
		t.Fatalf("cleanup:%d %v", removed, err)
	}
	for name, path := range paths {
		_, err := os.Stat(path)
		if name == "expired" {
			if !os.IsNotExist(err) {
				t.Fatalf("expired retained:%v", err)
			}
		} else if err != nil {
			t.Fatalf("%s removed:%v", name, err)
		}
	}
}
func TestWorkflowReceiptCleanupCapsDeletesAndStopsOnLookupError(t *testing.T) {
	directory := t.TempDir()
	old := time.Now().Add(-8 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		request := workflow.ExecutionRequest{RunID: fmt.Sprint(i), ExternalKey: "key"}
		receipt := &workflowReceipt{ExternalKey: "key", Fingerprint: "f", StartedAt: old.Add(-time.Hour), CompletedAt: old, Result: &workflow.ExecutionResult{}}
		if err := saveWorkflowReceipt(directory, request, receipt); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := cleanupWorkflowReceiptFiles(directory, "", 7*24*time.Hour, 1, func(string) error { return sql.ErrNoRows })
	if err != nil || removed != 1 {
		t.Fatalf("cap:%d %v", removed, err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, "execution-receipts"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("remaining:%d %v", len(entries), err)
	}
	broken := errors.New("database unavailable")
	removed, err = cleanupWorkflowReceiptFiles(directory, "", 7*24*time.Hour, 100, func(string) error { return broken })
	if removed != 0 || !errors.Is(err, broken) {
		t.Fatalf("lookup error:%d %v", removed, err)
	}
}

func TestWorkflowRepairFingerprintPreservesRetryPayload(t *testing.T) {
	request := workflow.ExecutionRequest{Step: workflow.Step{Kind: "repeat_check"}, Node: workflow.Node{Phase: "repair", Output: "failing output", History: []string{"check 1 failed"}}}
	first, err := workflowFingerprint(request, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request.Node.History = append(request.Node.History, "reconciliation retry: verified safe", "external operation outcome unknown: interrupted")
	retry, err := workflowFingerprint(request, DefaultConfig())
	if err != nil || retry != first {
		t.Fatalf("retry fingerprint changed:%v", err)
	}
	request.Node.Output = "different failure"
	changed, err := workflowFingerprint(request, DefaultConfig())
	if err != nil || changed == first {
		t.Fatalf("changed repair payload reused identity:%v", err)
	}
}
