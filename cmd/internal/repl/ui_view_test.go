package repl

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func TestComposerViewportFitsTerminalAndKeepsCursorVisible(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	getenv := testEnv(home, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("model.context_window_tokens", 128000); err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(context.Background(), getenv, time.Minute, state, config, nil, nil, nil, nil)
	if status := model.statusLine(); !strings.Contains(status, "high effort") || !strings.Contains(status, "Context ≈") || !strings.Contains(status, "available") {
		t.Fatalf("context status = %q", status)
	}
	model.draft = editor.Buffer{}
	if err := model.draft.Set(strings.Repeat("long line with a tab\tand wide 字\n", 15)); err != nil {
		t.Fatal(err)
	}
	for _, size := range []struct{ width, height int }{{80, 24}, {16, 3}, {8, 2}, {3, 1}} {
		model.width, model.height = size.width, size.height
		view := model.View()
		rows := strings.Count(view.Content, "\n") + 1
		if rows > size.height {
			t.Errorf("%dx%d rendered %d rows", size.width, size.height, rows)
		}
		if size.height > 1 && (view.Cursor == nil || view.Cursor.Y < 0 || view.Cursor.Y >= rows) {
			t.Errorf("%dx%d cursor = %#v, rows %d", size.width, size.height, view.Cursor, rows)
		}
	}
}

func TestActivityIndicatorStartsTicksAndStopsAfterIdle(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(context.Background(), getenv, time.Minute, state, config, nil, nil, nil, nil)
	model.noColor = true
	model.Update(runtimeEventsMsg{events: []RuntimeEvent{{Kind: EventTaskStarted}}})
	if !model.busy || !strings.Contains(model.statusLine(), "Thinking") {
		t.Fatalf("started status = %q", model.statusLine())
	}
	generation := model.activityGeneration
	model.Update(activityTickMsg(generation))
	if model.activityFrame != 1 {
		t.Fatalf("activity frame = %d", model.activityFrame)
	}
	model.Update(runtimeEventsMsg{events: []RuntimeEvent{{Kind: EventTaskIdle}}})
	model.Update(activityTickMsg(generation))
	if model.activityFrame != 1 || !strings.Contains(model.statusLine(), "Ready") {
		t.Fatalf("idle status = %q, frame = %d", model.statusLine(), model.activityFrame)
	}
}

func TestForkSidebarFitsAndKeepsComposerCursor(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(context.Background(), getenv, time.Minute, state, config, nil, nil, nil, nil)
	model.noColor = true
	state.Current.SessionID = "child-2"
	model.forkChoices = []SessionChoice{
		{Metadata: SessionMetadata{SessionID: "root", Name: "Root"}},
		{Metadata: SessionMetadata{SessionID: "child-1", Name: "First child"}, Depth: 1},
		{Metadata: SessionMetadata{SessionID: "child-2", Name: "Second child"}, Depth: 1},
		{Metadata: SessionMetadata{SessionID: "grandchild", Name: "Grandchild"}, Depth: 2},
	}
	model.width, model.height = 110, 10
	view := model.View()
	if !strings.Contains(view.Content, "›   Second child") || !strings.Contains(view.Content, "Grandchild") {
		t.Fatalf("sidebar = %q", view.Content)
	}
	if rows := strings.Count(view.Content, "\n") + 1; rows > model.height || view.Cursor == nil || view.Cursor.X >= model.width-25 || view.Cursor.Y >= rows {
		t.Fatalf("sidebar layout rows=%d cursor=%#v", rows, view.Cursor)
	}
}

func TestSlashDismissalKeepsComposerInOwnedScreen(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyBackspace}, {Code: tea.KeyEscape}} {
		model.draft.Set("/")
		model.suppressCompletion = false
		model.refreshCompletion()
		if model.completion == nil {
			t.Fatal("menu did not open")
		}
		model.Update(key)
		if model.completion != nil {
			t.Fatal("dismissed menu is still active")
		}
		view := model.View()
		if !view.AltScreen || view.MouseMode == tea.MouseModeNone {
			t.Fatal("composer shares native scrollback with transient menu rows")
		}
		if strings.Contains(view.Content, "Show commands and keys") {
			t.Fatal("dismissed menu remains visible")
		}
	}
}

func TestTranscriptScrollKeepsComposerAndToolsExpand(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	model.width, model.height = 80, 20
	model.draft.Set("keep this draft")
	model.appendText("assistant", strings.Repeat("earlier transcript\n", 80))
	model.transcript = append(model.transcript, transcriptBlock{kind: "tool", text: "Bash: go test ./...", detail: "stdout:\nunique hidden output\nPASS", outcome: "exit 0"})
	before := model.View()
	if strings.Contains(before.Content, "unique hidden output") {
		t.Fatal("tool output starts expanded")
	}
	model.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	expanded := model.View()
	if !strings.Contains(expanded.Content, "unique hidden output") {
		t.Fatalf("tool did not expand: %s", expanded.Content)
	}
	if expanded.Cursor.Y != before.Cursor.Y || model.draft.Source() != "keep this draft" {
		t.Fatal("expanding tool moved or edited the composer")
	}
	model.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	model.View()
	// Click the visible tool header using the actual rendered row map.
	for y := 0; y < model.transcriptHeight; y++ {
		i := model.transcriptTop + y
		if i < len(model.transcriptRows) && model.transcriptRows[i].block == 1 {
			model.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
			break
		}
	}
	if !model.transcript[1].expanded {
		t.Fatal("click did not expand the tool")
	}
	model.View()
	model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	scrolled := model.View()
	if model.followTranscript || scrolled.Cursor.Y != before.Cursor.Y || !strings.Contains(scrolled.Content, "keep this draft") {
		t.Fatal("mouse scroll displaced composer")
	}
	model.appendText("assistant", "new reply while reading")
	top := model.transcriptTop
	model.View()
	if model.transcriptTop != top {
		t.Fatal("incoming output moved the reader")
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if !strings.Contains(model.View().Content, "new reply while reading") {
		t.Fatal("return to latest did not follow output")
	}
}

func TestImageChipsStayInsideComposerAndSubmittedPrompt(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	model.draft.Set("Describe ")
	model.addAttachment(Attachment{Path: "/tmp/image.png", Width: 592, Height: 424, Format: "png", Submitted: true})
	view := model.View()
	if !strings.Contains(view.Content, "> Describe [Image #1]") || strings.Contains(view.Content, "592 x 424") {
		t.Fatalf("image not inline: %s", view.Content)
	}
	displayed := displayAttachmentPrompt(attachmentPrompt(model.draft.Source(), model.attachments))
	if displayed != "Describe [Image #1]" {
		t.Fatalf("submitted image display = %q", displayed)
	}
	model.removeLastAttachment()
	if strings.Contains(model.draft.Source(), "[Image #1]") {
		t.Fatal("removed image chip remains in draft")
	}
}

func TestRightSidebarClickAndRenameKeepsSession(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	first := state.Current
	other, err := OpenSessionState(state.Directory, state.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := other.Current
	other.Close()
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	model.width, model.height = 110, 24
	model.forkChoices = []SessionChoice{{Metadata: first}, {Metadata: second}}
	view := model.View()
	rows := strings.Split(view.Content, "\n")
	if len(rows) != 24 || !strings.Contains(rows[23], "│") || view.Cursor.X >= 85 {
		t.Fatal("sidebar does not occupy the right edge at full height")
	}
	model.Update(tea.MouseClickMsg{X: 100, Y: 2, Button: tea.MouseRight})
	if model.sessionMenu == nil || model.sessionMenu.id != second.SessionID || state.Current.SessionID != first.SessionID {
		t.Fatal("right-click switched session or missed rename")
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = model.rename.draft.Set("Renamed branch")
	model.updateRenameSession(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.rename != nil || state.Current.SessionID != first.SessionID {
		t.Fatal("renaming changed current session")
	}
	choices, err := state.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, choice := range choices {
		if choice.Metadata.SessionID == second.SessionID && choice.Metadata.Name == "Renamed branch" {
			found = true
			second = choice.Metadata
		}
	}
	if !found {
		t.Fatal("name did not persist")
	}
	model.forkChoices = []SessionChoice{{Metadata: first}, {Metadata: second}}
	model.View()
	model.Update(tea.MouseClickMsg{X: 100, Y: 2, Button: tea.MouseLeft})
	if state.Current.SessionID != second.SessionID {
		t.Fatal("left-click did not switch sessions")
	}
}

func TestCtrlCRequiresTwoConsecutivePresses(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	interrupt := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, command := model.Update(interrupt)
	if command != nil {
		t.Fatal("first Ctrl+C exits")
	}
	if !strings.Contains(model.View().Content, "Press Ctrl+C again to exit") {
		t.Fatal("exit confirmation missing")
	}
	model.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	_, command = model.Update(interrupt)
	if command != nil {
		t.Fatal("typing did not reset exit confirmation")
	}
	_, command = model.Update(interrupt)
	if command == nil {
		t.Fatal("second Ctrl+C did not exit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("second Ctrl+C did not return quit")
	}
	model = newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.contextReport = &contextReport{}
	_, command = model.Update(interrupt)
	if command != nil || !strings.Contains(model.View().Content, "Press Ctrl+C again to exit") {
		t.Fatal("modal did not show first Ctrl+C confirmation")
	}
	_, command = model.Update(interrupt)
	if command == nil {
		t.Fatal("second Ctrl+C did not exit modal")
	}
}

func TestSidebarResizeAndRenameReadline(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.width, model.height = 120, 30
	model.forkChoices = []SessionChoice{{Metadata: state.Current}, {Metadata: state.Current}}
	model.Update(tea.MouseClickMsg{X: 95, Y: 12, Button: tea.MouseLeft})
	model.Update(tea.MouseMotionMsg{X: 80, Y: 12, Button: tea.MouseLeft})
	model.Update(tea.MouseReleaseMsg{X: 80, Y: 12, Button: tea.MouseLeft})
	if model.sidebarSize() != 40 || model.sidebarDragging {
		t.Fatal("divider did not resize/release")
	}
	view := model.View()
	if view.Cursor.X >= 80 || len(strings.Split(view.Content, "\n")) != 30 {
		t.Fatal("resize broke layout")
	}
	model.openRenameSession(state.Current.SessionID)
	_ = model.rename.draft.Set("first second")
	model.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if model.rename.draft.Source() != "first " {
		t.Fatal("rename Ctrl+W")
	}
	model.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	model.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	if model.rename.draft.Source() != "first second" || model.rename.draft.Cursor() != 6 {
		t.Fatal("rename yank / word movement")
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.rename != nil {
		t.Fatal("rename cancel")
	}
}

func TestSystemPromptShowsSnapshotOrConfiguredPreview(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(map[string]any{"system_prompt": "Custom instructions."}); err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.runSystemPrompt("", "")
	last := model.transcript[len(model.transcript)-1]
	if !strings.Contains(last.text, "Custom instructions.") || !strings.Contains(last.text, "Workspace:") || !strings.Contains(last.kind, "preview") {
		t.Fatalf("preview = %#v", last)
	}
	model.runtime = &Runtime{systemPrompt: "Exact loaded prompt."}
	model.runSystemPrompt("", "")
	last = model.transcript[len(model.transcript)-1]
	if last.text != "Exact loaded prompt." || !strings.Contains(last.kind, "latest task") {
		t.Fatal("display rebuilt instead of showing loaded snapshot")
	}
}

func TestToolToggleAnchorsVisibleToolAndHandlesMixedCards(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	for i := 0; i < 20; i++ {
		model.transcript = append(model.transcript, transcriptBlock{kind: "tool", text: fmt.Sprintf("Tool %d", i), detail: strings.Repeat("output\n", 10), outcome: "completed"})
	}
	model.View()
	visible := -1
	for _, row := range model.transcriptRows[model.transcriptTop:] {
		if row.block >= 0 {
			visible = row.block
			break
		}
	}
	model.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	model.View()
	if visible < 0 || model.transcriptRows[model.transcriptTop].block != visible || !model.transcript[visible].expanded {
		t.Fatal("expansion jumped away from visible tool")
	}
	model.transcript[0].expanded = false
	model.transcript[0].rows = nil
	model.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if model.expandTools || model.transcript[visible].expanded {
		t.Fatal("mixed tool states prevented collapse")
	}
}

func TestPowerBarGlobalNavigationAndEffort(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, NewModelCatalog(config, getenv), nil, nil)
	_ = model.draft.Set("keep this draft")
	model.contextReport = &contextReport{}
	model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if model.powerBar == nil {
		t.Fatal("palette not global")
	}
	kinds := map[string]bool{}
	for _, choice := range model.powerBar.choices {
		kinds[choice.kind] = true
	}
	for _, kind := range []string{"command", "forks", "session", "model"} {
		if !kinds[kind] {
			t.Fatalf("missing %s", kind)
		}
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.contextReport == nil || model.draft.Source() != "keep this draft" {
		t.Fatal("palette escape lost underlying state")
	}
	model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModSuper})
	if model.powerBar == nil {
		t.Fatal("Super+K not handled")
	}
	for _, size := range []struct{ w, h int }{{80, 24}, {24, 11}, {20, 10}, {8, 3}} {
		model.width, model.height = size.w, size.h
		view := model.View()
		if strings.Count(view.Content, "\n")+1 > size.h {
			t.Fatalf("palette overflows %dx%d: %q", size.w, size.h, view.Content)
		}
	}
	model.width, model.height = 80, 24
	model.powerBar.query = "/effort"
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.powerBar != nil || model.contextReport != nil || model.completion == nil {
		t.Fatal("palette did not open effort selector")
	}
	model.runEffort("low", "")
	if config.Current().Model.ReasoningEffort != "low" {
		t.Fatal("effort not set")
	}
	reloaded, err := LoadConfig(getenv)
	if err != nil || reloaded.Current().Model.ReasoningEffort != "low" {
		t.Fatal("effort not persisted")
	}
	model.runEffort("invalid", "")
	if config.Current().Model.ReasoningEffort != "low" {
		t.Fatal("invalid effort applied")
	}
}

func TestPaletteForkJumpAndRenameSelection(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	parent, err := state.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Store.AppendTurn(t.Context(), session.ID(parent.SessionID), session.Turn{ID: "turn", Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkCurrent(t.Context(), "Alternate approach")
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, NewModelCatalog(config, getenv), nil, nil)
	_ = model.draft.Set("preserved draft")
	model.openPowerBar()
	choices := model.powerBar.filtered()
	if choices[0].kind != "session" || choices[1].kind != "session" || choices[model.powerBar.selected].Value != parent.SessionID {
		t.Fatal("fork family is not immediately accessible")
	}
	if !strings.Contains(model.powerBarView().Content, "F2/Ctrl+R rename") {
		t.Fatal("rename shortcut not discoverable")
	}
	for i, choice := range choices {
		if choice.Value == child.SessionID {
			model.powerBar.selected = i
		}
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if model.rename == nil || model.rename.id != child.SessionID || state.Current.SessionID != parent.SessionID {
		t.Fatal("rename targeted active session instead of highlighted fork")
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.powerBar == nil || model.powerBar.filtered()[model.powerBar.selected].Value != child.SessionID || model.draft.Source() != "preserved draft" {
		t.Fatal("cancel lost palette selection or draft")
	}
	model.powerBar.query = "Alternate"
	model.powerBar.selected = 0
	model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if model.rename == nil {
		t.Fatal("Ctrl+R did not rename")
	}
	_ = model.rename.draft.Set("Renamed fork")
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.powerBar == nil || model.rename != nil || state.Current.SessionID != parent.SessionID {
		t.Fatal("save did not return to palette without switching")
	}
	selected := model.powerBar.filtered()[model.powerBar.selected]
	if selected.Value != child.SessionID || selected.Label != "Renamed fork" {
		t.Fatalf("renamed selection lost: %#v", selected)
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.powerBar != nil || state.Current.SessionID != child.SessionID {
		t.Fatal("Enter did not jump to highlighted fork")
	}
	model.openPowerBar()
	model.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if model.rename == nil || model.rename.id != child.SessionID {
		t.Fatal("default selection cannot rename active fork")
	}
}

func TestUnrealSidebarSingleSessionAndNarrowLayout(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	for _, width := range []int{80, 40, 24, 16} {
		model.width, model.height = width, 12
		view := model.View()
		if model.sidebarSize() == 0 || !strings.Contains(view.Content, map[bool]string{true: "U", false: "UNREAL"}[width < 24]) {
			t.Fatalf("width %d sidebar missing: %q", width, view.Content)
		}
		if view.Cursor == nil || view.Cursor.X >= width-model.sidebarSize() {
			t.Fatalf("width %d cursor %#v", width, view.Cursor)
		}
	}
}

func TestPlanReviewScrollCommentAndRevision(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, runtime, nil)
	model.noColor, model.width, model.height = true, 52, 8
	model.appendText("assistant", "Plan\nStep one\nStep two\nStep three\nStep four\nStep five\nStep six")
	if !model.openPlan() {
		t.Fatal("plan missing")
	}
	model.updatePlan(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if model.plan.selected == 0 || !strings.Contains(model.planView().Content, "PLAN REVIEW") {
		t.Fatal("plan did not scroll")
	}
	model.updatePlan(tea.KeyPressMsg{Code: 'c', Text: "c"})
	for _, ch := range "Check this" {
		model.updatePlan(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	model.updatePlan(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(model.plan.comments) != 1 || model.plan.comments[0].text != "Check this" {
		t.Fatal("comment not attached")
	}
	model.updatePlan(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if model.plan != nil {
		t.Fatal("revision did not close plan")
	}
	call := nextRuntimeCall(t, adapter)
	found := false
	for _, item := range call.request.Input {
		if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, "Check this") {
			found = true
		}
	}
	answerRuntimeCall(call)
	if !found {
		t.Fatal("revision prompt missing comment")
	}
}

func TestTaskRailAnchorsAboveComposerAndAnimatesBothDirections(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = true
	now := time.Now()
	pending := taskList{Tasks: []taskItem{{ID: "a", Title: "Inspect the code", Status: "in_progress"}, {ID: "b", Title: "Check changes", Status: "pending"}}}
	model.updateTaskList(pending, now)
	for _, size := range []struct{ width, height int }{{80, 24}, {40, 12}, {24, 8}, {16, 6}} {
		model.width, model.height = size.width, size.height
		view := model.View()
		if rows := strings.Count(view.Content, "\n") + 1; rows > size.height || view.Cursor == nil || view.Cursor.Y >= rows {
			t.Fatalf("%dx%d invalid view rows=%d cursor=%#v", size.width, size.height, rows, view.Cursor)
		}
		if size.height >= 8 && (!strings.Contains(view.Content, "TASKS") || strings.Index(view.Content, "TASKS") > strings.Index(view.Content, "Compose")) {
			t.Fatalf("task rail not above composer: %q", view.Content)
		}
	}
	model.width, model.height = 80, 24
	complete := taskList{Tasks: []taskItem{{ID: "a", Title: "Inspect the code", Status: "completed", Note: "checked"}, {ID: "b", Title: "Check changes", Status: "pending"}}}
	model.updateTaskList(complete, now)
	if got := strings.Join(model.taskRailLines(55, 4, now.Add(taskFrame)), " "); !strings.Contains(got, "◔ Inspect the code") {
		t.Fatalf("check animation = %q", got)
	}
	model.updateTaskList(pending, now.Add(taskFrame*2))
	if got := strings.Join(model.taskRailLines(55, 4, now.Add(taskFrame*3)), " "); !strings.Contains(got, "◉ Inspect the code") {
		t.Fatalf("uncheck animation = %q", got)
	}
}

func TestTaskRailHoldsFifteenSecondsThenFadesAndCancelsOnUpdate(t *testing.T) {
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	done := taskList{Tasks: []taskItem{{ID: "a", Title: "Done", Status: "completed", Note: "verified"}}}
	start := time.Now()
	model.updateTaskList(done, start)
	if !model.taskVisible() || model.taskCompletedAt.IsZero() {
		t.Fatal("completed list did not enter hold")
	}
	gen := model.taskGeneration
	model.advanceTaskRail(start.Add(taskHold-time.Millisecond), gen)
	if model.taskFade != 0 || !model.taskVisible() {
		t.Fatal("dismissed before 15 seconds")
	}
	model.advanceTaskRail(start.Add(taskHold), gen)
	if model.taskFade != 1 || !model.taskVisible() {
		t.Fatal("fade did not start after 15 seconds")
	}
	model.advanceTaskRail(start.Add(taskHold+taskFrame), gen)
	if model.taskFade != 2 || !model.taskVisible() {
		t.Fatal("fade did not advance")
	}
	active := taskList{Tasks: []taskItem{{ID: "a", Title: "Reopened", Status: "in_progress"}}}
	model.updateTaskList(active, start.Add(taskHold+taskFrame))
	if model.taskFade != 0 || !model.taskCompletedAt.IsZero() || !model.taskVisible() {
		t.Fatal("new work did not cancel dismissal")
	}
	if cmd := model.advanceTaskRail(start.Add(2*taskHold), gen); cmd != nil || model.taskFade != 0 {
		t.Fatal("stale timer affected new list")
	}
	model.updateTaskList(done, start.Add(2*taskHold))
	gen = model.taskGeneration
	for i := 0; i < taskFadeFrames; i++ {
		model.advanceTaskRail(start.Add(3*taskHold+time.Duration(i)*taskFrame), gen)
	}
	if model.taskVisible() || model.nextTaskTick() != nil {
		t.Fatal("completed list remained after fade")
	}
}

func TestTaskAndQueueRailKeepSmallComposerUsable(t *testing.T) {
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	getenv := testEnv(t.TempDir(), "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, runtime, nil)
	model.noColor = true
	model.updateTaskList(taskList{Tasks: []taskItem{{ID: "a", Title: "Working", Status: "in_progress"}}}, time.Now())
	if _, _, err := runtime.Submit(t.Context(), "first", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	for _, prompt := range []string{"queued one", "queued two"} {
		if _, _, err := runtime.Submit(t.Context(), prompt, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []struct{ width, height int }{{80, 24}, {40, 12}, {24, 8}, {16, 6}, {8, 2}} {
		model.width, model.height = size.width, size.height
		view := model.View()
		rows := strings.Count(view.Content, "\n") + 1
		if rows > size.height || (size.height > 1 && (view.Cursor == nil || view.Cursor.Y >= rows || view.Cursor.X >= size.width)) {
			t.Fatalf("%dx%d view rows=%d cursor=%#v", size.width, size.height, rows, view.Cursor)
		}
		if size.height >= 12 && (!strings.Contains(view.Content, "TASKS") || !strings.Contains(view.Content, "QUEUED")) {
			t.Fatalf("%dx%d missing rails: %q", size.width, size.height, view.Content)
		}
	}
	answerRuntimeCall(first)
	answerRuntimeCall(nextRuntimeCall(t, adapter))
	answerRuntimeCall(nextRuntimeCall(t, adapter))
	waitRuntimeTasks(t, runtime, 3)
}

func TestComposerColorizesResolvedSlashTokens(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Check changes\n---\n")
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	model.noColor = false
	if err := model.draft.Set("Use /review and /help now"); err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if !strings.Contains(view.Content, "\x1b[38;2;194;174;237m/review\x1b[39m") {
		t.Fatalf("skill token not colorized: %q", view.Content)
	}
	if !strings.Contains(view.Content, "\x1b[38;2;229;175;131m/help\x1b[39m") {
		t.Fatalf("command token not colorized: %q", view.Content)
	}
	model.noColor = true
	model.draft.Restore("Use /review and /help now!", len("Use /review and /help now!"))
	view = model.View()
	if strings.Contains(view.Content, "\x1b[38;2;") {
		t.Fatalf("noColor still emitted color: %q", view.Content)
	}
	if !strings.Contains(view.Content, "/review") {
		t.Fatalf("token missing without color: %q", view.Content)
	}
}
