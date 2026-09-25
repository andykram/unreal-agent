package repl

import (
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func TestHistoryPersistsDeduplicatesAndRetainsEntries(t *testing.T) {
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewHistoryStore(state)
	for _, input := range []struct{ kind, text string }{{"prompt", "one"}, {"prompt", "one"}, {"command", "/help"}, {"prompt", "two"}} {
		if _, err := store.Append(input.kind, session.ID("first"), input.text, "immediate", 2); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := NewHistoryStore(state).Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Text != "/help" || entries[1].Text != "two" || entries[1].SessionID != "first" {
		t.Fatalf("history entries = %#v", entries)
	}
}

func TestBufferLogicalLinesAndDraftRestore(t *testing.T) {
	var buffer editor.Buffer
	if err := buffer.Restore("one\ntwo", len("one\ntw")); err != nil {
		t.Fatal(err)
	}
	if buffer.AtFirstLine() || !buffer.AtLastLine() || !buffer.Up() || buffer.Cursor() != 2 {
		t.Fatalf("up cursor = %d", buffer.Cursor())
	}
	if !buffer.Down() || buffer.Cursor() != len("one\ntw") {
		t.Fatalf("down cursor = %d", buffer.Cursor())
	}
	if err := buffer.Restore("draft", 2); err != nil || buffer.Source() != "draft" || buffer.Cursor() != 2 {
		t.Fatalf("draft restoration = %q at %d: %v", buffer.Source(), buffer.Cursor(), err)
	}
}
