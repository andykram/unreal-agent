package repl

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

type transcriptBlock struct {
	kind, text, key, detail, outcome, arguments string
	expires                                     time.Time
	hidden                                      bool
	expanded                                    bool
	width                                       int
	theme                                       string
	noColor                                     bool
	rows                                        []string
}

type transcriptRow struct {
	text  string
	block int
}

type noticeExpiredMsg time.Time

func (model *uiModel) appendNotice(text string) tea.Cmd {
	expires := time.Now().Add(time.Minute)
	model.transcript = append(model.transcript, transcriptBlock{kind: "notice", text: sanitizeTerminal(text), expires: expires})
	model.followTranscript = true
	return tea.Tick(time.Until(expires), func(now time.Time) tea.Msg { return noticeExpiredMsg(now) })
}

func (model *uiModel) appendText(kind, text string) {
	model.transcript = append(model.transcript, transcriptBlock{kind: kind, text: sanitizeTerminal(text)})
}

func (model *uiModel) appendItem(item sessionstore.Item) {
	switch item.Kind {
	case sessionstore.ItemFork:
		clear(model.runningTools)
		for i := range model.transcript {
			block := &model.transcript[i]
			if block.kind == "tool" && (block.outcome == "running" || block.outcome == "ready" || block.outcome == "awaiting" || block.outcome == "canceling") {
				block.outcome = "not continued in fork"
				block.rows = nil
			}
		}
	case sessionstore.ItemInput:
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputExternal {
			return
		}
		var prompt string
		if err := json.Unmarshal(input.Payload, &prompt); err != nil {
			model.message = "Stored prompt could not be decoded: " + err.Error()
			return
		}
		if model.pending != nil && model.pending.id == input.ID {
			model.pending = nil
			model.draft.Clear()
			model.message = ""
		}
		model.appendText("user", displayAttachmentPrompt(prompt))
	case sessionstore.ItemModelResponse:
		response, ok := item.Data.(sessionstore.ModelResponse)
		if !ok {
			return
		}
		for _, output := range response.Response.Output {
			switch output.Type {
			case llm.ItemMessage:
				if message, ok := output.Data.(llm.Message); ok && message.Role == llm.RoleAssistant {
					model.appendText("assistant", message.Text)
				}
			case llm.ItemToolCall:
				if call, ok := output.Data.(llm.ToolCall); ok {
					model.transcript = append(model.transcript, transcriptBlock{kind: "tool", key: fmt.Sprint(response.TurnID) + "/" + call.CallID, text: briefToolCall(call), arguments: sanitizeTerminal(call.Arguments), outcome: "running", expanded: model.expandTools})
				}
			}
		}
	case sessionstore.ItemToolCallStatus:
		status, ok := item.Data.(sessionstore.ToolCallStatus)
		if !ok {
			return
		}
		model.observeTaskStatus(status, item.RecordedAt)
		key := fmt.Sprint(status.TurnID) + "/" + status.CallID
		index := -1
		for i := len(model.transcript) - 1; i >= 0; i-- {
			if model.transcript[i].key == key {
				index = i
				break
			}
		}
		if index < 0 {
			model.transcript = append(model.transcript, transcriptBlock{kind: "tool", key: key, text: "Tool", expanded: model.expandTools})
			index = len(model.transcript) - 1
		}
		block := &model.transcript[index]
		var details, outcomes []string
		if status.Status.Error != "" {
			details = append(details, sanitizeTerminal(status.Status.Error))
			outcomes = append(outcomes, "failed")
		}
		for _, current := range status.Operations {
			switch current.Status {
			case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
				model.runningTools[current.ID] = true
				outcomes = append(outcomes, string(current.Status))
			case operation.StatusCompleted, operation.StatusFailed, operation.StatusCanceled:
				delete(model.runningTools, current.ID)
				rendered := renderToolOperation(current)
				details = append(details, strings.TrimPrefix(rendered, "tool < "))
				outcome := string(current.Status)
				if current.Type == operation.TypeShell {
					if shell, err := operation.DecodeShellState(current); err == nil && shell.Result != nil {
						outcome = fmt.Sprintf("exit %d", shell.Result.ExitCode)
					}
				}
				outcomes = append(outcomes, outcome)
			}
		}
		if len(details) > 0 {
			block.detail = strings.Join(details, "\n\n")
		}
		if len(outcomes) > 0 {
			block.outcome = strings.Join(outcomes, " · ")
		}
		block.rows = nil
	}
}

func (model *uiModel) blockRows(block *transcriptBlock, width int) []string {
	theme := model.config.Current().Appearance.Theme
	if block.rows != nil && block.width == width && block.theme == theme && block.noColor == model.noColor {
		return block.rows
	}
	palette := model.palette()
	var rows []string
	switch block.kind {
	case "tool":
		marker := "▸"
		if block.expanded {
			marker = "▾"
		}
		outcome := "  " + block.outcome
		label := strings.NewReplacer("\n", " ↵ ", "\t", " ").Replace(block.text)
		title := marker + " " + ansi.Truncate(label, max(1, width-ansi.StringWidth(outcome)-4), "…")
		rows = append(rows, model.ink(title, palette.muted, false)+model.ink(outcome, palette.accent, false))
		if block.expanded {
			for _, line := range strings.Split(ansi.Hardwrap(strings.TrimSpace(block.arguments+"\n"+block.detail), max(1, width-4), true), "\n") {
				rows = append(rows, model.ink("  │ ", palette.rule, false)+line)
			}
		} else if block.detail != "" {
			count := strings.Count(block.detail, "\n") + 1
			rows = append(rows, model.ink(fmt.Sprintf("  └ %d lines · click or ctrl+t to expand", count), palette.muted, false))
		}
	case "assistant":
		rows = append(rows, model.ink("◆ Unreal", palette.lilac, true))
		rows = append(rows, strings.Split(ansi.Hardwrap(renderCompleted(block.text, max(1, width-2), theme, model.noColor), max(1, width-2), true), "\n")...)
	case "user":
		rows = append(rows, model.ink("You", palette.accent, true))
		for _, line := range strings.Split(ansi.Hardwrap(block.text, max(1, width-2), true), "\n") {
			rows = append(rows, model.styleImageChips(line))
		}
	default:
		rows = append(rows, model.ink(block.kind, palette.muted, true))
		rows = append(rows, strings.Split(ansi.Hardwrap(block.text, max(1, width-2), true), "\n")...)
	}
	for i := range rows {
		rows[i] = ansi.Truncate(" "+rows[i], width, "")
	}
	block.rows, block.width, block.theme, block.noColor = rows, width, theme, model.noColor
	return rows
}

func (model *uiModel) transcriptView(width, height int) []string {
	model.transcriptHeight = height
	var rows []transcriptRow
	for i := 0; i < len(model.transcript); i++ {
		if model.transcript[i].hidden {
			count := 1
			for i+1 < len(model.transcript) && model.transcript[i+1].hidden {
				count++
				i++
			}
			noun := "item"
			if count != 1 {
				noun = "items"
			}
			rows = append(rows, transcriptRow{model.ink(fmt.Sprintf(" %d %s hidden", count, noun), model.palette().muted, false), -1}, transcriptRow{"", -1})
			continue
		}
		for _, line := range model.blockRows(&model.transcript[i], width) {
			rows = append(rows, transcriptRow{line, i})
		}
		rows = append(rows, transcriptRow{"", -1})
	}
	model.transcriptRows = rows
	last := max(0, len(rows)-height)
	if model.followTranscript {
		model.transcriptTop = last
	} else {
		model.transcriptTop = min(model.transcriptTop, last)
	}
	result := make([]string, height)
	for row := range result {
		if index := model.transcriptTop + row; index < len(rows) {
			result[row] = rows[index].text
		}
	}
	if len(rows) == 0 && height >= 4 {
		palette := model.palette()
		result[height-4] = model.ink(" ◆ Unreal", palette.lilac, true)
		result[height-3] = model.ink(" Ask, build, explore.", palette.text, false)
		result[height-2] = model.ink(" / commands   ctrl+r history   ctrl+v image", palette.muted, false)
	}
	return result
}

func (model *uiModel) scrollTranscript(delta int) {
	last := max(0, len(model.transcriptRows)-model.transcriptHeight)
	model.transcriptTop = min(last, max(0, model.transcriptTop+delta))
	model.followTranscript = model.transcriptTop == last
}

func (model *uiModel) clickTranscript(x, y int) {
	if y < 0 || y >= model.transcriptHeight || x < 0 {
		return
	}
	if model.sidebarSize() > 0 && x >= model.width-model.sidebarSize() {
		return
	}
	index := model.transcriptTop + y
	if index >= len(model.transcriptRows) {
		return
	}
	blockIndex := model.transcriptRows[index].block
	if blockIndex < 0 {
		return
	}
	block := &model.transcript[blockIndex]
	if block.kind != "tool" {
		return
	}
	block.expanded = !block.expanded
	block.rows = nil
	model.followTranscript = false
}

func (model *uiModel) toggleTools() {
	anchor, screenRow := -1, 0
	for row := 0; row < model.transcriptHeight; row++ {
		index := model.transcriptTop + row
		if index >= len(model.transcriptRows) {
			break
		}
		block := model.transcriptRows[index].block
		if block >= 0 && model.transcript[block].kind == "tool" {
			anchor, screenRow = block, row
			break
		}
	}
	count := 0
	visibleAnchor := anchor >= 0
	for i, block := range model.transcript {
		if block.kind != "tool" {
			continue
		}
		count++
		if !visibleAnchor {
			anchor = i
		}
	}
	if count == 0 {
		model.message = "No tool calls to expand yet."
		return
	}
	model.expandTools = !model.transcript[anchor].expanded
	if model.expandTools {
		screenRow = 0
	}
	for i := range model.transcript {
		if model.transcript[i].kind == "tool" {
			model.transcript[i].expanded = model.expandTools
			model.transcript[i].rows = nil
		}
	}
	model.followTranscript = false
	model.transcriptView(model.width-model.sidebarSize(), model.transcriptHeight)
	for index, row := range model.transcriptRows {
		if row.block == anchor {
			model.transcriptTop = max(0, index-screenRow)
			break
		}
	}
}
