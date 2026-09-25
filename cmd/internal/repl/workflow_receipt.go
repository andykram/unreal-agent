package repl

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

type workflowReceipt struct {
	Prompt         *workflowPromptSnapshot `json:",omitempty"`
	RunID          string
	ExternalKey    string
	Fingerprint    string
	AgentSessionID string
	Workspace      string
	StartedAt      time.Time
	CompletedAt    time.Time
	Result         *workflow.ExecutionResult
	Error          string
}

func workflowReceiptPath(directory string, request workflow.ExecutionRequest) string {
	digest := sha256.Sum256([]byte(request.RunID + "\x00" + request.ExternalKey))
	return filepath.Join(directory, "execution-receipts", hex.EncodeToString(digest[:])+".json")
}
func workflowFingerprint(request workflow.ExecutionRequest, config Config) (string, error) {
	id, _ := request.Step.Spec["workspace"].(string)
	var priorOutput any
	var repairHistory []string
	if request.Node.Phase == "repair" {
		priorOutput = request.Node.Output
		repairHistory = workflowRepairHistory(request.Node.History)
	}
	encoded, err := json.Marshal(struct {
		Step             workflow.Step
		Inputs           any
		Phase, Workspace string
		Config           Config
		PriorOutput      any      `json:",omitempty"`
		RepairHistory    []string `json:",omitempty"`
	}{request.Step, request.Node.Inputs, request.Node.Phase, request.State[id].Workspace, config, priorOutput, repairHistory})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
func readWorkflowReceipt(directory string, request workflow.ExecutionRequest, config Config) (*workflowReceipt, error) {
	data, err := os.ReadFile(workflowReceiptPath(directory, request))
	if err != nil {
		return nil, err
	}
	var receipt workflowReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	fingerprint, err := workflowFingerprint(request, config)
	if err != nil {
		return nil, err
	}
	if (receipt.RunID != "" && receipt.RunID != request.RunID) || receipt.ExternalKey != request.ExternalKey || receipt.Fingerprint != fingerprint {
		return nil, fmt.Errorf("saved execution receipt does not match this operation and its inputs")
	}
	return &receipt, nil
}
func saveWorkflowReceipt(directory string, request workflow.ExecutionRequest, receipt *workflowReceipt) error {
	receipt.RunID = request.RunID
	path := workflowReceiptPath(directory, request)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".receipt-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err = temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	directoryFile, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

// cleanupWorkflowReceipts removes only expired terminal evidence for runs already
// removed by run retention. Unknown, unresolved, failed, and legacy receipts stay.
func cleanupWorkflowReceipts(directory string, store *workflow.Store, activeRun string, retention time.Duration, batch int) (int, error) {
	return cleanupWorkflowReceiptFiles(directory, activeRun, retention, batch, func(id string) error {
		_, _, _, err := store.Load(id)
		return err
	})
}

func cleanupWorkflowReceiptFiles(directory, activeRun string, retention time.Duration, batch int, lookup func(string) error) (int, error) {
	if retention <= 0 || batch <= 0 || batch > 1000 {
		return 0, fmt.Errorf("receipt cleanup requires positive retention and batch size 1..1000")
	}
	root := filepath.Join(directory, "execution-receipts")
	dir, err := os.Open(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	cutoff := time.Now().Add(-retention)
	removed := 0
	for removed < batch {
		entries, readErr := dir.ReadDir(100)
		for _, entry := range entries {
			if removed == batch {
				break
			}
			if !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return removed, err
			}
			if !info.Mode().IsRegular() || info.Size() > 8<<20 {
				continue
			}
			filename := filepath.Join(root, entry.Name())
			data, err := os.ReadFile(filename)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return removed, err
			}
			var receipt workflowReceipt
			if !json.Valid(data) || json.Unmarshal(data, &receipt) != nil {
				continue
			}
			if receipt.RunID == "" || receipt.RunID == activeRun || receipt.ExternalKey == "" || receipt.Fingerprint == "" || receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() || !receipt.CompletedAt.Before(cutoff) || receipt.CompletedAt.Before(receipt.StartedAt) || receipt.Result == nil || receipt.Result.ExitCode != 0 || receipt.Error != "" {
				continue
			}
			expected := workflowReceiptPath(directory, workflow.ExecutionRequest{RunID: receipt.RunID, ExternalKey: receipt.ExternalKey})
			if expected != filename {
				continue
			}
			err = lookup(receipt.RunID)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return removed, fmt.Errorf("check receipt run %s before cleanup: %w", receipt.RunID, err)
			}
			if err := os.Remove(filename); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return removed, err
			}
			removed++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return removed, readErr
		}
	}
	if removed > 0 {
		if err := dir.Sync(); err != nil {
			return removed, err
		}
	}
	return removed, nil
}
