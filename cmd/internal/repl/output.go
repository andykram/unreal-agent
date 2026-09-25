package repl

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

const terminalToolOutputRunes = 8000

func briefToolCall(call llm.ToolCall) string {
	label := sanitizeTerminal(call.Name)
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return label
	}
	for _, field := range []string{"command", "path", "title", "name", "query"} {
		value, okay := arguments[field].(string)
		if !okay || value == "" {
			continue
		}
		value = sanitizeTerminal(value)
		if utf8.RuneCountInString(value) > 120 {
			value = string([]rune(value)[:120]) + "…"
		}
		return label + ": " + value
	}
	return label
}

func renderToolOperation(current operation.Operation) string {
	switch current.Type {
	case operation.TypeShell:
		state, err := operation.DecodeShellState(current)
		if err != nil {
			return "tool < Bash: " + sanitizeTerminal(err.Error())
		}
		if current.Status != operation.StatusCompleted || state.Result == nil {
			message := state.TerminalError
			if message == "" {
				message = string(current.Status)
			}
			return "tool < Bash: " + sanitizeTerminal(message)
		}
		var blocks []string
		blocks = append(blocks, fmt.Sprintf("tool < Bash exit %d", state.Result.ExitCode))
		if state.Result.Out != "" {
			blocks = append(blocks, boundedTerminalOutput("stdout", state.Result.Out, state.OutPath, state.OutTruncated))
		}
		if state.Result.Err != "" {
			blocks = append(blocks, boundedTerminalOutput("stderr", state.Result.Err, state.ErrPath, state.ErrTruncated))
		}
		return strings.Join(blocks, "\n")
	case operation.TypeViewImage:
		state, err := operation.DecodeViewImageState(current)
		if err != nil {
			return "tool < ViewImage: " + sanitizeTerminal(err.Error())
		}
		if state.Result != nil && state.Result.Error == "" && current.Status == operation.StatusCompleted {
			return fmt.Sprintf("tool < ViewImage: %d x %d %s", state.Result.OriginalWidth, state.Result.OriginalHeight, sanitizeTerminal(state.Result.OriginalMIMEType))
		}
		if state.Result != nil && state.Result.Error != "" {
			return "tool < ViewImage: " + sanitizeTerminal(state.Result.Error)
		}
		return "tool < ViewImage: " + string(current.Status)
	case operation.TypeSkillUse:
		state, err := operation.DecodeSkillUse(current)
		if err == nil && current.Status == operation.StatusCompleted {
			return "tool < " + boundedTerminalOutput("Skill instructions", string(state.Content), state.Path, false)
		}
		if err == nil && state.TerminalError != "" {
			return "tool < SkillUse: " + sanitizeTerminal(state.TerminalError)
		}
	case operation.TypeValue:
		value, err := operation.DecodeValue(current)
		if err == nil && current.Status == operation.StatusCompleted {
			return "tool < " + boundedTerminalOutput("Result", string(value), "", false)
		}
	case operation.TypeRemoteJob:
		state, err := operation.DecodeRemoteJobState(current)
		if err != nil {
			return "tool < question: " + sanitizeTerminal(err.Error())
		}
		if state.Plan.Type == taskPlanType && current.Status == operation.StatusCompleted {
			var list taskList
			if err := json.Unmarshal([]byte(state.TerminalResult), &list); err == nil {
				completed := 0
				var lines []string
				for _, task := range list.Tasks {
					marker := "○"
					switch task.Status {
					case "completed":
						marker = "✓"
						completed++
					case "in_progress":
						marker = "◉"
					case "blocked":
						marker = "!"
					case "canceled":
						marker = "−"
					}
					line := marker + " " + sanitizeTerminal(task.Title) + " · " + task.Status
					if task.Note != "" {
						line += "\n  " + sanitizeTerminal(task.Note)
					}
					lines = append(lines, line)
				}
				return fmt.Sprintf("tool < Tasks · %d/%d complete\n", completed, len(list.Tasks)) + strings.Join(lines, "\n")
			}
		}
		if state.Plan.Type == askUserPlanType {
			if current.Status == operation.StatusCompleted {
				return "tool < question answered"
			}
			if current.Status == operation.StatusCanceled {
				return "tool < question dismissed"
			}
			if state.TerminalError != "" {
				return "tool < question error: " + sanitizeTerminal(state.TerminalError)
			}
		}
	}
	return "tool < " + sanitizeTerminal(string(current.Type)) + ": " + string(current.Status)
}

func boundedTerminalOutput(label, raw, path string, alreadyTruncated bool) string {
	clean := sanitizeTerminal(raw)
	runes := []rune(clean)
	shortened := len(runes) > terminalToolOutputRunes
	if shortened {
		clean = string(runes[:terminalToolOutputRunes])
	}
	result := label + ":\n" + clean
	if shortened || alreadyTruncated {
		result += "\n[Output shortened for terminal]"
		if path != "" {
			result += " Complete output: " + sanitizeTerminal(path)
		}
	}
	return result
}
