package editor

import (
	"strings"
	"testing"
)

func TestProjectionRetainsSourceAndRevealsAdjacentStrong(t *testing.T) {
	source := "A **bold** z"
	far := Project(source, 0, 80, true)
	if got := strings.Join(far.Lines, "\n"); got != "A bold z" {
		t.Fatalf("inactive projection = %q", got)
	}
	for _, cursor := range []int{2, 3, 5, 10, 11} {
		active := Project(source, cursor, 80, true)
		if got := strings.Join(active.Lines, "\n"); got != source {
			t.Fatalf("cursor %d revealed %q, want %q", cursor, got, source)
		}
	}
	plain := Project(source, 0, 80, false)
	if got := strings.Join(plain.Lines, "\n"); got != source {
		t.Fatalf("plain projection = %q", got)
	}
}

func TestProjectionWrapsEmojiWithoutChangingSource(t *testing.T) {
	source := "# Hello 👩‍💻 world"
	projected := Project(source, len(source), 8, true)
	if projected.VisualRows < 2 {
		t.Fatalf("visual rows = %d, want wrapping", projected.VisualRows)
	}
	if projected.CursorRow >= projected.VisualRows {
		t.Fatalf("cursor row = %d, rows = %d", projected.CursorRow, projected.VisualRows)
	}
	if got := strings.Join(Project(source, 0, 80, false).Lines, "\n"); got != source {
		t.Fatalf("source changed = %q", got)
	}
}

func TestProjectionExpandsTabsAndEscapesControls(t *testing.T) {
	source := "A\tB\x1bC"
	projected := Project(source, 0, 80, false)
	if got := projected.Lines[0]; got != "A       B␛C" {
		t.Fatalf("display = %q", got)
	}
	if len(projected.Cells) != 5 || projected.Cells[1].Width != 7 || projected.Cells[3].Source.Start != 3 {
		t.Fatalf("cells = %#v", projected.Cells)
	}
}

func TestProjectionLeavesIncompleteMarkdownLiteral(t *testing.T) {
	source := "before **unfinished"
	if got := strings.Join(Project(source, 0, 80, true).Lines, "\n"); got != source {
		t.Fatalf("incomplete Markdown projection = %q", got)
	}
}

func TestSelectionRevealsSyntaxAndMapsGraphemes(t *testing.T) {
	source := "👩‍💻 **bold** next"
	selected := SourceRange{Start: len("👩‍💻 "), End: len("👩‍💻 **bold")}
	projected := Project(source, len(source), 80, true, selected)
	if got := strings.Join(projected.Lines, "\n"); got != source {
		t.Fatalf("selection should reveal delimiters: %q", got)
	}
	if !strings.Contains(projected.SelectedLines[0], "\x1b[7m**bold\x1b[27m") {
		t.Fatalf("selection display = %q", projected.SelectedLines[0])
	}
	for _, cell := range projected.Cells {
		if cell.Source.Start > cell.Source.End || cell.Source.End > len(source) {
			t.Fatalf("invalid cell: %#v", cell)
		}
	}
}

func TestTaskMarkerProjection(t *testing.T) {
	source := "A\n- [x] done\nZ"
	if got := Project(source, 0, 80, true).Lines[1]; got != "done" {
		t.Fatalf("task display = %q", got)
	}
	if got := Project(source, 6, 80, true).Lines[1]; got != "- [x] done" {
		t.Fatalf("active task display = %q", got)
	}
}

func TestNestedEmphasisRevealsContainingSyntax(t *testing.T) {
	source := "Z **outer *inner* text** Q"
	if got := Project(source, 0, 80, true).Lines[0]; got != "Z outer inner text Q" {
		t.Fatalf("inactive nested emphasis = %q", got)
	}
	if got := Project(source, strings.Index(source, "inner")+2, 80, true).Lines[0]; got != source {
		t.Fatalf("active nested emphasis = %q", got)
	}
}

func TestProjectionLinksCodeStrikesAndFences(t *testing.T) {
	for _, fixture := range []struct {
		source, hidden string
		active         int
	}{
		{"A [label](destination) z", "A label z", 5},
		{"A `code` z", "A code z", 4},
		{"A ~~gone~~ z", "A gone z", 6},
		{"before\n```go\ncode\n```\nafter", "before\n\ncode\n\nafter", len("before\n```go\nco")},
		{"A\n> quote\nZ", "A\nquote\nZ", 5},
		{"A\n- item\nZ", "A\nitem\nZ", 5},
	} {
		if got := strings.Join(Project(fixture.source, 0, 80, true).Lines, "\n"); got != fixture.hidden {
			t.Errorf("inactive %q = %q, want %q", fixture.source, got, fixture.hidden)
		}
		if got := strings.Join(Project(fixture.source, fixture.active, 80, true).Lines, "\n"); got != fixture.source {
			t.Errorf("active %q = %q", fixture.source, got)
		}
	}
	for _, source := range []string{"A [unfinished](dest", "A `unfinished", "```go\ncode"} {
		if got := strings.Join(Project(source, 0, 80, true).Lines, "\n"); got != source {
			t.Errorf("incomplete %q = %q", source, got)
		}
	}
}

func TestProjectParsedColorsSgrSpanOnlyInStyledLines(t *testing.T) {
	source := "run /help now"
	spans := []SyntaxSpan{{Kind: "slash_command", Source: SourceRange{Start: 4, End: 9}, SGR: "38;2;229;175;131"}}
	plain := ProjectParsed(source, 0, 80, false, spans)
	if got := strings.Join(plain.Lines, "\n"); got != source {
		t.Fatalf("plain Lines changed: %q", got)
	}
	if got := plain.SelectedLines[0]; got != "run \x1b[38;2;229;175;131m/help\x1b[39m now" {
		t.Fatalf("styled = %q", got)
	}
	active := ProjectParsed(source, 6, 80, false, spans)
	if got := active.SelectedLines[0]; got != "run \x1b[38;2;229;175;131m/help\x1b[39m now" {
		t.Fatalf("cursor inside styled span = %q", got)
	}
	untouched := ProjectParsed(source, 0, 80, false, nil)
	if got := untouched.SelectedLines[0]; got != source {
		t.Fatalf("unstyled = %q", got)
	}
}

func TestProjectParsedClosesColorAcrossRowBreaks(t *testing.T) {
	source := "aaa /help bbb"
	spans := []SyntaxSpan{{Kind: "slash_command", Source: SourceRange{Start: 4, End: 9}, SGR: "38;5;208"}}
	projected := ProjectParsed(source, 0, 7, false, spans)
	if projected.VisualRows != 2 {
		t.Fatalf("rows = %d", projected.VisualRows)
	}
	if got := projected.SelectedLines[0]; got != "aaa \x1b[38;5;208m/he\x1b[39m" {
		t.Fatalf("row 0 = %q", got)
	}
	if got := projected.SelectedLines[1]; got != "\x1b[38;5;208mlp\x1b[39m bbb" {
		t.Fatalf("row 1 = %q", got)
	}
	wrapped := ProjectParsed("one\ntwo /help", 0, 20, false, []SyntaxSpan{{Kind: "slash_command", Source: SourceRange{Start: 8, End: 13}, SGR: "38;5;208"}})
	if got := wrapped.SelectedLines; len(got) != 2 || got[1] != "two \x1b[38;5;208m/help\x1b[39m" {
		t.Fatalf("newline rows = %#v", got)
	}
}

func TestProjectParsedColorsNestWithSelection(t *testing.T) {
	source := "run /help now"
	spans := []SyntaxSpan{{Kind: "slash_command", Source: SourceRange{Start: 4, End: 9}, SGR: "38;5;208"}}
	selected := SourceRange{Start: 6, End: 9}
	projected := ProjectParsed(source, 0, 80, false, spans, selected)
	want := "run \x1b[38;5;208m/h\x1b[7melp\x1b[39m\x1b[27m now"
	if got := projected.SelectedLines[0]; got != want {
		t.Fatalf("nested = %q, want %q", got, want)
	}
}
