package repl

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func TestHistorySearchPreservesDraftAndRestoresImage(t *testing.T) {
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	image, err := saveAttachment(state, testPNG(t), true)
	if err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(context.Background(), getenv, time.Minute, state, config, nil, nil, nil, nil)
	for _, entry := range []struct {
		kind, text string
		images     []string
	}{
		{kind: "prompt", text: "find red image", images: []string{image.ID}},
		{kind: "command", text: "/help"},
		{kind: "prompt", text: "inspect blue"},
	} {
		if _, err := model.history.Append(entry.kind, session.ID(state.Current.SessionID), entry.text, "immediate", 100, entry.images...); err != nil {
			t.Fatal(err)
		}
	}
	if err := model.draft.Set("unsent draft"); err != nil {
		t.Fatal(err)
	}
	model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if model.historySearch == nil || len(model.historySearch.matches) != 3 {
		t.Fatal("Ctrl+R did not open workspace history")
	}
	model.historySearch.appendQuery("rdi")
	if len(model.historySearch.matches) != 1 || model.historySearch.entries[model.historySearch.matches[0]].Text != "find red image" {
		t.Fatalf("fuzzy results = %#v", model.historySearch.matches)
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.historySearch != nil || model.draft.Source() != "unsent draft" {
		t.Fatalf("cancel changed draft: %q", model.draft.Source())
	}
	model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	model.historySearch.appendQuery("red")
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.draft.Source() != "find red image [Image #1]" || len(model.attachments) != 1 || model.attachments[0].Missing || !model.attachments[0].Submitted {
		t.Fatalf("history selection = %q, %#v", model.draft.Source(), model.attachments)
	}
	model.contextReport = &contextReport{}
	_, command := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if command == nil || reflect.TypeOf(command()) != reflect.TypeOf(tea.Quit()) {
		t.Fatal("Ctrl+D did not quit from a modal with a draft and image")
	}
	if _, err := os.Stat(image.Path); err != nil {
		t.Fatalf("Ctrl+D removed a submitted history image: %v", err)
	}
}

func TestEmptyHistorySearchNavigationAndTimedNotices(t *testing.T) {
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, time.Minute, state, config, nil, nil, nil, nil)
	for _, text := range []string{"old prompt", "new prompt"} {
		if _, err := model.history.Append("prompt", session.ID(state.Current.SessionID), text, "immediate", 100); err != nil {
			t.Fatal(err)
		}
	}
	model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if model.historySearch.selected != 1 {
		t.Fatal("Up did not navigate empty search")
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.historySearch.selected != 0 {
		t.Fatal("Down did not navigate empty search")
	}
	_, timer := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if timer == nil || model.draft.Source() != "new prompt" || model.historySearch != nil {
		t.Fatal("Enter did not select empty-query result")
	}
	if model.message != "" || len(model.transcript) != 1 || model.transcript[0].text != "Loaded prompt from history." {
		t.Fatal("history notice did not enter transcript")
	}
	expiry := model.transcript[0].expires
	model.Update(noticeExpiredMsg(expiry.Add(-time.Second)))
	if model.transcript[0].hidden {
		t.Fatal("notice hidden early")
	}
	model.appendNotice("Second notice")
	model.Update(noticeExpiredMsg(expiry.Add(time.Minute)))
	model.transcriptView(80, 24)
	if !strings.Contains(model.transcriptRows[0].text, "2 items hidden") {
		t.Fatalf("notices did not group: %#v", model.transcriptRows)
	}
	model.appendText("assistant", "Separate message")
	model.appendNotice("Third notice")
	model.Update(noticeExpiredMsg(time.Now().Add(2 * time.Minute)))
	model.transcriptView(80, 24)
	counts := 0
	for _, row := range model.transcriptRows {
		if strings.Contains(row.text, "hidden") {
			counts++
		}
	}
	if counts != 2 {
		t.Fatal("fold crossed an ordinary message")
	}
}
