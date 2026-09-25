package repl

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

type sessionContextMenu struct {
	id            string
	row, selected int
}

func (model *uiModel) sidebarSize() int {
	if model.width < 16 {
		return 0
	}
	width := model.sidebarWidth
	if width == 0 {
		width = 25
	}
	if model.width < 24 {
		return 6
	}
	if model.width < 60 {
		return min(max(8, model.width/4), model.width-12)
	}
	return min(max(20, width), model.width-40)
}

func (model *uiModel) updateSessionMenu(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return model, nil
	}
	switch key.String() {
	case "esc":
		model.sessionMenu = nil
	case "up", "ctrl+p", "down", "ctrl+n", "tab":
		model.sessionMenu.selected = 1 - model.sessionMenu.selected
	case "enter":
		return model.selectSessionMenu()
	}
	return model, nil
}

func (model *uiModel) selectSessionMenu() (tea.Model, tea.Cmd) {
	menu := model.sessionMenu
	model.sessionMenu = nil
	if menu.selected == 0 {
		return model.openRenameSession(menu.id)
	}
	return model.selectSidebarThread(menu.id)
}

func (model *uiModel) clickSessionMenu(mouse tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	menu := model.sessionMenu
	row := min(menu.row, max(0, model.height-4))
	if mouse.X > model.width-model.sidebarSize() && mouse.Y >= row+1 && mouse.Y <= row+2 && mouse.Button == tea.MouseLeft {
		menu.selected = mouse.Y - row - 1
		return model.selectSessionMenu()
	}
	model.sessionMenu = nil
	return model, nil
}

func (model *uiModel) sessionMenuView() tea.View {
	menu := model.sessionMenu
	model.sessionMenu = nil
	view := model.viewContent()
	model.sessionMenu = menu
	width := model.sidebarSize()
	if width == 0 {
		return view
	}
	lines := strings.Split(view.Content, "\n")
	row := min(menu.row, max(0, model.height-4))
	for offset, label := range []string{" Session actions", " Rename", " Switch to session", " Esc  cancel"} {
		y := row + offset
		if y >= len(lines) {
			break
		}
		if offset == menu.selected+1 {
			label = "›" + label
		}
		prefix := ansi.Truncate(lines[y], model.width-width+1, "")
		label = ansi.Truncate(label, width-1, "…")
		lines[y] = prefix + model.ink(label+strings.Repeat(" ", max(0, width-1-ansi.StringWidth(label))), model.palette().lilac, offset == menu.selected+1)
	}
	view.Content = strings.Join(lines, "\n")
	view.Cursor = nil
	return view
}
