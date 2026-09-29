package repl

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type commandPopup struct {
	choices  []commandChoice
	selected int
	start    int
	end      int
	hint     string
}

func (model *uiModel) refreshCompletion() {
	model.completion = nil
	if model.suppressCompletion || model.popup != nil || model.resumePopup != nil || model.historySearch != nil || model.settings != nil || model.showQuestion {
		return
	}
	raw, cursor := model.draft.Source(), model.draft.Cursor()
	if cursor > len(raw) {
		return
	}
	before := raw[:cursor]
	start := len(before)
	for start > 0 && !strings.ContainsAny(before[start-1:start], " \t\r\n") {
		start--
	}
	first := len(before) - len(strings.TrimLeft(before, " \t"))
	if first < len(before) && strings.HasPrefix(before[first:], "/") && !strings.Contains(before[first:], "\n") {
		start = first
	}
	if start == len(before) || (start > 0 && before[start-1] == '/' && start > 1 && before[start-2] == ':') {
		return
	}
	token := before[start:]
	if !strings.HasPrefix(token, "/") || strings.HasPrefix(token, "//") {
		return
	}
	popup := &commandPopup{start: start, end: cursor}
	name, argument, hasArguments := splitCommand(token)
	if !hasArguments {
		for _, command := range commandRegistry() {
			if strings.HasPrefix(command.Name, name) {
				value := command.Name
				popup.choices = append(popup.choices, commandChoice{Label: value, Description: command.Description, Value: value})
			}
		}

		for _, choice := range completeSkills(model, strings.TrimPrefix(name, "/")) {
			collision := false
			for _, command := range commandRegistry() {
				if command.Name == choice.Label {
					collision = true
					break
				}
			}
			if !collision {
				popup.choices = append(popup.choices, commandChoice{Label: choice.Label, Description: choice.Description, Value: choice.Label + " "})
			}
		}
	} else {
		for _, command := range commandRegistry() {
			if command.Name != name {
				continue
			}
			popup.hint = command.ArgumentHint
			if command.Complete != nil {
				popup.choices = command.Complete(model, argument)
			}
			break
		}
	}
	if len(popup.choices) != 0 || popup.hint != "" {
		model.completion = popup
	}
}

func (model *uiModel) acceptCompletion() {
	if model.completion == nil {
		return
	}
	if len(model.completion.choices) == 0 {
		model.completion = nil
		model.suppressCompletion = true
		return
	}
	popup := model.completion
	choice := popup.choices[popup.selected]
	raw := model.draft.Source()
	replacement := raw[:popup.start] + choice.Value + raw[popup.end:]
	if err := model.draft.Restore(replacement, popup.start+len(choice.Value)); err != nil {
		model.message = err.Error()
		return
	}
	model.completion = nil
	model.suppressCompletion = true
}

func (popup *commandPopup) view(width, available int) []string {
	if available < 1 {
		return nil
	}
	var lines []string
	if popup.hint != "" {
		lines = append(lines, "Argument: "+popup.hint)
	}
	if len(popup.choices) == 0 {
		return lines
	}
	start := max(0, popup.selected-max(1, available-len(lines))+1)
	limit := min(len(popup.choices), start+max(1, available-len(lines)))
	for index := start; index < limit; index++ {
		prefix := "  "
		if index == popup.selected {
			prefix = "› "
		}
		choice := popup.choices[index]
		line := sanitizeTerminal(fmt.Sprintf("%s%s · %s", prefix, choice.Label, choice.Description))
		if width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		lines = append(lines, line)
	}
	return lines
}

func completeSkills(model *uiModel, query string) []commandChoice {
	var choices []commandChoice
	words := strings.Fields(strings.ToLower(query))
	for _, entry := range model.skills.Entries {
		if !entry.UserInvocable {
			continue
		}
		candidate := strings.ToLower(entry.Skill.Name + " " + entry.Skill.Description)
		matches := true
		for _, word := range words {
			if !strings.Contains(candidate, word) {
				matches = false
				break
			}
		}
		if matches {
			choices = append(choices, commandChoice{Label: "/" + entry.Skill.Name, Description: entry.Skill.Description, Value: "$" + entry.Skill.Name + " "})
		}
	}
	return choices
}
