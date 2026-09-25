package repl

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type historySearchPopup struct {
	entries  []HistoryEntry
	query    string
	matches  []int
	selected int
}

func (model *uiModel) openHistorySearch() (tea.Model, tea.Cmd) {
	entries, err := model.history.Entries()
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	if len(entries) == 0 {
		model.message = "No prompt or command history yet."
		return model, nil
	}
	model.historySearch = &historySearchPopup{entries: entries}
	model.historySearch.filter()
	model.message = ""
	return model, nil
}

func (model *uiModel) updateHistorySearch(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	search := model.historySearch
	switch key.String() {
	case "esc":
		model.historySearch = nil
		model.message = ""
	case "enter", "tab":
		if len(search.matches) == 0 {
			return model, nil
		}
		entry := search.entries[search.matches[search.selected]]
		if err := model.draft.Set(entry.Text); err != nil {
			model.message = err.Error()
			return model, nil
		}
		model.cleanupAttachments()
		model.cleanupHistoryDraftAttachments()
		model.historyIndex = -1
		model.attachments = model.attachmentsForHistory(entry)
		model.ensureImageChips()
		model.historySearch = nil
		notice := model.appendNotice("Loaded " + entry.Kind + " from history.")
		model.message = ""
		for _, attachment := range model.attachments {
			if attachment.Missing {
				model.message = "History image " + attachment.ID + " is missing; remove it with Ctrl+X before sending."
				break
			}
		}
		model.refreshCompletion()
		return model, notice
	case "ctrl+r", "down", "ctrl+n":
		if len(search.matches) > 0 {
			search.selected = (search.selected + 1) % len(search.matches)
		}
	case "up", "ctrl+p":
		if len(search.matches) > 0 {
			search.selected = (search.selected + len(search.matches) - 1) % len(search.matches)
		}
	case "backspace":
		if search.query != "" {
			_, size := utf8.DecodeLastRuneInString(search.query)
			search.query = search.query[:len(search.query)-size]
			search.filter()
		}
	default:
		if key.Text != "" {
			search.appendQuery(key.Text)
		}
	}
	return model, nil
}

func (search *historySearchPopup) appendQuery(text string) {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if utf8.RuneCountInString(search.query)+utf8.RuneCountInString(text) > 256 {
		return
	}
	search.query += text
	search.filter()
}

func (search *historySearchPopup) filter() {
	search.matches = nil
	query := strings.ToLower(strings.TrimSpace(search.query))
	var fuzzy []int
	for index := len(search.entries) - 1; index >= 0; index-- {
		candidate := strings.ToLower(search.entries[index].Text)
		if query == "" || strings.Contains(candidate, query) {
			search.matches = append(search.matches, index)
		} else if historySubsequence(candidate, query) {
			fuzzy = append(fuzzy, index)
		}
	}
	search.matches = append(search.matches, fuzzy...)
	search.selected = 0
}

func historySubsequence(candidate, query string) bool {
	remaining := []rune(query)
	for _, character := range candidate {
		if len(remaining) > 0 && character == remaining[0] {
			remaining = remaining[1:]
		}
	}
	return len(remaining) == 0
}

func (search *historySearchPopup) view(width, available int, noColor bool) []string {
	if available < 1 {
		return nil
	}
	header := "⌕ History  " + search.query
	if search.query == "" {
		header += "↑/↓ choose · Enter load · type to filter"
	}
	if !noColor {
		header = lipgloss.NewStyle().Foreground(lipgloss.Color("#87A9F6")).Bold(true).Render(header)
	}
	lines := []string{ansi.Truncate(header, width, "…")}
	if available == 1 {
		return lines
	}
	if len(search.matches) == 0 {
		return append(lines, "  No matching history")
	}
	visible := min(7, available-1)
	start := max(0, search.selected-visible+1)
	for position := start; position < min(len(search.matches), start+visible); position++ {
		entry := search.entries[search.matches[position]]
		prefix := "  "
		if position == search.selected {
			prefix = "› "
		}
		preview := strings.NewReplacer("\n", " ↵ ", "\t", " ").Replace(sanitizeTerminal(entry.Text))
		line := fmt.Sprintf("%s%-7s %s", prefix, entry.Kind, preview)
		line = ansi.Truncate(line, width, "…")
		if !noColor && position == search.selected {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("#F4BE78")).Bold(true).Render(line)
		}
		lines = append(lines, line)
	}
	return lines
}
