package repl

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

const skipOptionID = "__unreal_agent_skip__"

type questionValue struct {
	selected string
	multiple []string
	text     string
	other    string
}

type questionForm struct {
	id        operation.ID
	plan      AskUserPlan
	form      *huh.Form
	values    []questionValue
	submitted bool
}

func newQuestionForm(pending PendingQuestion, width, height int) *questionForm {
	current := &questionForm{id: pending.OperationID, plan: pending.Plan, values: make([]questionValue, len(pending.Plan.Form.Questions))}
	var groups []*huh.Group
	for index, question := range pending.Plan.Form.Questions {
		value := &current.values[index]
		title := fmt.Sprintf("%d/%d · %s", index+1, len(pending.Plan.Form.Questions), question.Prompt)
		var fields []huh.Field
		switch question.Kind {
		case "text":
			input := huh.NewText().Title(title).Description(question.Description).Value(&value.text)
			if question.Required {
				input.Validate(func(text string) error {
					if strings.TrimSpace(text) == "" {
						return fmt.Errorf("%s is required", question.Prompt)
					}
					return nil
				})
			}
			fields = append(fields, input)
		case "single_select", "multi_select":
			options := make([]huh.Option[string], 0, len(question.Options)+1)
			recommended := make(map[string]bool, len(question.Recommended))
			for _, id := range question.Recommended {
				recommended[id] = true
			}
			for _, option := range question.Options {
				label := option.Label
				if recommended[option.ID] {
					label += " (recommended)"
				}
				if option.Description != "" {
					label += " · " + option.Description
				}
				options = append(options, huh.NewOption(label, option.ID))
			}
			if question.Kind == "single_select" {
				if !question.Required {
					options = append(options, huh.NewOption("Skip this question", skipOptionID))
				}
				fields = append(fields, huh.NewSelect[string]().Title(title).Description(question.Description).Options(options...).Value(&value.selected))
			} else {
				fields = append(fields, huh.NewMultiSelect[string]().Title(title).Description(question.Description).Options(options...).Value(&value.multiple))
			}
			if question.AllowOther {
				fields = append(fields, huh.NewInput().Title("Other answer (optional)").Value(&value.other))
			}
		}
		groups = append(groups, huh.NewGroup(fields...).Title(fmt.Sprintf("Step %d of %d", index+1, len(pending.Plan.Form.Questions))))
	}
	confirmed := false
	review := huh.NewNote().Title("Review answers").DescriptionFunc(current.review, &current.values).Height(min(8, len(current.values)+2))
	confirm := huh.NewConfirm().Title("Submit these answers?").Affirmative("Submit").Negative("Go back").Value(&confirmed).Validate(func(value bool) error {
		if !value {
			return fmt.Errorf("select Submit, or go back to edit answers")
		}
		return validateAskUserResult(current.plan.Form, current.Result())
	})
	groups = append(groups, huh.NewGroup(review, confirm).Title("Review"))
	current.form = huh.NewForm(groups...).WithWidth(max(20, width-2)).WithHeight(max(6, height-3))
	return current
}

func (current *questionForm) Init() tea.Cmd { return current.form.Init() }

func (current *questionForm) Update(message tea.Msg) tea.Cmd {
	updated, command := current.form.Update(message)
	if form, ok := updated.(*huh.Form); ok {
		current.form = form
	}
	return command
}

func (current *questionForm) Result() AskUserResult {
	result := AskUserResult{Status: "answered"}
	for index, question := range current.plan.Form.Questions {
		value := current.values[index]
		answer := QuestionAnswer{QuestionID: question.ID}
		switch question.Kind {
		case "text":
			answer.Text = value.text
			answer.Skipped = !question.Required && strings.TrimSpace(value.text) == ""
		case "single_select":
			if value.selected != "" && value.selected != skipOptionID {
				answer.Selected = []string{value.selected}
			}
			answer.Text = value.other
			if strings.TrimSpace(answer.Text) != "" {
				answer.Selected = nil
			}
			answer.Skipped = value.selected == skipOptionID || !question.Required && len(answer.Selected) == 0 && strings.TrimSpace(value.other) == ""
		case "multi_select":
			answer.Selected = append([]string(nil), value.multiple...)
			answer.Text = value.other
			answer.Skipped = !question.Required && len(answer.Selected) == 0 && strings.TrimSpace(value.other) == ""
		}
		result.Answers = append(result.Answers, answer)
	}
	return result
}

func (current *questionForm) review() string {
	result := current.Result()
	lines := make([]string, 0, len(result.Answers))
	for index, answer := range result.Answers {
		label := current.plan.Form.Questions[index].Prompt
		value := answer.Text
		if len(answer.Selected) != 0 {
			value = strings.Join(answer.Selected, ", ")
		}
		if answer.Skipped {
			value = "skipped"
		}
		if value == "" {
			value = "unanswered"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, value))
	}
	return strings.Join(lines, "\n")
}
