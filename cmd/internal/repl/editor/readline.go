package editor

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func wordCharacter(text string) bool {
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

func (buffer *Buffer) WordLeft() {
	for buffer.cursor > 0 {
		start := previousGrapheme(buffer.source, buffer.cursor)
		if wordCharacter(buffer.source[start:buffer.cursor]) {
			break
		}
		buffer.cursor = start
	}
	for buffer.cursor > 0 {
		start := previousGrapheme(buffer.source, buffer.cursor)
		if !wordCharacter(buffer.source[start:buffer.cursor]) {
			break
		}
		buffer.cursor = start
	}
}

func (buffer *Buffer) WordRight() {
	for buffer.cursor < len(buffer.source) && !wordCharacter(buffer.source[buffer.cursor:]) {
		buffer.Right()
	}
	for buffer.cursor < len(buffer.source) && wordCharacter(buffer.source[buffer.cursor:]) {
		buffer.Right()
	}
}

func (buffer *Buffer) kill(start, end int) {
	if selected, ok := buffer.Selection(); ok {
		start, end = selected.Start, selected.End
	}
	if start == end {
		return
	}
	buffer.killed = buffer.source[start:end]
	buffer.saveUndo()
	buffer.source = buffer.source[:start] + buffer.source[end:]
	buffer.cursor, buffer.selecting = start, false
}

func (buffer *Buffer) KillBackward(spaceDelimited bool) {
	end := buffer.cursor
	if spaceDelimited {
		for buffer.cursor > 0 {
			start := previousGrapheme(buffer.source, buffer.cursor)
			if strings.TrimSpace(buffer.source[start:buffer.cursor]) != "" {
				break
			}
			buffer.cursor = start
		}
		for buffer.cursor > 0 {
			start := previousGrapheme(buffer.source, buffer.cursor)
			if strings.TrimSpace(buffer.source[start:buffer.cursor]) == "" {
				break
			}
			buffer.cursor = start
		}
	} else {
		buffer.WordLeft()
	}
	start := buffer.cursor
	buffer.cursor = end
	buffer.kill(start, end)
}
func (buffer *Buffer) KillForward() {
	start := buffer.cursor
	buffer.WordRight()
	end := buffer.cursor
	buffer.cursor = start
	buffer.kill(start, end)
}
func (buffer *Buffer) KillLineStart() {
	end := buffer.cursor
	buffer.Home()
	start := buffer.cursor
	buffer.cursor = end
	buffer.kill(start, end)
}
func (buffer *Buffer) KillLineEnd() {
	start := buffer.cursor
	buffer.End()
	end := buffer.cursor
	if end == start && end < len(buffer.source) {
		end++
	}
	buffer.cursor = start
	buffer.kill(start, end)
}
func (buffer *Buffer) Yank() error { return buffer.Insert(buffer.killed) }
