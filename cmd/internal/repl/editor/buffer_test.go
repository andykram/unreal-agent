package editor

import (
	"strings"
	"testing"
)

func TestSelectionReplacesWholeGraphemesAndUndoRestores(t *testing.T) {
	var draft Buffer
	if err := draft.Set("a👩‍💻b"); err != nil {
		t.Fatal(err)
	}
	draft.Left()
	draft.Extend(draft.Left)
	if got := draft.SelectedText(); got != "👩‍💻" {
		t.Fatalf("selection = %q", got)
	}
	if err := draft.Insert("中"); err != nil {
		t.Fatal(err)
	}
	if got := draft.Source(); got != "a中b" {
		t.Fatalf("replacement = %q", got)
	}
	if !draft.Undo() || draft.Source() != "a👩‍💻b" || draft.SelectedText() != "👩‍💻" {
		t.Fatalf("undo failed: %q, %q", draft.Source(), draft.SelectedText())
	}
	draft.Backspace()
	if got := draft.Source(); got != "ab" {
		t.Fatalf("delete selection = %q", got)
	}
}

func TestBufferKeepsGraphemeAndRawMarkdown(t *testing.T) {
	var buffer Buffer
	if err := buffer.Insert("**bold** 👩‍💻\nnext"); err != nil {
		t.Fatal(err)
	}
	buffer.Home()
	if buffer.Cursor() != len("**bold** 👩‍💻\n") {
		t.Fatalf("home cursor = %d", buffer.Cursor())
	}
	buffer.Left()
	buffer.Backspace()
	if got := buffer.Source(); got != "**bold** \nnext" {
		t.Fatalf("backspace split an emoji or changed Markdown: %q", got)
	}
	if !buffer.Undo() || buffer.Source() != "**bold** 👩‍💻\nnext" {
		t.Fatal("undo did not restore raw source")
	}
	if !buffer.Redo() || buffer.Source() != "**bold** \nnext" {
		t.Fatal("redo did not restore the edit")
	}
}

func TestBufferRejectsOversizeImportWithoutChangingDraft(t *testing.T) {
	var buffer Buffer
	if err := buffer.Set("kept"); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Set(strings.Repeat("x", MaxSourceBytes+1)); err == nil {
		t.Fatal("oversize editor import was accepted")
	}
	if buffer.Source() != "kept" {
		t.Fatal("failed edit changed the draft")
	}
}

func TestReadlineUnicodeKillsYankAndUndo(t *testing.T) {
	var buffer Buffer
	_ = buffer.Set("hello café 👩‍💻 world")
	buffer.WordLeft()
	if buffer.Cursor() != len("hello café 👩‍💻 ") {
		t.Fatal("word motion failed")
	}
	buffer.KillBackward(true)
	if buffer.Source() != "hello café world" {
		t.Fatalf("kill: %q", buffer.Source())
	}
	_ = buffer.Yank()
	if buffer.Source() != "hello café 👩‍💻 world" {
		t.Fatalf("yank: %q", buffer.Source())
	}
	buffer.Home()
	buffer.WordRight()
	buffer.WordRight()
	if buffer.Cursor() != len("hello café") {
		t.Fatal("word forward split Unicode")
	}
	buffer.KillLineStart()
	if buffer.Source() != " 👩‍💻 world" {
		t.Fatal("line kill")
	}
	buffer.Undo()
	if buffer.Source() != "hello café 👩‍💻 world" || buffer.Cursor() != len("hello café") {
		t.Fatal("undo did not restore cursor")
	}
	buffer.KillForward()
	if buffer.Source() != "hello café" {
		t.Fatalf("forward kill %q", buffer.Source())
	}
}
