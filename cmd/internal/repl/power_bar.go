package repl

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type powerChoice struct {
	commandChoice
	kind, provider string
}
type powerBar struct {
	query    string
	selected int
	choices  []powerChoice
}

func (model *uiModel) openPowerBar() (tea.Model, tea.Cmd) {
	bar := &powerBar{}
	family := map[string]bool{}
	if choices, err := model.state.Family(model.ctx); err == nil {
		for _, choice := range choices {
			if choice.Issue != nil {
				continue
			}
			id := choice.Metadata.SessionID
			family[id] = true
			description := "fork"
			if id == model.state.Current.SessionID {
				description = "fork · current"
				bar.selected = len(bar.choices)
			}
			bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: choice.Metadata.Name, Description: description, Value: id}, kind: "session"})
		}
	}
	for _, panel := range model.workflowPanels() {
		name := panel.graph.Name
		if name == "" {
			name = "Loading workflow"
		}
		if panel == model.workflow && panel.visible {
			bar.selected = len(bar.choices)
		}
		identity := panel.runID
		if identity == "" {
			identity = panel.threadID
		}
		bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: name, Description: "Workflow thread · " + identity + " · " + workflowThreadStatus(panel), Value: workflowPanelID(panel)}, kind: "workflow"})
	}
	for _, command := range commandRegistry() {
		bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: command.Name, Description: command.Description, Value: command.Name}, kind: "command"})
	}
	bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: "Forks", Description: "Navigate the current session family"}, kind: "forks"})
	if sessions, err := model.state.List(model.ctx); err == nil {
		for _, choice := range sessions {
			if choice.Issue == nil && choice.Metadata.Workspace == model.state.Workspace && !family[choice.Metadata.SessionID] {
				bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: choice.Metadata.Name, Description: "session", Value: choice.Metadata.SessionID}, kind: "session"})
			}
		}
	}
	if model.catalog != nil {
		for _, provider := range model.catalog.ProviderNames() {
			bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: provider, Description: "Choose a provider model", Value: provider}, kind: "provider"})
		}
		for _, choice := range model.catalog.Choices() {
			bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: choice.Ref.ID, Description: choice.Ref.Provider, Value: choice.Ref.ID}, provider: choice.Ref.Provider, kind: "model"})
		}
	}
	for _, skill := range model.skills.Entries {
		if skill.UserInvocable {
			bar.choices = append(bar.choices, powerChoice{commandChoice: commandChoice{Label: skill.Skill.Name, Description: skill.Skill.Description, Value: "$" + skill.Skill.Name + " "}, kind: "skill"})
		}
	}
	model.powerBar = bar
	return model, nil
}
func (bar *powerBar) filtered() []powerChoice {
	result := []powerChoice{}
	for _, choice := range bar.choices {
		haystack := strings.ToLower(choice.kind + " " + choice.Label + " " + choice.Description)
		matches := true
		for _, word := range strings.Fields(strings.ToLower(bar.query)) {
			if !strings.Contains(haystack, word) {
				matches = false
				break
			}
		}
		if matches {
			result = append(result, choice)
		}
	}
	return result
}
func (model *uiModel) updatePowerBar(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	bar := model.powerBar
	choices := bar.filtered()
	switch key.String() {
	case "esc":
		model.powerBar = nil
	case "up", "ctrl+p":
		if len(choices) > 0 {
			bar.selected = (bar.selected + len(choices) - 1) % len(choices)
		}
	case "down", "ctrl+n", "tab":
		if len(choices) > 0 {
			bar.selected = (bar.selected + 1) % len(choices)
		}
	case "backspace":
		if bar.query != "" {
			_, n := utf8.DecodeLastRuneInString(bar.query)
			bar.query = bar.query[:len(bar.query)-n]
			bar.selected = 0
		}
	case "f2", "ctrl+r":
		if len(choices) > 0 && choices[bar.selected].kind == "session" {
			_, command := model.openRenameSession(choices[bar.selected].Value)
			if model.rename != nil {
				model.rename.palette = bar
				model.powerBar = nil
			}
			return model, command
		}
	case "ctrl+u":
		bar.query = ""
		bar.selected = 0
	case "enter":
		if len(choices) == 0 {
			return model, nil
		}
		choice := choices[bar.selected]
		model.powerBar = nil
		model.popup = nil
		model.resumePopup = nil
		model.historySearch = nil
		model.settings = nil
		model.rename = nil
		model.contextReport = nil
		model.sessionMenu = nil
		model.showQuestion = false
		model.completion = nil
		if model.workflow != nil && choice.kind != "workflow" && choice.kind != "session" {
			model.workflow.visible = false
		}
		switch choice.kind {
		case "command":
			if choice.Value == "/rename" {
				return model.openRenameSession(model.state.Current.SessionID)
			}
			if choice.Value == "/attach" {
				_ = model.draft.Set(choice.Value + " ")
				model.suppressCompletion = false
				model.refreshCompletion()
				return model, nil
			}
			return model.command(choice.Value)
		case "forks":
			return model.openForkPicker()
		case "skill":
			if err := model.draft.Insert(choice.Value); err != nil {
				model.message = err.Error()
			}
			return model, nil
		case "session", "workflow":
			return model.selectSidebarThread(choice.Value)
		case "provider":
			return model.openProviderPopup(choice.Value)
		case "model":
			cmd, err := model.chooseModel(choice.provider, choice.Value)
			if err != nil {
				model.message = err.Error()
			}
			return model, cmd
		}
	default:
		if key.Text != "" && len(bar.query) < 512 {
			bar.query += key.Text
			bar.selected = 0
		}
	}
	return model, nil
}
func (model *uiModel) powerBarView() tea.View {
	width := max(1, min(76, model.width-8))
	choices := model.powerBar.filtered()
	popup := commandPopup{selected: model.powerBar.selected}
	for _, choice := range choices {
		popup.choices = append(popup.choices, commandChoice{Label: choice.kind + " · " + choice.Label, Description: choice.Description})
	}
	lines := []string{model.ink("Go to anything", model.palette().lilac, true), "", "> " + sanitizeTerminal(model.powerBar.query), ""}
	lines = append(lines, popup.view(width, max(1, min(12, model.height-11)))...)
	if len(choices) == 0 {
		lines = append(lines, "No matching commands, threads, models, or skills.")
	}
	hint := "↑/↓ choose · Enter open · Esc return"
	if len(choices) > 0 && choices[model.powerBar.selected].kind == "session" {
		hint = "Enter jump · F2/Ctrl+R rename · ↑/↓ choose · Esc return"
	}
	lines = append(lines, "", hint)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	content := strings.Join(lines, "\n")
	if model.height < 11 || model.width < 20 {
		return tea.NewView(strings.Join(lines[:min(len(lines), model.height)], "\n"))
	}
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).Width(width + 6).Render(content)
	return tea.NewView(lipgloss.Place(model.width, model.height, lipgloss.Center, lipgloss.Center, panel))
}

func (model *uiModel) restorePowerBar(previous *powerBar, id string) {
	model.openPowerBar()
	model.powerBar.query = previous.query
	selectID := func() bool {
		for index, choice := range model.powerBar.filtered() {
			if choice.kind == "session" && choice.Value == id {
				model.powerBar.selected = index
				return true
			}
		}
		return false
	}
	if !selectID() {
		model.powerBar.query = ""
		selectID()
	}
}
