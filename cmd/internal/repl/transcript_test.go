package repl

import (
	"encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func TestResumedTranscriptReplaysOnceAndTracksSequence(t *testing.T) {
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	encoded, err := json.Marshal("saved prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Store.AppendInput(t.Context(), session.ID(state.Current.SessionID), inbox.Input{ID: "input-1", Kind: inbox.InputExternal, Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(testEnv(root, ""))
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), testEnv(root, ""), 0, state, config, nil, nil, nil, nil)
	if !strings.Contains(model.View().Content, "saved prompt") || model.lastDisplayed != sessionstore.Sequence(1) {
		t.Fatalf("replay = %q at %d", model.View().Content, model.lastDisplayed)
	}
	model.Init()
	if strings.Count(model.View().Content, "saved prompt") != 1 {
		t.Fatal("transcript replay duplicated content")
	}
}
