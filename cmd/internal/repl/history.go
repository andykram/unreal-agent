package repl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/session"
	"uuid"
)

type HistoryEntry struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	SessionID     string    `json:"session_id"`
	SubmittedAt   time.Time `json:"submitted_at"`
	Text          string    `json:"text"`
	AttachmentIDs []string  `json:"attachment_ids,omitempty"`
	Admission     string    `json:"admission"`
}

type HistoryStore struct {
	mu   sync.Mutex
	path string
}

func NewHistoryStore(state *SessionState) *HistoryStore {
	return &HistoryStore{path: filepath.Join(state.Directory, "history", WorkspaceKey(state.Workspace)+".jsonl")}
}

func (store *HistoryStore) Entries() ([]HistoryEntry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.read()
}

func (store *HistoryStore) read() ([]HistoryEntry, error) {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var entries []HistoryEntry
	for {
		var entry HistoryEntry
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			return entries, nil
		} else if err != nil {
			return nil, fmt.Errorf("parse history %s entry %d: %w", store.path, len(entries)+1, err)
		}
		entries = append(entries, entry)
	}
}

func (store *HistoryStore) Append(kind string, id session.ID, value, admission string, maxEntries int, attachmentIDs ...string) (bool, error) {
	if kind != "prompt" && kind != "command" {
		return false, fmt.Errorf("invalid history kind %q", kind)
	}
	if maxEntries < 1 {
		return false, errors.New("history retention must be positive")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := os.OpenFile(store.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entries, err := store.read()
	if err != nil {
		return false, err
	}
	if len(entries) > 0 && entries[len(entries)-1].Kind == kind && entries[len(entries)-1].Text == value && slices.Equal(entries[len(entries)-1].AttachmentIDs, attachmentIDs) {
		return false, nil
	}
	entries = append(entries, HistoryEntry{ID: uuid.New().String(), Kind: kind, SessionID: string(id), SubmittedAt: time.Now().UTC(), Text: value, Admission: admission, AttachmentIDs: append([]string(nil), attachmentIDs...)})
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	var encoded bytes.Buffer
	for _, entry := range entries {
		if err := json.NewEncoder(&encoded).Encode(entry); err != nil {
			return false, err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".history-*.jsonl")
	if err != nil {
		return false, err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(0600); err != nil {
		return false, err
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temporary.Name(), store.path); err != nil {
		return false, err
	}
	dir, err := os.Open(filepath.Dir(store.path))
	if err != nil {
		return false, err
	}
	defer dir.Close()
	return true, dir.Sync()
}
