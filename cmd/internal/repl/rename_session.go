package repl

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
)

type renameSessionForm struct {
	id, name, err string
	draft         editor.Buffer
	palette       *powerBar
}

func (model *uiModel) openRenameSession(id string) (tea.Model, tea.Cmd) {
	choices, err := model.state.List(model.ctx)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	for _, choice := range choices {
		if choice.Issue != nil || choice.Metadata.Workspace != model.state.Workspace || choice.Metadata.SessionID != id {
			continue
		}
		current := &renameSessionForm{id: id, name: choice.Metadata.Name}
		_ = current.draft.Set(current.name)
		model.rename = current
		return model, nil
	}
	return model, nil
}

func (model *uiModel) updateRenameSession(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && key.String() == "esc" {
		model.powerBar = model.rename.palette
		model.rename = nil
		return model, nil
	}
	current := model.rename
	switch value := message.(type) {
	case tea.PasteMsg:
		_ = current.draft.Insert(strings.NewReplacer("\n", " ", "\r", " ").Replace(sanitizeTerminal(value.Content)))
	case tea.KeyPressMsg:
		if handleReadline(&current.draft, value) {
			return model, nil
		}
		switch value.String() {
		case "enter":
			current.name = current.draft.Source()
			if err := validateSessionName(current.name); err != nil {
				current.err = err.Error()
				return model, nil
			}
			if err := model.state.RenameSession(model.ctx, current.id, current.name); err != nil {
				current.err = err.Error()
				return model, nil
			}
			model.rename = nil
			model.refreshForkChoices()
			if current.palette != nil {
				model.restorePowerBar(current.palette, current.id)
			}
			return model, model.appendNotice("Session renamed.")
		case "left", "ctrl+b":
			current.draft.Left()
		case "right", "ctrl+f":
			current.draft.Right()
		case "home", "ctrl+a":
			current.draft.Home()
		case "end", "ctrl+e":
			current.draft.End()
		case "backspace", "ctrl+h":
			current.draft.Backspace()
		case "delete":
			current.draft.Delete()
		case "ctrl+k":
			current.draft.KillLineEnd()
		default:
			if value.Text != "" {
				_ = current.draft.Insert(value.Text)
			}
		}
	}
	return model, nil
}

func (current *renameSessionForm) view(width int) string {
	inner := max(1, min(54, width-14))
	projection := editor.Project(current.draft.Source(), current.draft.Cursor(), max(1, inner-1), false)
	line := projection.Lines[projection.CursorRow]
	text := ansi.Cut(line, 0, projection.CursorColumn) + "▏" + ansi.Cut(line, projection.CursorColumn, inner)
	return "Rename session\n\n" + text + "\n"
}

func (state *SessionState) RenameSession(ctx context.Context, id, name string) error {
	if id == state.Current.SessionID {
		return state.Rename(ctx, name)
	}
	target, err := OpenSessionState(state.Directory, state.Workspace)
	if err != nil {
		return err
	}
	target.Store = state.Store
	defer target.Close()
	if _, err := target.Resume(ctx, id); err != nil {
		return err
	}
	return target.Rename(ctx, name)
}
