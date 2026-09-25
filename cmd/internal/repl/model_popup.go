package repl

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type catalogMsg CatalogResult

type modelPopup struct {
	query     string
	selected  int
	choices   []ModelChoice
	providers map[string]struct{}
}

func (popup *modelPopup) filtered() []ModelChoice {
	query := strings.ToLower(strings.TrimSpace(popup.query))
	result := make([]ModelChoice, 0, len(popup.choices))
	for _, choice := range popup.choices {
		if _, allowed := popup.providers[choice.Ref.Provider]; !allowed {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(choice.Label), query) {
			continue
		}
		result = append(result, choice)
	}
	return result
}

func (model *uiModel) openModelPopup() (tea.Model, tea.Cmd) {
	providers := make(map[string]struct{})
	for _, provider := range model.catalog.ConfiguredProviders() {
		providers[provider] = struct{}{}
	}
	model.popup = &modelPopup{choices: model.catalog.Choices(), providers: providers}
	model.draft.Clear()
	model.message = "Type to search. Enter selects. F5 refreshes. Esc closes."
	return model, model.refreshCatalog(false)
}

func (model *uiModel) openProviderPopup(provider string) (tea.Model, tea.Cmd) {
	model.popup = &modelPopup{choices: model.catalog.Choices(), providers: map[string]struct{}{provider: {}}}
	model.draft.Clear()
	model.message = "Choose a " + provider + " model. F5 refreshes. Esc closes."
	return model, model.refreshCatalog(false)
}

func (model *uiModel) refreshCatalog(force bool) tea.Cmd {
	var commands []tea.Cmd
	for provider := range model.popup.providers {
		generation := model.catalog.BeginRefresh(provider)
		provider := provider
		commands = append(commands, func() tea.Msg {
			var result CatalogResult
			if force {
				result = model.catalog.Refresh(context.Background(), provider, generation)
			} else {
				result = model.catalog.Discover(context.Background(), provider, generation)
			}
			return catalogMsg(result)
		})
	}
	return tea.Batch(commands...)
}

func (model *uiModel) updateModelPopup(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filtered := model.popup.filtered()
	switch key.String() {
	case "esc":
		model.popup = nil
		model.message = ""
		return model, nil
	case "f5":
		model.message = "Refreshing model catalogs..."
		return model, model.refreshCatalog(true)
	case "up":
		if model.popup.selected > 0 {
			model.popup.selected--
		}
	case "down":
		if model.popup.selected+1 < len(filtered) {
			model.popup.selected++
		}
	case "enter", "tab":
		if len(filtered) == 0 {
			return model, nil
		}
		choice := filtered[model.popup.selected]
		cmd, err := model.chooseModel(choice.Ref.Provider, choice.Ref.ID)
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		model.popup = nil
		model.message = "Selected " + choice.Label + " for the next model request."
		return model, cmd
	case "backspace":
		if len(model.popup.query) > 0 {
			_, size := utf8.DecodeLastRuneInString(model.popup.query)
			model.popup.query = model.popup.query[:len(model.popup.query)-size]
			model.popup.selected = 0
		}
	default:
		if key.Text != "" {
			model.popup.query += key.Text
			model.popup.selected = 0
		}
	}
	return model, nil
}

func (popup *modelPopup) view(width, available int) []string {
	if available < 1 {
		return nil
	}
	lines := []string{"Models: " + popup.query}
	filtered := popup.filtered()
	if len(filtered) == 0 {
		return append(lines, "  No matching models. Use /model <provider> <model-id> for a manual ID.")
	}
	start := max(0, popup.selected-max(1, available-2)+1)
	limit := min(len(filtered), start+available-1)
	for index := start; index < limit; index++ {
		prefix := "  "
		if index == popup.selected {
			prefix = "› "
		}
		choice := filtered[index]
		line := sanitizeTerminal(fmt.Sprintf("%s%s [%s]", prefix, choice.Label, choice.Source))
		if width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		lines = append(lines, line)
	}
	return lines
}
