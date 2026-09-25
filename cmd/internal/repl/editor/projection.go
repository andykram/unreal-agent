// Package editor keeps prompt source separate from its terminal projection.
package editor

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type SourceRange struct {
	Start int
	End   int
}

type SyntaxSpan struct {
	Kind       string
	Source     SourceRange
	Delimiters []SourceRange
	// SGR is an optional SGR parameter sequence, such as "38;2;1;2;3".
	// ProjectParsed renders the span content with that style in SelectedLines
	// and restores the default foreground with SGR 39. SGR spans must not
	// overlap each other. Lines stays plain so callers can read source text.
	SGR string
}

type DisplayCell struct {
	Row    int
	Column int
	Width  int
	Source SourceRange
}

type Projection struct {
	Lines         []string
	SelectedLines []string
	Cells         []DisplayCell
	CursorRow     int
	CursorColumn  int
	VisualRows    int
}

// Project maps the raw UTF-8 prompt to visible terminal cells. Unsupported or
// incomplete Markdown remains literal source, so editing never loses bytes.
func Project(source string, cursor, width int, markdown bool, selection ...SourceRange) Projection {
	var spans []SyntaxSpan
	if markdown {
		spans = ParseSyntax(source)
	}
	return ProjectParsed(source, cursor, width, markdown, spans, selection...)
}

// ParseSyntax caches parser work across cursor motion for an unchanged draft.
func ParseSyntax(source string) []SyntaxSpan {
	if !utf8.ValidString(source) {
		return nil
	}
	return syntaxSpans([]byte(source))
}

// ProjectParsed projects source using syntax spans for that exact source version.
func ProjectParsed(source string, cursor, width int, markdown bool, spans []SyntaxSpan, selection ...SourceRange) Projection {
	if width < 1 {
		width = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(source) {
		cursor = len(source)
	}
	hidden := make([]bool, len(source))
	if markdown && utf8.ValidString(source) {
		boundaries := []int{0}
		clusters := uniseg.NewGraphemes(source)
		for clusters.Next() {
			_, end := clusters.Positions()
			boundaries = append(boundaries, end)
		}
		for _, span := range spans {
			selected := len(selection) != 0 && selection[0].Start < span.Source.End && selection[0].End > span.Source.Start
			if selected || spanActiveBoundaries(boundaries, cursor, span.Source) {
				continue
			}
			for _, delimiter := range span.Delimiters {
				for offset := delimiter.Start; offset < delimiter.End; offset++ {
					hidden[offset] = true
				}
			}
		}
	}
	result := Projection{Lines: []string{""}, SelectedLines: []string{""}}
	row, column := 0, 0
	selectedStyle := false
	type colorInterval struct {
		start, end int
		sgr        string
	}
	var intervals []colorInterval
	for _, span := range spans {
		if span.SGR != "" && span.Source.Start < span.Source.End {
			intervals = append(intervals, colorInterval{span.Source.Start, span.Source.End, span.SGR})
		}
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	openColor, openEnd := "", 0
	closeColor := func() {
		if openColor != "" {
			result.SelectedLines[row] += "\x1b[39m"
			openColor = ""
		}
	}
	appendSelected := func(value string, selected bool) {
		if selected != selectedStyle {
			if selected {
				result.SelectedLines[row] += "\x1b[7m"
			} else {
				result.SelectedLines[row] += "\x1b[27m"
			}
			selectedStyle = selected
		}
		result.SelectedLines[row] += value
	}
	graphemes := uniseg.NewGraphemes(source)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if start == cursor {
			result.CursorRow, result.CursorColumn = row, column
		}
		piece := source[start:end]
		if piece == "\n" {
			if selectedStyle {
				result.SelectedLines[row] += "\x1b[27m"
				selectedStyle = false
			}
			closeColor()
			row++
			column = 0
			result.Lines = append(result.Lines, "")
			result.SelectedLines = append(result.SelectedLines, "")
			continue
		}
		if hidden[start] {
			continue
		}
		display := piece
		cellWidth := uniseg.StringWidth(piece)
		if piece == "\t" {
			cellWidth = 8 - column%8
			display = strings.Repeat(" ", cellWidth)
		} else if letter, size := utf8.DecodeRuneInString(piece); size == len(piece) && unicode.IsControl(letter) {
			if letter <= 0x1f {
				display = string(rune(0x2400) + letter)
			} else if letter == 0x7f {
				display = "␡"
			} else {
				display = "�"
			}
			cellWidth = uniseg.StringWidth(display)
		}
		if cellWidth < 1 {
			cellWidth = 1
		}
		if column > 0 && column+cellWidth > width {
			if selectedStyle {
				result.SelectedLines[row] += "\x1b[27m"
				selectedStyle = false
			}
			closeColor()
			row++
			column = 0
			result.Lines = append(result.Lines, "")
			result.SelectedLines = append(result.SelectedLines, "")
		}
		if openColor != "" && start >= openEnd {
			closeColor()
		}
		if openColor == "" {
			for _, interval := range intervals {
				if interval.start > start {
					break
				}
				if start < interval.end {
					openColor, openEnd = interval.sgr, interval.end
					result.SelectedLines[row] += "\x1b[" + interval.sgr + "m"
					break
				}
			}
		}
		result.Cells = append(result.Cells, DisplayCell{Row: row, Column: column, Width: cellWidth, Source: SourceRange{start, end}})
		result.Lines[row] += display
		selected := len(selection) != 0 && selection[0].Start < end && selection[0].End > start
		appendSelected(display, selected)
		column += cellWidth
	}
	if selectedStyle {
		result.SelectedLines[row] += "\x1b[27m"
	}
	closeColor()
	if cursor == len(source) {
		result.CursorRow, result.CursorColumn = row, column
	}
	result.VisualRows = len(result.Lines)
	return result
}

func spanActive(source string, cursor int, span SourceRange) bool {
	graphemes := uniseg.NewGraphemes(source)
	boundaries := []int{0}
	for graphemes.Next() {
		_, end := graphemes.Positions()
		boundaries = append(boundaries, end)
	}
	return spanActiveBoundaries(boundaries, cursor, span)
}

func spanActiveBoundaries(boundaries []int, cursor int, span SourceRange) bool {
	before, after := span.Start, span.End
	if index := sort.SearchInts(boundaries, span.Start); index > 0 && index < len(boundaries) && boundaries[index] == span.Start {
		before = boundaries[index-1]
	}
	if index := sort.SearchInts(boundaries, span.End); index < len(boundaries)-1 && boundaries[index] == span.End {
		after = boundaries[index+1]
	}
	return cursor >= before && cursor <= after
}

func syntaxSpans(source []byte) []SyntaxSpan {
	root := goldmark.New(goldmark.WithExtensions(extension.Strikethrough)).Parser().Parse(text.NewReader(source))
	var result []SyntaxSpan
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Heading:
			lines := typed.Lines()
			if lines.Len() == 0 {
				break
			}
			line := lines.At(0)
			lineStart := bytes.LastIndexByte(source[:line.Start], '\n') + 1
			if line.Start > lineStart && bytes.Count(source[lineStart:line.Start], []byte{'#'}) == typed.Level {
				result = append(result, SyntaxSpan{
					Kind: "heading", Source: SourceRange{lineStart, line.Stop},
					Delimiters: []SourceRange{{lineStart, line.Start}},
				})
			}
		case *ast.Emphasis:
			start, end, ok := childSourceRange(typed)
			if !ok || start < typed.Level || end+typed.Level > len(source) {
				break
			}
			left := source[start-typed.Level : start]
			right := source[end : end+typed.Level]
			if len(left) == typed.Level && bytes.Equal(left, right) && (left[0] == '*' || left[0] == '_') {
				result = append(result, SyntaxSpan{
					Kind: "emphasis", Source: SourceRange{start - typed.Level, end + typed.Level},
					Delimiters: []SourceRange{{start - typed.Level, start}, {end, end + typed.Level}},
				})
			}
		case *extensionast.Strikethrough:
			if span, ok := symmetricInlineSpan(source, typed, []byte("~~"), "strikethrough"); ok {
				result = append(result, span)
			}
		case *ast.CodeSpan:
			if span, ok := codeSpan(source, typed); ok {
				result = append(result, span)
			}
		case *ast.Link:
			if span, ok := linkSpan(source, typed); ok {
				result = append(result, span)
			}
		}
		return ast.WalkContinue, nil
	})
	result = append(result, lineSyntaxSpans(source)...)
	return result
}

func symmetricInlineSpan(source []byte, node ast.Node, marker []byte, kind string) (SyntaxSpan, bool) {
	start, end, ok := childSourceRange(node)
	length := len(marker)
	if !ok || start < length || end+length > len(source) || !bytes.Equal(source[start-length:start], marker) || !bytes.Equal(source[end:end+length], marker) {
		return SyntaxSpan{}, false
	}
	return SyntaxSpan{Kind: kind, Source: SourceRange{start - length, end + length}, Delimiters: []SourceRange{{start - length, start}, {end, end + length}}}, true
}

func codeSpan(source []byte, node *ast.CodeSpan) (SyntaxSpan, bool) {
	start, end, ok := childSourceRange(node)
	if !ok || start < 1 || end >= len(source) {
		return SyntaxSpan{}, false
	}
	for length := 1; length <= 16 && start-length >= 0 && end+length <= len(source); length++ {
		marker := bytes.Repeat([]byte{'`'}, length)
		if bytes.Equal(source[start-length:start], marker) && bytes.Equal(source[end:end+length], marker) {
			if start-length > 0 && source[start-length-1] == '`' || end+length < len(source) && source[end+length] == '`' {
				continue
			}
			return SyntaxSpan{Kind: "inline_code", Source: SourceRange{start - length, end + length}, Delimiters: []SourceRange{{start - length, start}, {end, end + length}}}, true
		}
	}
	return SyntaxSpan{}, false
}

func linkSpan(source []byte, node *ast.Link) (SyntaxSpan, bool) {
	start, end, ok := childSourceRange(node)
	if !ok || start < 1 {
		return SyntaxSpan{}, false
	}
	open := start - 1
	for open >= 0 && source[open] != '\n' && start-open <= 1024 {
		if source[open] == '[' && (open == 0 || source[open-1] != '\\') {
			break
		}
		open--
	}
	if open < 0 || source[open] != '[' {
		return SyntaxSpan{}, false
	}
	closeLabel := end
	for closeLabel < len(source) && closeLabel-end <= 1024 && source[closeLabel] != '\n' && source[closeLabel] != ']' {
		closeLabel++
	}
	if closeLabel+1 >= len(source) || source[closeLabel] != ']' || source[closeLabel+1] != '(' {
		return SyntaxSpan{}, false
	}
	depth := 1
	close := closeLabel + 2
	for ; close < len(source) && close-(closeLabel+2) <= 4096; close++ {
		if source[close] == '\n' {
			return SyntaxSpan{}, false
		}
		if source[close] == '\\' {
			close++
			continue
		}
		if source[close] == '(' {
			depth++
		}
		if source[close] == ')' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if close >= len(source) || depth != 0 {
		return SyntaxSpan{}, false
	}
	return SyntaxSpan{Kind: "link", Source: SourceRange{open, close + 1}, Delimiters: []SourceRange{{open, open + 1}, {closeLabel, close + 1}}}, true
}

func lineSyntaxSpans(source []byte) []SyntaxSpan {
	var result []SyntaxSpan
	type sourceLine struct {
		start, end int
		fenced     bool
	}
	var lines []sourceLine
	for start := 0; start < len(source); {
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += start
		}
		lines = append(lines, sourceLine{start: start, end: end})
		if end == len(source) {
			break
		}
		start = end + 1
	}
	openFence := -1
	var fence byte
	var fenceLength int
	for index := range lines {
		line := source[lines[index].start:lines[index].end]
		marker := 0
		for marker < len(line) && marker < 3 && line[marker] == ' ' {
			marker++
		}
		if marker >= len(line) || line[marker] != '`' && line[marker] != '~' {
			continue
		}
		length := 0
		for marker+length < len(line) && line[marker+length] == line[marker] {
			length++
		}
		if length < 3 {
			continue
		}
		if openFence < 0 {
			openFence, fence, fenceLength = index, line[marker], length
			continue
		}
		if line[marker] != fence || length < fenceLength || len(bytes.TrimSpace(line[marker+length:])) != 0 {
			continue
		}
		opening := lines[openFence]
		closing := lines[index]
		result = append(result, SyntaxSpan{Kind: "fence", Source: SourceRange{opening.start, closing.end}, Delimiters: []SourceRange{{opening.start, opening.end}, {closing.start, closing.end}}})
		for covered := openFence; covered <= index; covered++ {
			lines[covered].fenced = true
		}
		openFence = -1
	}
	if openFence >= 0 {
		for covered := openFence; covered < len(lines); covered++ {
			lines[covered].fenced = true
		}
	}
	for _, position := range lines {
		if position.fenced {
			continue
		}
		start, end := position.start, position.end
		line := source[start:end]
		marker := 0
		for marker < len(line) && marker < 3 && line[marker] == ' ' {
			marker++
		}
		if marker < len(line) && line[marker] == '>' {
			stop := marker + 1
			if stop < len(line) && line[stop] == ' ' {
				stop++
			}
			result = append(result, SyntaxSpan{Kind: "blockquote", Source: SourceRange{start + marker, end}, Delimiters: []SourceRange{{start + marker, start + stop}}})
		} else if marker < len(line) && (line[marker] == '-' || line[marker] == '+' || line[marker] == '*') && marker+1 < len(line) && line[marker+1] == ' ' {
			result = append(result, SyntaxSpan{Kind: "list", Source: SourceRange{start + marker, end}, Delimiters: []SourceRange{{start + marker, start + marker + 2}}})
			if marker+6 <= len(line) && line[marker+2] == '[' && (line[marker+3] == ' ' || line[marker+3] == 'x' || line[marker+3] == 'X') && line[marker+4] == ']' && line[marker+5] == ' ' {
				result = append(result, SyntaxSpan{Kind: "task", Source: SourceRange{start + marker, end}, Delimiters: []SourceRange{{start + marker + 2, start + marker + 6}}})
			}
		} else {
			index := marker
			for index < len(line) && index-marker < 9 && line[index] >= '0' && line[index] <= '9' {
				index++
			}
			if index > marker && index+1 < len(line) && (line[index] == '.' || line[index] == ')') && line[index+1] == ' ' {
				result = append(result, SyntaxSpan{Kind: "list", Source: SourceRange{start + marker, end}, Delimiters: []SourceRange{{start + marker, start + index + 2}}})
			}
		}
	}
	return result
}

func childSourceRange(parent ast.Node) (int, int, bool) {
	start, end := -1, -1
	_ = ast.Walk(parent, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node == parent {
			return ast.WalkContinue, nil
		}
		if value, ok := node.(*ast.Text); ok {
			if start < 0 || value.Segment.Start < start {
				start = value.Segment.Start
			}
			if value.Segment.Stop > end {
				end = value.Segment.Stop
			}
		}
		return ast.WalkContinue, nil
	})
	return start, end, start >= 0
}
