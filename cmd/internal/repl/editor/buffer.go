package editor

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const MaxSourceBytes = 4 << 20

type snapshot struct {
	source    string
	cursor    int
	anchor    int
	selecting bool
}

// Buffer owns raw prompt bytes and a cursor at a grapheme boundary. The
// terminal projection never becomes the submitted or externally edited text.
type Buffer struct {
	source    string
	killed    string
	cursor    int
	anchor    int
	selecting bool
	version   uint64
	undo      []snapshot
	redo      []snapshot
}

func (buffer *Buffer) Source() string  { return buffer.source }
func (buffer *Buffer) Cursor() int     { return buffer.cursor }
func (buffer *Buffer) Version() uint64 { return buffer.version }
func (buffer *Buffer) Selection() (SourceRange, bool) {
	if !buffer.selecting || buffer.anchor == buffer.cursor {
		return SourceRange{}, false
	}
	return SourceRange{Start: min(buffer.anchor, buffer.cursor), End: max(buffer.anchor, buffer.cursor)}, true
}
func (buffer *Buffer) SelectedText() string {
	if selected, ok := buffer.Selection(); ok {
		return buffer.source[selected.Start:selected.End]
	}
	return ""
}
func (buffer *Buffer) ClearSelection() { buffer.selecting = false }
func (buffer *Buffer) SelectAll() {
	buffer.anchor, buffer.cursor, buffer.selecting = 0, len(buffer.source), true
}
func (buffer *Buffer) Extend(move func()) {
	if !buffer.selecting {
		buffer.anchor, buffer.selecting = buffer.cursor, true
	}
	move()
}

func (buffer *Buffer) Restore(source string, cursor int) error {
	if err := validEdit(source); err != nil {
		return err
	}
	if cursor < 0 || cursor > len(source) || !graphemeBoundary(source, cursor) {
		return errors.New("cursor is not at a grapheme boundary")
	}
	buffer.saveUndo()
	buffer.source, buffer.cursor = source, cursor
	buffer.selecting = false
	return nil
}

func (buffer *Buffer) Set(source string) error {
	if err := validEdit(source); err != nil {
		return err
	}
	buffer.saveUndo()
	buffer.source = source
	buffer.cursor = len(source)
	buffer.selecting = false
	return nil
}

func (buffer *Buffer) Clear() { _ = buffer.Set("") }

func (buffer *Buffer) Insert(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("inserted text is not valid UTF-8")
	}
	selected, hasSelection := buffer.Selection()
	removed := 0
	if hasSelection {
		removed = selected.End - selected.Start
	}
	if len(buffer.source)-removed+len(value) > MaxSourceBytes {
		return errors.New("prompt exceeds the 4 MiB editor limit")
	}
	if value == "" && !hasSelection {
		return nil
	}
	buffer.saveUndo()
	if hasSelection {
		buffer.source = buffer.source[:selected.Start] + value + buffer.source[selected.End:]
		buffer.cursor = selected.Start + len(value)
	} else {
		buffer.source = buffer.source[:buffer.cursor] + value + buffer.source[buffer.cursor:]
		buffer.cursor += len(value)
	}
	if !graphemeBoundary(buffer.source, buffer.cursor) {
		graphemes := uniseg.NewGraphemes(buffer.source)
		for graphemes.Next() {
			_, end := graphemes.Positions()
			if end >= buffer.cursor {
				buffer.cursor = end
				break
			}
		}
	}
	buffer.selecting = false
	return nil
}

func (buffer *Buffer) Backspace() {
	if _, ok := buffer.Selection(); ok {
		_ = buffer.Insert("")
		return
	}
	if buffer.cursor == 0 {
		return
	}
	start := previousGrapheme(buffer.source, buffer.cursor)
	buffer.saveUndo()
	buffer.source = buffer.source[:start] + buffer.source[buffer.cursor:]
	buffer.cursor = start
	buffer.selecting = false
}

func (buffer *Buffer) Delete() {
	if _, ok := buffer.Selection(); ok {
		_ = buffer.Insert("")
		return
	}
	if buffer.cursor == len(buffer.source) {
		return
	}
	end := nextGrapheme(buffer.source, buffer.cursor)
	buffer.saveUndo()
	buffer.source = buffer.source[:buffer.cursor] + buffer.source[end:]
	buffer.selecting = false
}

func (buffer *Buffer) Left()  { buffer.cursor = previousGrapheme(buffer.source, buffer.cursor) }
func (buffer *Buffer) Right() { buffer.cursor = nextGrapheme(buffer.source, buffer.cursor) }

func (buffer *Buffer) Home() {
	buffer.cursor = strings.LastIndex(buffer.source[:buffer.cursor], "\n") + 1
}

func (buffer *Buffer) End() {
	if offset := strings.Index(buffer.source[buffer.cursor:], "\n"); offset >= 0 {
		buffer.cursor += offset
	} else {
		buffer.cursor = len(buffer.source)
	}
}

func (buffer *Buffer) AtFirstLine() bool {
	return !strings.Contains(buffer.source[:buffer.cursor], "\n")
}

func (buffer *Buffer) AtLastLine() bool {
	return !strings.Contains(buffer.source[buffer.cursor:], "\n")
}

func (buffer *Buffer) Up() bool {
	start := strings.LastIndex(buffer.source[:buffer.cursor], "\n") + 1
	if start == 0 {
		return false
	}
	column := uniseg.GraphemeClusterCount(buffer.source[start:buffer.cursor])
	previousEnd := start - 1
	previousStart := strings.LastIndex(buffer.source[:previousEnd], "\n") + 1
	buffer.cursor = graphemeColumn(buffer.source, previousStart, previousEnd, column)
	return true
}

func (buffer *Buffer) Down() bool {
	end := len(buffer.source)
	if offset := strings.Index(buffer.source[buffer.cursor:], "\n"); offset >= 0 {
		end = buffer.cursor + offset
	} else {
		return false
	}
	start := strings.LastIndex(buffer.source[:buffer.cursor], "\n") + 1
	column := uniseg.GraphemeClusterCount(buffer.source[start:buffer.cursor])
	nextStart := end + 1
	nextEnd := len(buffer.source)
	if offset := strings.Index(buffer.source[nextStart:], "\n"); offset >= 0 {
		nextEnd = nextStart + offset
	}
	buffer.cursor = graphemeColumn(buffer.source, nextStart, nextEnd, column)
	return true
}

func graphemeColumn(source string, start, end, column int) int {
	position := start
	graphemes := uniseg.NewGraphemes(source[start:end])
	for i := 0; i < column && graphemes.Next(); i++ {
		_, finish := graphemes.Positions()
		position = start + finish
	}
	return position
}

func graphemeBoundary(source string, cursor int) bool {
	if cursor == 0 || cursor == len(source) {
		return true
	}
	graphemes := uniseg.NewGraphemes(source)
	for graphemes.Next() {
		_, end := graphemes.Positions()
		if end == cursor {
			return true
		}
		if end > cursor {
			return false
		}
	}
	return false
}

func (buffer *Buffer) Undo() bool {
	if len(buffer.undo) == 0 {
		return false
	}
	buffer.version++
	buffer.redo = append(buffer.redo, snapshot{buffer.source, buffer.cursor, buffer.anchor, buffer.selecting})
	last := buffer.undo[len(buffer.undo)-1]
	buffer.undo = buffer.undo[:len(buffer.undo)-1]
	buffer.source, buffer.cursor, buffer.anchor, buffer.selecting = last.source, last.cursor, last.anchor, last.selecting
	return true
}

func (buffer *Buffer) Redo() bool {
	if len(buffer.redo) == 0 {
		return false
	}
	buffer.version++
	buffer.undo = append(buffer.undo, snapshot{buffer.source, buffer.cursor, buffer.anchor, buffer.selecting})
	last := buffer.redo[len(buffer.redo)-1]
	buffer.redo = buffer.redo[:len(buffer.redo)-1]
	buffer.source, buffer.cursor, buffer.anchor, buffer.selecting = last.source, last.cursor, last.anchor, last.selecting
	return true
}

func (buffer *Buffer) saveUndo() {
	buffer.version++
	buffer.undo = append(buffer.undo, snapshot{buffer.source, buffer.cursor, buffer.anchor, buffer.selecting})
	if len(buffer.undo) > 1000 {
		buffer.undo = buffer.undo[len(buffer.undo)-1000:]
	}
	buffer.redo = nil
}

func validEdit(source string) error {
	if len(source) > MaxSourceBytes {
		return errors.New("prompt exceeds the 4 MiB editor limit")
	}
	if !utf8.ValidString(source) {
		return errors.New("prompt is not valid UTF-8")
	}
	return nil
}

func previousGrapheme(source string, cursor int) int {
	previous := 0
	graphemes := uniseg.NewGraphemes(source)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if end >= cursor {
			return start
		}
		previous = end
	}
	return previous
}

func nextGrapheme(source string, cursor int) int {
	graphemes := uniseg.NewGraphemes(source)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if start >= cursor {
			return end
		}
	}
	return len(source)
}
