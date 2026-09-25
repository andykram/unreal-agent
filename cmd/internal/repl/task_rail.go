package repl

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

const taskFrame = 90 * time.Millisecond
const taskHold = 15 * time.Second
const taskFadeFrames = 4

type taskTickMsg uint64

type taskTransition struct {
	fromChecked bool
	started     time.Time
}

func (model *uiModel) observeTaskStatus(status sessionstore.ToolCallStatus, now time.Time) {
	for _, current := range status.Operations {
		list, written, err := completedTaskWrite(current)
		if err != nil {
			model.message = "Task status: " + err.Error()
			continue
		}
		if written && current.ID != model.lastTaskWriteID {
			model.lastTaskWriteID = current.ID
			if model.replaying {
				model.restoreTaskList(list, now)
			} else {
				model.updateTaskList(list, now)
			}
		}
	}
}

// Restore the completed hold from the stored status timestamp without replaying motion.
func (model *uiModel) restoreTaskList(list taskList, recordedAt time.Time) {
	model.tasks = list
	model.taskCompletedAt = time.Time{}
	model.taskFade = taskFadeFrames
	if len(list.Tasks) == 0 || taskListActive(list) || recordedAt.IsZero() {
		return
	}
	elapsed := time.Since(recordedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < taskHold+taskFrame*taskFadeFrames {
		model.taskCompletedAt = recordedAt
		model.taskFade = 0
		if elapsed >= taskHold {
			model.taskFade = min(taskFadeFrames-1, int((elapsed-taskHold)/taskFrame)+1)
		}
	}
}

func (model *uiModel) updateTaskList(list taskList, now time.Time) {
	previous := map[string]bool{}
	for _, item := range model.tasks.Tasks {
		previous[item.ID] = item.Status == "completed"
	}
	model.taskTransitions = map[string]taskTransition{}
	for _, item := range list.Tasks {
		if checked, exists := previous[item.ID]; exists && checked != (item.Status == "completed") {
			model.taskTransitions[item.ID] = taskTransition{fromChecked: checked, started: now}
		}
	}
	model.tasks = list
	model.taskGeneration++
	model.taskFade = 0
	model.taskCompletedAt = time.Time{}
	if len(list.Tasks) > 0 && !taskListActive(list) {
		model.taskCompletedAt = now
	}
}

func (model *uiModel) taskVisible() bool {
	return taskListActive(model.tasks) || (!model.taskCompletedAt.IsZero() && model.taskFade < taskFadeFrames)
}

func (model *uiModel) nextTaskTick() tea.Cmd {
	if !model.taskVisible() {
		return nil
	}
	var delay time.Duration
	switch {
	case len(model.taskTransitions) > 0:
		delay = taskFrame
	case model.taskCompletedAt.IsZero():
		return nil
	case time.Now().Before(model.taskCompletedAt.Add(taskHold)):
		delay = time.Until(model.taskCompletedAt.Add(taskHold))
	default:
		delay = taskFrame
	}
	generation := model.taskGeneration
	return tea.Tick(delay, func(time.Time) tea.Msg { return taskTickMsg(generation) })
}

func (model *uiModel) advanceTaskRail(now time.Time, generation uint64) tea.Cmd {
	if generation != model.taskGeneration {
		return nil
	}
	for id, motion := range model.taskTransitions {
		if now.Sub(motion.started) >= taskFrame*taskFadeFrames {
			delete(model.taskTransitions, id)
		}
	}
	if !model.taskCompletedAt.IsZero() && !now.Before(model.taskCompletedAt.Add(taskHold)) {
		model.taskFade++
	}
	return model.nextTaskTick()
}

func (model *uiModel) taskRailLines(width, budget int, now time.Time) []string {
	if budget <= 0 || !model.taskVisible() {
		return nil
	}
	done := 0
	for _, item := range model.tasks.Tasks {
		if item.Status == "completed" {
			done++
		}
	}
	title := fmt.Sprintf("◇  TASKS   %d/%d", done, len(model.tasks.Tasks))
	result := []string{model.ink(ansi.Truncate(title, width, "…"), model.palette().lilac, true)}
	if budget == 1 {
		return result
	}
	visible := min(len(model.tasks.Tasks), budget-1)
	if visible < len(model.tasks.Tasks) && budget > 2 {
		visible--
	}
	for _, item := range model.tasks.Tasks[:visible] {
		marker := "○"
		shade := model.palette().muted
		switch item.Status {
		case "completed":
			marker, shade = "✓", model.palette().lilac
		case "in_progress":
			marker, shade = "◉", model.palette().accent
		case "blocked":
			marker = "!"
		case "canceled":
			marker = "−"
		}
		if motion, ok := model.taskTransitions[item.ID]; ok {
			frame := min(taskFadeFrames-1, max(0, int(now.Sub(motion.started)/taskFrame)))
			if motion.fromChecked {
				marker = []string{"✓", "◉", "◔", "○"}[frame]
			} else {
				marker = []string{"○", "◔", "◉", "✓"}[frame]
			}
		}
		label := marker + " " + sanitizeTerminal(item.Title)
		result = append(result, model.ink(ansi.Truncate(strings.ReplaceAll(label, "\n", " ↵ "), width, "…"), shade, false))
	}
	if hidden := len(model.tasks.Tasks) - visible; hidden > 0 && budget > 2 {
		result = append(result, model.ink(fmt.Sprintf("  +%d more", hidden), model.palette().muted, false))
	}
	if model.taskFade > 0 {
		// A terminal has no alpha. Step through the existing palette before hiding.
		shade := []string{model.palette().muted, model.palette().rule, model.palette().chip}[min(2, model.taskFade-1)]
		for i, line := range result {
			result[i] = model.ink(ansi.Strip(line), shade, false)
		}
	}
	return result
}

func (model *uiModel) queuedLines(queued []string, width, budget int) []string {
	if len(queued) == 0 || budget <= 0 {
		return nil
	}
	result := []string{model.ink(ansi.Truncate(fmt.Sprintf("⌁  QUEUED   %d", len(queued)), width, "…"), model.palette().muted, true)}
	visible := min(len(queued), budget-1)
	if visible < len(queued) && budget > 2 {
		visible--
	}
	for _, text := range queued[:visible] {
		preview := strings.ReplaceAll(sanitizeTerminal(displayAttachmentPrompt(text)), "\n", " ↵ ")
		result = append(result, model.ink(ansi.Truncate("  ↳ "+preview, width, "…"), model.palette().muted, false))
	}
	if hidden := len(queued) - visible; hidden > 0 && budget > 2 {
		result = append(result, model.ink(fmt.Sprintf("    +%d more queued", hidden), model.palette().muted, false))
	}
	return result
}
