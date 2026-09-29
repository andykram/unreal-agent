package repl

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExternalEditorCommandAndImport(t *testing.T) {
	words, err := parseEditorWords(`code --wait "a b" 'c d'`)
	if err != nil || !reflect.DeepEqual(words, []string{"code", "--wait", "a b", "c d"}) {
		t.Fatalf("editor words = %#v, %v", words, err)
	}
	if _, err := parseEditorWords(`editor "unfinished`); err == nil {
		t.Fatal("unterminated quote accepted")
	}
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	config, err := LoadConfig(testEnv(root, ""))
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), testEnv(root, ""), 0, state, config, nil, nil, nil, nil)
	if err := model.draft.Set("before"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state.Directory, "tmp", "draft.md")
	if err := os.WriteFile(path, []byte("after\nedit"), 0600); err != nil {
		t.Fatal(err)
	}
	model.editorPath = path
	model.finishExternalEditor(editorFinishedMsg{path: path, sessionID: state.Current.SessionID})
	if model.draft.Source() != "after\nedit" || model.editorPath != "" {
		t.Fatalf("imported draft = %q", model.draft.Source())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary draft was not removed: %v", err)
	}
	if err := os.WriteFile(path, []byte("saved despite failure"), 0600); err != nil {
		t.Fatal(err)
	}
	model.finishExternalEditor(editorFinishedMsg{path: path, sessionID: state.Current.SessionID, err: errors.New("exit 1")})
	if model.draft.Source() != "saved despite failure" || !strings.Contains(model.message, "exit 1") {
		t.Fatalf("failed editor import = %q, %q", model.draft.Source(), model.message)
	}
	if err := os.WriteFile(path, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	model.finishExternalEditor(editorFinishedMsg{path: path, sessionID: state.Current.SessionID})
	if model.draft.Source() != "saved despite failure" || !strings.Contains(model.message, "recovery file") {
		t.Fatalf("invalid editor import = %q, %q", model.draft.Source(), model.message)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovery file removed: %v", err)
	}
}
