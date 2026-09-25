package repl

import (
	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
)

func handleReadline(buffer *editor.Buffer, key tea.KeyPressMsg) bool {
	switch key.String() {
	case "ctrl+z", "ctrl+_":
		buffer.Undo()
	case "ctrl+shift+z", "alt+_":
		buffer.Redo()
	case "alt+b", "alt+left":
		buffer.ClearSelection()
		buffer.WordLeft()
	case "alt+f", "alt+right":
		buffer.ClearSelection()
		buffer.WordRight()
	case "ctrl+w":
		buffer.KillBackward(true)
	case "alt+backspace":
		buffer.KillBackward(false)
	case "alt+d":
		buffer.KillForward()
	case "ctrl+u":
		buffer.KillLineStart()
	case "alt+k":
		buffer.KillLineEnd()
	case "ctrl+y":
		_ = buffer.Yank()
	default:
		return false
	}
	return true
}
