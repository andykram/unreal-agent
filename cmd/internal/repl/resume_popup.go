package repl

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type resumePopup struct {
	choices  []SessionChoice
	selected int
	startup  bool
	forks    bool
}

func (model *uiModel) openForkPicker() (tea.Model, tea.Cmd) {
	choices, err := model.state.Family(model.ctx)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	popup := &resumePopup{choices: choices, forks: true}
	for i, choice := range choices {
		if choice.Metadata.SessionID == model.state.Current.SessionID {
			popup.selected = i
			break
		}
	}
	model.resumePopup = popup
	model.message = ""
	return model, nil
}

func (model *uiModel) openResumePopup(startup bool) error {
	choices, err := model.state.List(model.ctx)
	if err != nil {
		return err
	}
	filtered := make([]SessionChoice, 0, len(choices))
	for _, choice := range choices {
		if choice.Metadata.Workspace == model.state.Workspace || choice.Issue != nil {
			filtered = append(filtered, choice)
		}
	}
	model.resumePopup = &resumePopup{choices: filtered, startup: startup}
	return nil
}

func (model *uiModel) updateResumePopup(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	popup := model.resumePopup
	switch key.String() {
	case "esc":
		if popup.startup {
			return model, tea.Quit
		}
		model.resumePopup = nil
		model.message = ""
	case "up":
		if popup.selected > 0 {
			popup.selected--
		}
	case "down":
		if popup.selected+1 < len(popup.choices) {
			popup.selected++
		}
	case "enter":
		if len(popup.choices) == 0 {
			model.message = "No saved sessions in this workspace. Press Ctrl+N for a new session."
			return model, nil
		}
		choice := popup.choices[popup.selected]
		if choice.Issue != nil {
			model.message = choice.Issue.Error()
			return model, nil
		}
		if popup.forks && choice.Metadata.SessionID == model.state.Current.SessionID {
			model.resumePopup = nil
			return model, nil
		}
		cmd, err := model.switchSession(choice.Metadata.SessionID)
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		model.resumePopup = nil
		model.draft.Clear()
		model.message = "Switched to " + choice.Metadata.Name
		return model, cmd
	case "ctrl+n":
		if popup.forks {
			return model, nil
		}
		cmd, err := model.newSession()
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		model.resumePopup = nil
		model.message = "Started a new session."
		return model, cmd
	}
	return model, nil
}

func (model *uiModel) switchSession(nameOrID string) (tea.Cmd, error) {
	if model.runtime != nil && (model.runtime.IsBusy() || model.runtime.QueueLength() != 0) {
		return nil, fmt.Errorf("finish active work and queued prompts before changing sessions")
	}
	oldID := model.state.Current.SessionID
	if oldID != "" {
		if model.runtime != nil {
			model.runtime.Close()
			model.runtime = nil
		}
		if err := model.state.Close(); err != nil {
			return nil, err
		}
	}
	if _, err := model.state.Resume(model.ctx, nameOrID); err != nil {
		if oldID != "" {
			_, _ = model.state.Resume(model.ctx, oldID)
			_, _ = model.startRuntime()
		}
		return nil, err
	}
	model.cleanupAttachments()
	model.cleanupHistoryDraftAttachments()
	model.question = nil
	model.showQuestion = false
	return model.startRuntime()
}

func (model *uiModel) newSession() (tea.Cmd, error) {
	if model.runtime != nil && (model.runtime.IsBusy() || model.runtime.QueueLength() != 0) {
		return nil, fmt.Errorf("finish active work and queued prompts before starting a new session")
	}
	if model.runtime != nil {
		model.runtime.Close()
		model.runtime = nil
	}
	if err := model.state.Close(); err != nil {
		return nil, err
	}
	if _, err := model.state.New(model.ctx); err != nil {
		return nil, err
	}
	model.cleanupAttachments()
	model.cleanupHistoryDraftAttachments()
	model.question = nil
	model.showQuestion = false
	return model.startRuntime()
}

func (model *uiModel) startRuntime() (tea.Cmd, error) {
	if model.router == nil {
		if err := model.loadTranscript(); err != nil {
			return nil, err
		}
		model.refreshForkChoices()
		model.refreshContextSnapshot()
		return model.nextTaskTick(), nil
	}
	runtime, err := newAppRuntime(model.ctx, model.state, model.router, model.getenv, model.heartbeat)
	if err != nil {
		return nil, err
	}
	model.runtime = runtime
	if err := model.loadTranscript(); err != nil {
		return nil, err
	}
	model.refreshForkChoices()
	model.refreshContextSnapshot()
	return tea.Batch(model.waitRuntime(), model.nextTaskTick()), nil
}

func (popup *resumePopup) view(width, available int) []string {
	heading := "Resume session · Enter select · Ctrl+N new · Esc cancel"
	if popup.forks {
		heading = "Session forks · ↑/↓ choose · Enter switch · Esc cancel"
	}
	lines := []string{heading}
	if len(popup.choices) == 0 {
		return append(lines, "No saved sessions in this workspace.")
	}
	start := max(0, popup.selected-max(1, available-2)+1)
	limit := min(len(popup.choices), start+max(1, available-1))
	for index := start; index < limit; index++ {
		choice := popup.choices[index]
		name := choice.Metadata.Name
		if popup.forks {
			name = strings.Repeat("  ", choice.Depth) + name
		}
		if name == "" {
			name = choice.Metadata.SessionID
		}
		if choice.Issue != nil {
			name += " [unavailable]"
		}
		if choice.Unfinished != 0 {
			name += fmt.Sprintf(" [%d unfinished]", choice.Unfinished)
		}
		if len(choice.Providers) != 0 {
			name += " [prior " + strings.Join(choice.Providers, ",") + "]"
		}
		prefix := "  "
		if index == popup.selected {
			prefix = "› "
		}
		line := sanitizeTerminal(prefix + name + " · " + choice.LastActive.Local().Format("2006-01-02 15:04") + " · " + choice.Metadata.SessionID)
		if width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return lines
}
