package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func TestSessionForkAndFamily(t *testing.T) {
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	parent, err := state.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ForkCurrent(t.Context(), "early"); err == nil {
		t.Fatal("forked an empty session")
	}
	if err := state.Store.AppendTurn(t.Context(), session.ID(parent.SessionID), session.Turn{ID: "turn-1", Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkCurrent(t.Context(), "Branch A")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentSessionID != parent.SessionID || child.ForkTurnID != "turn-1" {
		t.Fatalf("fork metadata = %#v", child)
	}
	if _, err := state.ForkCurrent(t.Context(), "Branch A"); err == nil {
		t.Fatal("duplicate branch name was accepted")
	}
	page, err := state.Store.Items(t.Context(), session.ID(child.SessionID), sessionstore.BeforeFirst, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[1].Kind != sessionstore.ItemFork {
		t.Fatalf("child history = %#v", page.Items)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Resume(t.Context(), child.SessionID); err != nil {
		t.Fatal(err)
	}
	family, err := state.Family(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(family) != 2 || family[0].Metadata.SessionID != parent.SessionID || family[1].Metadata.SessionID != child.SessionID || family[1].Depth != 1 {
		t.Fatalf("family = %#v", family)
	}
}

func TestSessionStateNewRenameResumeAndWorkspaceScope(t *testing.T) {
	directory, workspace := t.TempDir(), t.TempDir()
	state, err := OpenSessionState(directory, workspace)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := state.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SessionID == "" || metadata.Workspace != state.Workspace {
		t.Fatalf("new metadata = %#v", metadata)
	}
	if err := state.Rename(t.Context(), "Review with spaces 🌿"); err != nil {
		t.Fatal(err)
	}
	if state.Current.SessionID != metadata.SessionID {
		t.Fatal("rename changed stable session ID")
	}
	other, err := OpenSessionState(directory, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Resume(t.Context(), "Review with spaces 🌿"); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("second process resume error = %v, want lock conflict", err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := other.Resume(t.Context(), "Review with spaces 🌿")
	if err != nil || resumed.SessionID != metadata.SessionID {
		t.Fatalf("resumed = %#v, %v", resumed, err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	foreign, err := OpenSessionState(directory, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Resume(t.Context(), metadata.SessionID); err == nil {
		t.Fatal("cross-workspace resume by ID succeeded")
	}
}

func TestSessionStateReportsDamagedOrMissingMetadata(t *testing.T) {
	directory, workspace := t.TempDir(), t.TempDir()
	state, err := OpenSessionState(directory, workspace)
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.metadataPath(session.ID(first.SessionID)), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	orphanID := session.ID(newSessionID())
	if _, err := state.Store.Create(t.Context(), orphanID); err != nil {
		t.Fatal(err)
	}
	choices, err := state.List(t.Context())
	if err != nil || len(choices) != 2 {
		t.Fatalf("choices = %#v, %v", choices, err)
	}
	issues := 0
	for _, choice := range choices {
		if choice.Issue != nil {
			issues++
		}
	}
	if issues != 2 {
		t.Fatalf("damaged and missing metadata issues = %d", issues)
	}
	if _, err := state.Resume(t.Context(), first.SessionID); err == nil {
		t.Fatal("damaged metadata was silently resumed")
	}
}

func TestSessionNameValidationAndStatePath(t *testing.T) {
	for _, bad := range []string{"", "\n", strings.Repeat("x", 81)} {
		if err := validateSessionName(bad); err == nil {
			t.Fatalf("name %q was accepted", bad)
		}
	}
	home := t.TempDir()
	path, err := StateDirectory(testEnv(home, ""))
	if err != nil || path != filepath.Join(home, ".local", "state", "unreal-agent-repl") {
		t.Fatalf("state path = %q, %v", path, err)
	}
	if WorkspaceKey("/one") == WorkspaceKey("/two") {
		t.Fatal("workspace keys collided")
	}
}
