package repl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
)

type editorFinishedMsg struct {
	path      string
	sessionID string
	err       error
}

func parseEditorWords(command string) ([]string, error) {
	var words []string
	var current strings.Builder
	var quote rune
	escaped := false
	inWord := false
	for _, letter := range command {
		if escaped {
			current.WriteRune(letter)
			escaped = false
			inWord = true
			continue
		}
		if letter == '\\' && quote != '\'' {
			escaped = true
			inWord = true
			continue
		}
		if quote != 0 {
			if letter == quote {
				quote = 0
			} else {
				current.WriteRune(letter)
			}
			continue
		}
		if letter == '\'' || letter == '"' {
			quote = letter
			inWord = true
			continue
		}
		if letter == ' ' || letter == '\t' || letter == '\n' {
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
			continue
		}
		current.WriteRune(letter)
		inWord = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("external editor command has an unfinished quote or escape")
	}
	if inWord {
		words = append(words, current.String())
	}
	if len(words) == 0 || words[0] == "" {
		return nil, errors.New("external editor command is empty")
	}
	return words, nil
}

func (model *uiModel) openExternalEditor() tea.Cmd {
	if model.editorPath != "" {
		model.message = "External editor is already open."
		return nil
	}
	command := strings.TrimSpace(model.config.Current().Editor.Command)
	if command == "" {
		command = strings.TrimSpace(model.getenv("EDITOR"))
	}
	if command == "" {
		command = strings.TrimSpace(model.getenv("VISUAL"))
	}
	if command == "" {
		command = "vi"
	}
	words, err := parseEditorWords(command)
	if err != nil {
		model.message = err.Error()
		return nil
	}
	file, err := os.CreateTemp(filepath.Join(model.state.Directory, "tmp"), "draft-*.md")
	if err != nil {
		model.message = err.Error()
		return nil
	}
	path := file.Name()
	if err := file.Chmod(0600); err != nil {
		file.Close()
		os.Remove(path)
		model.message = err.Error()
		return nil
	}
	if _, err := file.WriteString(model.draft.Source()); err != nil {
		file.Close()
		os.Remove(path)
		model.message = err.Error()
		return nil
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		model.message = err.Error()
		return nil
	}
	model.editorPath = path
	process := exec.CommandContext(model.ctx, words[0], append(words[1:], path)...)
	process.Dir = model.state.Workspace
	sessionID := model.state.Current.SessionID
	return tea.ExecProcess(process, func(err error) tea.Msg { return editorFinishedMsg{path: path, sessionID: sessionID, err: err} })
}

func (model *uiModel) finishExternalEditor(message editorFinishedMsg) {
	model.editorPath = ""
	if message.sessionID != model.state.Current.SessionID {
		_ = os.Remove(message.path)
		return
	}
	info, err := os.Stat(message.path)
	if err != nil {
		model.message = fmt.Sprintf("external editor failed to save %s: %v", message.path, err)
		return
	}
	if info.Size() > editor.MaxSourceBytes {
		model.message = "External editor draft exceeds the 4 MiB limit; recovery file: " + message.path
		return
	}
	data, err := os.ReadFile(message.path)
	if err != nil {
		model.message = fmt.Sprintf("cannot read external editor draft; recovery file %s: %v", message.path, err)
		return
	}
	if err := model.draft.Set(string(data)); err != nil {
		model.message = fmt.Sprintf("cannot import external editor draft; recovery file %s: %v", message.path, err)
		return
	}
	model.historyIndex = -1
	_ = os.Remove(message.path)
	if message.err != nil {
		model.message = fmt.Sprintf("Imported saved draft; external editor exited with error: %v", message.err)
	} else {
		model.message = "Imported draft from external editor."
	}
}
