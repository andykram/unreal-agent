package repl

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type runtimeEventsMsg struct {
	runtime *Runtime
	events  []RuntimeEvent
}

type activityTickMsg uint64
type contextCatalogMsg CatalogResult

var activityFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type pendingUIInput struct {
	id   inbox.ID
	text string
}

type projectionCache struct {
	version       uint64
	cursor        int
	width         int
	markdown      bool
	selection     editor.SourceRange
	hasSelection  bool
	value         editor.Projection
	valid         bool
	parsedVersion uint64
	noColor       bool
	spans         []editor.SyntaxSpan
	slashSpans    []editor.SyntaxSpan
}

type uiModel struct {
	state                   *SessionState
	config                  *ConfigStore
	router                  *ModelRouter
	catalog                 *ModelCatalog
	runtime                 *Runtime
	popup                   *modelPopup
	resumePopup             *resumePopup
	rename                  *renameSessionForm
	sidebarRows             map[int]string
	sidebarWidth            int
	sidebarDragging         bool
	sessionMenu             *sessionContextMenu
	approvalTop             int
	plan                    *planReview
	setupError              error
	ctx                     context.Context
	getenv                  func(string) string
	heartbeat               time.Duration
	draft                   editor.Buffer
	projection              projectionCache
	pending                 *pendingUIInput
	width                   int
	height                  int
	busy                    bool
	quitArmed               bool
	busySince               time.Time
	activityFrame           int
	activityGeneration      uint64
	message                 string
	noColor                 bool
	history                 *HistoryStore
	historyEntries          []HistoryEntry
	historyIndex            int
	historyDraft            string
	historyCursor           int
	historyDraftAttachments []Attachment
	question                *questionForm
	showQuestion            bool
	dismissQuestion         bool
	settings                *settingsForm
	contextReport           *contextReport
	contextSnapshot         contextReport
	forkChoices             []SessionChoice
	attachments             []Attachment
	clipboard               clipboardReader
	editorPath              string
	powerBar                *powerBar
	skills                  SkillCatalog
	completion              *commandPopup
	historySearch           *historySearchPopup
	suppressCompletion      bool
	lastDisplayed           sessionstore.Sequence
	transcript              []transcriptBlock
	transcriptTop           int
	transcriptHeight        int
	transcriptRows          []transcriptRow
	followTranscript        bool
	expandTools             bool
	runningTools            map[operation.ID]bool
	tasks                   taskList
	lastTaskWriteID         operation.ID
	replaying               bool
	taskTransitions         map[string]taskTransition
	taskGeneration          uint64
	taskCompletedAt         time.Time
	taskFade                int
}

var _ tea.Model = (*uiModel)(nil)

func newUI(ctx context.Context, getenv func(string) string, heartbeat time.Duration, state *SessionState, config *ConfigStore, router *ModelRouter, catalog *ModelCatalog, runtime *Runtime, setupError error) *uiModel {
	model := &uiModel{ctx: ctx, getenv: getenv, heartbeat: heartbeat, state: state, config: config, router: router, catalog: catalog, runtime: runtime, setupError: setupError, width: 80, height: 24, followTranscript: true, noColor: getenv("NO_COLOR") != "", history: NewHistoryStore(state), historyIndex: -1, clipboard: nativeClipboard{}, runningTools: map[operation.ID]bool{}}
	if setupError != nil {
		model.message = setupError.Error()
	}
	if err := model.loadTranscript(); err != nil {
		model.message = err.Error()
	}
	model.refreshContextSnapshot()
	model.refreshForkChoices()
	if skills, err := DiscoverCLISkills(state.Workspace, projectBoundary(state.Workspace), getenv); err == nil {
		model.skills = skills
	} else {
		model.message = err.Error()
	}
	return model
}

func (model *uiModel) refreshContextSnapshot() {
	if report, err := buildContextReport(model.ctx, model); err == nil {
		model.contextSnapshot = report
	}
}

func (model *uiModel) refreshForkChoices() {
	if model.state.Current.SessionID == "" {
		model.forkChoices = nil
		return
	}
	if choices, err := model.state.Family(model.ctx); err == nil {
		model.forkChoices = choices
	}
}

func (model *uiModel) Init() tea.Cmd {
	var main tea.Cmd
	if model.runtime != nil {
		main = model.waitRuntime()
	}
	return tea.Batch(main, model.selectedContextCatalogCmd(), model.nextTaskTick())
}

func (model *uiModel) selectedContextCatalogCmd() tea.Cmd {
	if model.catalog == nil || model.config.Current().Model.ContextWindowTokens > 0 {
		return nil
	}
	ref := ModelRef{Provider: model.config.Current().Model.Provider}
	if model.router != nil {
		ref = model.router.Selected()
	}
	if ref.Provider != "openrouter" && ref.Provider != "openai-codex" && ref.Provider != "ollama-cloud" {
		return nil
	}
	generation := model.catalog.BeginRefresh(ref.Provider)
	return func() tea.Msg { return contextCatalogMsg(model.catalog.Discover(model.ctx, ref.Provider, generation)) }
}

func (model *uiModel) loadTranscript() error {
	model.replaying = true
	defer func() { model.replaying = false }()
	model.lastDisplayed = sessionstore.BeforeFirst
	model.runningTools = map[operation.ID]bool{}
	model.transcript = nil
	model.transcriptTop = 0
	model.followTranscript = true
	model.tasks = taskList{}
	model.lastTaskWriteID = ""
	model.taskTransitions = nil
	model.taskCompletedAt = time.Time{}
	model.taskFade = taskFadeFrames
	model.taskGeneration++
	if model.state.Current.SessionID == "" {
		return nil
	}
	for {
		page, err := model.state.Store.Items(model.ctx, session.ID(model.state.Current.SessionID), model.lastDisplayed, 256)
		if err != nil {
			return fmt.Errorf("replay session transcript: %w", err)
		}
		for _, item := range page.Items {
			model.appendItem(item)
			model.lastDisplayed = item.Sequence
		}
		if !page.More {
			break
		}
	}
	return nil
}

func (model *uiModel) waitRuntime() tea.Cmd {
	runtime := model.runtime
	return func() tea.Msg {
		select {
		case <-runtime.Events():
			return runtimeEventsMsg{runtime: runtime, events: runtime.DrainEvents()}
		case <-runtime.Done():
			return nil
		}
	}
}

func (model *uiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.MouseWheelMsg:
		model.disarmQuit()
		if model.powerBar != nil || model.sessionMenu != nil || model.rename != nil {
			return model, nil
		}
		model.disarmQuit()
		if model.powerBar == nil && model.rename == nil && model.settings == nil && model.contextReport == nil && !model.showQuestion && model.resumePopup == nil {
			if value.Button == tea.MouseWheelUp {
				model.scrollTranscript(-3)
			}
			if value.Button == tea.MouseWheelDown {
				model.scrollTranscript(3)
			}
		}
		return model, nil
	case tea.MouseMotionMsg:
		if model.sidebarDragging {
			model.sidebarWidth = min(model.width-12, max(8, model.width-value.X))
		}
		return model, nil
	case tea.MouseReleaseMsg:
		model.sidebarDragging = false
		return model, nil
	case tea.MouseClickMsg:
		model.disarmQuit()
		if model.powerBar != nil || model.rename != nil {
			return model, nil
		}
		if command, handled := model.handleSidebarClick(value); handled {
			return model, command
		}
		if model.sessionMenu != nil {
			return model.clickSessionMenu(value)
		}
		model.disarmQuit()
		if model.powerBar != nil {
			return model, nil
		}
		if model.powerBar == nil && model.rename == nil && model.settings == nil && model.contextReport == nil && !model.showQuestion && model.resumePopup == nil && value.Button == tea.MouseLeft {
			model.clickTranscript(value.X, value.Y)
		}

		return model, nil
	case tea.WindowSizeMsg:
		model.width, model.height = max(1, value.Width), max(1, value.Height)
		if model.settings != nil {
			return model, model.settings.Update(value)
		}
		if model.question != nil && model.showQuestion {
			return model, model.question.Update(value)
		}
	case tea.PasteMsg:
		model.disarmQuit()
		if model.powerBar != nil {
			model.powerBar.query += strings.NewReplacer("\n", " ", "\r", " ").Replace(value.Content)
			model.powerBar.selected = 0
			return model, nil
		}
		if model.sessionMenu != nil {
			return model.updateSessionMenu(value)
		}
		if model.rename != nil {
			return model.updateRenameSession(value)
		}
		if model.historySearch != nil {
			model.historySearch.appendQuery(value.Content)
			return model, nil
		}
		if model.contextReport != nil {
			return model, nil
		}
		if model.settings != nil {
			return model.updateSettings(value)
		}
		if model.question != nil && model.showQuestion {
			return model.updateQuestion(value)
		}
		model.historyIndex = -1
		if err := model.draft.Insert(value.Content); err != nil {
			model.message = err.Error()
		}
		model.suppressCompletion = false
		model.refreshCompletion()
	case clipboardPayload:
		if value.sessionID != model.state.Current.SessionID {
			return model, nil
		}
		if value.err != nil {
			model.message = value.err.Error()
		} else if value.image != nil {
			attachment, err := saveAttachment(model.state, value.image, true)
			if err != nil {
				model.message = err.Error()
			} else {
				model.addAttachment(attachment)
			}
		} else if value.text != "" {
			if err := model.draft.Insert(value.text); err != nil {
				model.message = err.Error()
			}
			model.suppressCompletion = false
			model.refreshCompletion()
		} else {
			model.message = "Clipboard is empty."
		}
	case editorFinishedMsg:
		model.finishExternalEditor(value)
	case noticeExpiredMsg:
		for i := range model.transcript {
			block := &model.transcript[i]
			if !block.expires.IsZero() && !block.expires.After(time.Time(value)) {
				block.hidden = true
			}
		}
	case taskTickMsg:
		return model, model.advanceTaskRail(time.Now(), uint64(value))
	case activityTickMsg:
		if !model.busy || uint64(value) != model.activityGeneration {
			return model, nil
		}
		model.activityFrame = (model.activityFrame + 1) % len(activityFrames)
		return model, model.nextActivityTick()
	case tea.KeyPressMsg:
		if value.String() == "ctrl+c" {
			return model.interrupt()
		}
		model.disarmQuit()
		if value.String() == "ctrl+d" {
			return model, tea.Quit
		}
		if value.String() == "ctrl+k" || value.String() == "super+k" || value.String() == "meta+k" {
			return model.openPowerBar()
		}
		if model.powerBar != nil {
			return model.updatePowerBar(value)
		}
		if model.sessionMenu != nil {
			return model.updateSessionMenu(value)
		}
		if model.rename != nil {
			return model.updateRenameSession(value)
		}
		if model.runtime != nil && model.runtime.options.Approvals != nil {
			gate := model.runtime.options.Approvals
			if request := gate.Pending(); request != nil {
				switch value.String() {
				case "y", "Y", "shift+y":
					gate.Decide(request, true)
					model.approvalTop = 0
				case "n", "N", "shift+n", "esc":
					gate.Decide(request, false)
					model.approvalTop = 0
				case "down", "j", "pgdown":
					model.approvalTop += max(1, model.height-6)
				case "up", "k", "pgup":
					model.approvalTop = max(0, model.approvalTop-max(1, model.height-6))
				}
				return model, nil
			}
		}
		if value.String() == "ctrl+d" {
			return model, tea.Quit
		}

		if model.plan != nil {
			return model.updatePlan(value)
		}
		if value.String() == "ctrl+d" {
			return model, tea.Quit
		}

		if model.historySearch != nil {
			return model.updateHistorySearch(value)
		}
		if model.contextReport != nil {
			if value.String() == "esc" || value.String() == "enter" || value.String() == "q" {
				model.contextReport = nil
				return model, nil
			}
			return model, nil
		}
		if model.settings != nil {
			return model.updateSettings(value)
		}
		if model.question != nil && model.showQuestion {
			return model.updateQuestion(value)
		}
		if value.String() == "ctrl+r" && model.state.Current.SessionID != "" {
			model.popup = nil
			model.resumePopup = nil
			return model.openHistorySearch()
		}

		if model.resumePopup != nil {
			return model.updateResumePopup(value)
		}
		if model.popup != nil {
			return model.updateModelPopup(value)
		}
		return model.updateKey(value)
	case catalogMsg:
		result := CatalogResult(value)
		if result.Err != nil {
			model.message = result.Err.Error()
		} else if model.catalog.Apply(result) && model.popup != nil {
			model.popup.choices = model.catalog.Choices()
			model.message = "Catalog refreshed."
		}
	case contextCatalogMsg:
		result := CatalogResult(value)
		if result.Err == nil && model.catalog.Apply(result) {
			model.refreshContextSnapshot()
			if model.contextReport != nil {
				model.contextReport.capacity, model.contextReport.capacitySource = model.contextCapacity()
			}
		}
	case runtimeEventsMsg:
		if value.runtime != model.runtime {
			return model, nil
		}
		var activity tea.Cmd
		priorTaskGeneration := model.taskGeneration
		refreshContext := false
		for _, event := range value.events {
			switch event.Kind {
			case EventQuestion:
				// The form is selected after all events in this batch are applied.
			case EventTaskStarted:
				if !model.busy {
					model.busySince = time.Now()
					model.activityFrame = 0
					model.activityGeneration++
					activity = model.nextActivityTick()
				}
				model.busy = true
			case EventTaskIdle:
				if model.config.Current().Mode == "plan" {
					model.openPlan()
				}
				model.busy = false
				model.activityGeneration++
			case EventTaskError:
				model.busy = false
				model.activityGeneration++
				model.message = fmt.Sprintf("task failed: %v", event.Err)
			case EventItem:
				if event.Item.Kind == sessionstore.ItemModelResponse {
					refreshContext = true
				}
				if event.Item.Sequence <= model.lastDisplayed {
					continue
				}
				model.lastDisplayed = event.Item.Sequence
				model.appendItem(event.Item)
			}
		}
		if refreshContext {
			model.refreshContextSnapshot()
		}
		if model.router != nil {
			if warning := model.router.TakeProvenanceWarning(); warning != nil {
				model.message = warning.Error()
			}
		}
		form := model.syncQuestion()
		var taskTick tea.Cmd
		if model.taskGeneration != priorTaskGeneration {
			taskTick = model.nextTaskTick()
		}
		return model, tea.Batch(form, activity, taskTick, model.waitRuntime())
	}
	if model.rename != nil {
		return model.updateRenameSession(message)
	}
	if model.settings != nil {
		return model.updateSettings(message)
	}
	if model.question != nil && model.showQuestion {
		return model.updateQuestion(message)
	}
	return model, nil
}

func (model *uiModel) updateSettings(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && key.String() == "esc" {
		model.settings = nil
		model.message = "Settings canceled."
		return model, nil
	}
	command := model.settings.Update(message)
	if model.settings.form.State != huh.StateCompleted {
		return model, command
	}
	current := model.settings
	candidate, changes, err := current.candidate()
	if err == nil {
		if _, changingMode := changes["mode"]; changingMode && model.runtime != nil && model.runtime.IsBusy() {
			err = fmt.Errorf("stop active work before changing harness mode")
		}
	}
	if err == nil {
		if model.router != nil {
			err = model.router.ApplySettings(changes, candidate)
		} else if modelSettingsChanged(changes, candidate.Model.Provider) {
			var router *ModelRouter
			router, err = NewModelRouterSettings(model.config, model.getenv, changes, candidate)
			if err == nil {
				var runtime *Runtime
				runtime, err = newAppRuntime(model.ctx, model.state, router, model.getenv, model.heartbeat)
				if err == nil {
					model.router, model.runtime, model.setupError = router, runtime, nil
					command = tea.Batch(command, model.waitRuntime())
				} else {
					_ = router.Close()
				}
			}
		} else {
			err = model.config.SaveSettings(changes)
		}
	}
	if err != nil {
		model.settings = newSettingsFormWithValues(model.config, model.width, model.height, &current.values)
		model.message = err.Error()
		return model, model.settings.form.Init()
	}
	model.settings = nil
	model.message = "Settings saved to " + model.config.Path()
	model.refreshContextSnapshot()
	return model, tea.Batch(command, model.selectedContextCatalogCmd())
}

func (model *uiModel) syncQuestion() tea.Cmd {
	if model.runtime == nil {
		model.question = nil
		return nil
	}
	questions := model.runtime.Questions()
	if len(questions) == 0 {
		model.question = nil
		model.showQuestion = false
		model.dismissQuestion = false
		return nil
	}
	if model.question == nil || model.question.id != questions[0].OperationID {
		model.question = newQuestionForm(questions[0], model.width, model.height)
		model.showQuestion = true
		model.dismissQuestion = false
		return model.question.Init()
	}
	return nil
}

func (model *uiModel) updateQuestion(message tea.Msg) (tea.Model, tea.Cmd) {
	if model.dismissQuestion {
		if key, ok := message.(tea.KeyPressMsg); ok {
			switch key.String() {
			case "esc", "r", "R":
				model.dismissQuestion = false
			case "d", "D":
				if err := model.runtime.AnswerQuestion(model.question.id, AskUserResult{Status: "dismissed"}); err != nil {
					model.message = err.Error()
				} else {
					model.question.submitted = true
					model.dismissQuestion = false
					model.message = "Dismissing question; waiting for the stored tool result."
				}
			}
		}
		return model, nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			model.dismissQuestion = true
			return model, nil
		case "ctrl+t":
			model.showQuestion = false
			model.message = "Question pending. Press Ctrl+Q to return."
			return model, nil
		}
	}
	command := model.question.Update(message)
	if model.question.form.State == huh.StateCompleted && !model.question.submitted {
		if err := model.runtime.AnswerQuestion(model.question.id, model.question.Result()); err != nil {
			model.message = err.Error()
		} else {
			model.question.submitted = true
			model.message = "Submitting answers; waiting for the stored tool result."
		}
	}
	return model, command
}

func (model *uiModel) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.completion != nil {
		switch key.String() {
		case "esc":
			model.completion = nil
			model.suppressCompletion = true
			return model, nil
		case "up":
			if model.completion.selected > 0 {
				model.completion.selected--
			}
			return model, nil
		case "down":
			if model.completion.selected+1 < len(model.completion.choices) {
				model.completion.selected++
			}
			return model, nil
		case "enter", "tab":
			model.acceptCompletion()
			return model, nil
		}
	}
	defer model.refreshCompletion()
	if handleReadline(&model.draft, key) {
		model.historyIndex = -1
		return model, nil
	}
	switch key.String() {
	case "pgup":
		model.scrollTranscript(-max(1, model.transcriptHeight-2))
		return model, nil
	case "pgdown":
		model.scrollTranscript(max(1, model.transcriptHeight-2))
		return model, nil
	case "ctrl+home":
		model.followTranscript, model.transcriptTop = false, 0
		return model, nil
	case "ctrl+end":
		model.followTranscript = true
		return model, nil
	case "ctrl+t":
		model.toggleTools()
		return model, nil
	case "ctrl+g":
		return model, model.openExternalEditor()
	case "ctrl+v":
		if model.state.Current.SessionID == "" {
			model.message = "Choose a session before pasting."
			return model, nil
		}
		id := session.ID(model.state.Current.SessionID)
		reader := model.clipboard
		return model, func() tea.Msg { return readClipboard(model.ctx, reader, id) }
	case "ctrl+x":
		model.removeLastAttachment()
		return model, nil
	case "ctrl+q":
		if model.question != nil {
			model.showQuestion = true
			model.message = ""
		}
		return model, nil
	case "enter":
		return model.submit(false)
	case "alt+enter":
		return model.submit(true)
	case "shift+enter", "ctrl+j":
		model.suppressCompletion = false
		model.historyIndex = -1
		model.message = ""
		if err := model.draft.Insert("\n"); err != nil {
			model.message = err.Error()
		}
	case "backspace", "ctrl+h":
		model.suppressCompletion = false
		model.historyIndex = -1
		model.draft.Backspace()
	case "delete":
		model.suppressCompletion = false
		model.historyIndex = -1
		model.draft.Delete()
	case "left", "ctrl+b":
		model.draft.ClearSelection()
		model.draft.Left()
	case "right", "ctrl+f":
		model.draft.ClearSelection()
		model.draft.Right()
	case "shift+left":
		model.draft.Extend(model.draft.Left)
	case "shift+right":
		model.draft.Extend(model.draft.Right)
	case "shift+up":
		model.draft.Extend(func() { model.draft.Up() })
	case "shift+down":
		model.draft.Extend(func() { model.draft.Down() })
	case "ctrl+shift+a":
		model.draft.SelectAll()
	case "home", "ctrl+a":
		model.draft.ClearSelection()
		model.draft.Home()
	case "shift+home":
		model.draft.Extend(model.draft.Home)
	case "end", "ctrl+e":
		model.draft.ClearSelection()
		model.draft.End()
	case "shift+end":
		model.draft.Extend(model.draft.End)
	case "up":
		model.draft.ClearSelection()
		if model.historyIndex >= 0 || model.draft.AtFirstLine() {
			model.browseHistory(-1)
		} else {
			model.draft.Up()
		}
	case "down":
		model.draft.ClearSelection()
		if model.historyIndex >= 0 || model.draft.AtLastLine() {
			model.browseHistory(1)
		} else {
			model.draft.Down()
		}
	case "ctrl+p":
		model.browseHistory(-1)
	case "ctrl+n":
		model.browseHistory(1)
	default:
		if key.Text != "" {
			model.suppressCompletion = false
			model.historyIndex = -1
			model.message = ""
			if err := model.draft.Insert(key.Text); err != nil {
				model.message = err.Error()
			}
		}
	}
	return model, nil
}

func (model *uiModel) disarmQuit() {
	if model.quitArmed {
		model.quitArmed = false
		model.message = ""
	}
}

func (model *uiModel) interrupt() (tea.Model, tea.Cmd) {
	if model.quitArmed {
		return model, tea.Quit
	}
	model.quitArmed = true
	model.message = "Press Ctrl+C again to exit."
	if model.busy && model.runtime != nil {
		if err := model.runtime.Stop(context.Background()); err != nil {
			model.message = err.Error() + " · Press Ctrl+C again to exit."
		} else {
			model.message = "Stopping active work. Press Ctrl+C again to exit."
		}
	} else if model.draft.Source() != "" || len(model.attachments) > 0 {
		model.draft.Clear()
		model.cleanupAttachments()
		model.completion = nil
		model.message = "Draft cleared. Press Ctrl+C again to exit."
	}
	return model, nil
}

func (model *uiModel) submit(steer bool) (tea.Model, tea.Cmd) {
	raw := model.draft.Source()
	if strings.TrimSpace(raw) == "" && len(model.attachments) == 0 {
		return model, nil
	}
	if model.pending != nil {
		model.message = "Waiting for the last prompt to be stored."
		return model, nil
	}
	for _, attachment := range model.attachments {
		if attachment.Missing {
			model.message = "A recalled image file is missing. Remove its chip with /detach before sending."
			return model, nil
		}
	}
	// Built-in commands win collisions; skill aliases enter the normal prompt path.
	if strings.HasPrefix(strings.TrimSpace(raw), "/") && !strings.HasPrefix(strings.TrimSpace(raw), "//") {
		name, argument, _ := splitCommand(strings.TrimSpace(raw))
		builtin := false
		for _, command := range commandRegistry() {
			if command.Name == name {
				builtin = true
				break
			}
		}
		if !builtin {
			if entry, ok := model.skills.UserSkill(strings.TrimPrefix(name, "/")); ok {
				raw = "$" + entry.Skill.Name
				if argument != "" {
					raw += " " + argument
				}
			}
		}
	}
	// Slash skills inside prose use the same explicit invocation as a leading alias.
	raw = inlineSkillAliases(raw, model.skills)
	if len(model.attachments) == 0 && strings.HasPrefix(strings.TrimSpace(raw), "/") && !strings.HasPrefix(strings.TrimSpace(raw), "//") {
		command := strings.TrimSpace(raw)
		next, action := model.command(command)
		if model.draft.Source() == "" || model.popup != nil || model.resumePopup != nil {
			model.historyIndex = -1
			model.cleanupHistoryDraftAttachments()
			if err := model.recordHistory("command", command, "immediate"); err != nil {
				model.message = err.Error()
			}
		}
		return next, action
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "//") {
		raw = strings.Replace(raw, "//", "/", 1)
	}
	if model.runtime == nil {
		model.message = fmt.Sprintf("Model setup required: %v", model.setupError)
		return model, nil
	}
	attachments := append([]Attachment(nil), model.attachments...)
	id, admission, err := model.runtime.Submit(context.Background(), attachmentPrompt(raw, attachments), steer)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.historyIndex = -1
	attachmentIDs := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		attachmentIDs = append(attachmentIDs, attachment.ID)
	}
	historyErr := model.recordHistory("prompt", raw, string(admission), attachmentIDs...)
	model.attachments = nil
	model.cleanupHistoryDraftAttachments()
	if admission == AdmissionQueued {
		model.draft.Clear()
		model.message = "Prompt queued until current work reaches idle."
		if historyErr != nil {
			model.message += " History: " + historyErr.Error()
		}
		return model, nil
	}
	model.pending = &pendingUIInput{id: id, text: raw}
	model.message = "Submitting prompt..."
	if historyErr != nil {
		model.message += " History: " + historyErr.Error()
	}
	return model, nil
}

func (model *uiModel) recordHistory(kind, value, admission string, attachmentIDs ...string) error {
	_, err := model.history.Append(kind, session.ID(model.state.Current.SessionID), value, admission, model.config.Current().History.MaxEntries, attachmentIDs...)
	return err
}

func (model *uiModel) cleanupAttachments() {
	for _, attachment := range model.attachments {
		if !attachment.Submitted && attachment.Path != "" {
			_ = os.Remove(attachment.Path)
		}
	}
	model.attachments = nil
}

func (model *uiModel) cleanupHistoryDraftAttachments() {
	for _, attachment := range model.historyDraftAttachments {
		if !attachment.Submitted && attachment.Path != "" {
			_ = os.Remove(attachment.Path)
		}
	}
	model.historyDraftAttachments = nil
}

func (model *uiModel) browseHistory(direction int) {
	if model.historyIndex < 0 {
		if direction > 0 {
			return
		}
		entries, err := model.history.Entries()
		if err != nil {
			model.message = err.Error()
			return
		}
		if len(entries) == 0 {
			model.message = "No prompt history yet."
			return
		}
		model.historyEntries = entries
		model.historyDraft, model.historyCursor = model.draft.Source(), model.draft.Cursor()
		model.historyDraftAttachments = append([]Attachment(nil), model.attachments...)
		model.historyIndex = len(entries) - 1
	} else {
		next := model.historyIndex + direction
		if next >= len(model.historyEntries) {
			model.historyIndex = -1
			_ = model.draft.Restore(model.historyDraft, model.historyCursor)
			model.attachments = model.historyDraftAttachments
			model.historyDraftAttachments = nil
			return
		}
		if next < 0 {
			return
		}
		model.historyIndex = next
	}
	if err := model.draft.Set(model.historyEntries[model.historyIndex].Text); err != nil {
		model.message = err.Error()
		model.historyIndex = -1
		return
	}
	entry := model.historyEntries[model.historyIndex]
	model.attachments = model.attachmentsForHistory(entry)
	model.ensureImageChips()
	for _, attachment := range model.attachments {
		if attachment.Missing {
			model.message = "History image " + attachment.ID + " is missing; remove its chip with Ctrl+X before sending."
		}
	}
}

func (model *uiModel) attachmentsForHistory(entry HistoryEntry) []Attachment {
	attachments := make([]Attachment, 0, len(entry.AttachmentIDs))
	for _, id := range entry.AttachmentIDs {
		attachments = append(attachments, restoreAttachment(model.state, entry.SessionID, id))
	}
	return attachments
}

func (model *uiModel) chooseModel(provider, id string) (tea.Cmd, error) {
	if model.router != nil {
		if err := model.router.Select(provider, id); err != nil {
			return nil, err
		}
		model.refreshContextSnapshot()
		return model.selectedContextCatalogCmd(), nil
	}
	router, err := NewModelRouterSelection(model.config, model.getenv, provider, id)
	if err != nil {
		return nil, err
	}
	runtime, err := newAppRuntime(model.ctx, model.state, router, model.getenv, model.heartbeat)
	if err != nil {
		_ = router.Close()
		return nil, err
	}
	model.router, model.runtime, model.setupError = router, runtime, nil
	model.refreshContextSnapshot()
	return tea.Batch(model.waitRuntime(), model.selectedContextCatalogCmd()), nil
}

func (model *uiModel) View() tea.View {
	view := model.viewContent()
	if model.quitArmed && (model.powerBar != nil || model.contextReport != nil || model.rename != nil || model.settings != nil || model.showQuestion || model.resumePopup != nil) {
		lines := strings.Split(view.Content, "\n")
		if len(lines) >= model.height {
			lines = lines[:max(0, model.height-1)]
		}
		lines = append(lines, model.ink(ansi.Truncate(model.message, model.width, "…"), model.palette().accent, true))
		view.Content = strings.Join(lines, "\n")
	}
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (model *uiModel) viewContent() tea.View {
	if model.powerBar != nil {
		return model.powerBarView()
	}
	if model.sessionMenu != nil {
		return model.sessionMenuView()
	}
	if model.rename != nil {
		content := model.rename.view(model.width) + "\nEnter save · Esc cancel"
		if model.rename.err != "" {
			content += "\n" + model.rename.err
		}
		panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(model.palette().lilac)).Padding(1, 2).Width(max(10, min(58, model.width-6))).Render(content)
		return tea.NewView(lipgloss.Place(model.width, model.height, lipgloss.Center, lipgloss.Center, panel))
	}
	if model.runtime != nil && model.runtime.options.Approvals != nil {
		gate := model.runtime.options.Approvals
		if request := gate.Pending(); request != nil {
			return model.approvalView(request)
		}
	}
	if model.plan != nil {
		return model.planView()
	}

	if model.contextReport != nil {
		view := tea.NewView(model.contextReport.view(model.width, model.height, model.noColor))
		view.AltScreen = true
		view.WindowTitle = windowTitle(model.state.Workspace, model.state.Current.Name) + " · Context"
		return view
	}
	if model.settings != nil {
		content := "Settings · " + model.config.Path() + "\n" + model.settings.form.View()
		if model.message != "" {
			content += "\n" + sanitizeTerminal(model.message)
		}
		content += "\nEsc cancel"
		view := tea.NewView(content)
		view.AltScreen = true
		view.WindowTitle = windowTitle(model.state.Workspace, model.state.Current.Name)
		return view
	}
	if model.question != nil && model.showQuestion {
		content := model.question.plan.Form.Title + "\n"
		if model.dismissQuestion {
			content += "Dismiss this question? D dismiss · R return to form · Esc return"
		} else if model.question.submitted {
			content += "Submitting answers; waiting for the stored tool result."
		} else {
			content += model.question.form.View()
		}
		content += "\n" + model.statusLine() + "\nCtrl+T composer · Esc dismissal options"
		if model.message != "" {
			content += "\n" + sanitizeTerminal(model.message)
		}
		view := tea.NewView(content)
		view.WindowTitle = windowTitle(model.state.Workspace, model.state.Current.Name)
		return view
	}
	if model.resumePopup != nil {
		lines := model.resumePopup.view(model.width, model.height-2)
		if model.message != "" {
			lines = append(lines, sanitizeTerminal(model.message))
		}
		view := tea.NewView(strings.Join(lines, "\n"))
		view.WindowTitle = "unreal-agent-repl · " + filepathBase(model.state.Workspace) + " · Resume"
		return view
	}
	settings := model.config.Current()
	sidebarWidth := model.sidebarSize()
	contentWidth := model.width - sidebarWidth
	projection := model.projectDraft(settings, contentWidth)
	inset := 0
	padding := 0
	if contentWidth >= 20 {
		inset = 2
	}
	if model.height >= 12 {
		padding = 2
	}
	if model.height <= 1 {
		view := tea.NewView(ansi.Truncate("> "+projection.Lines[min(projection.CursorRow, len(projection.Lines)-1)], contentWidth, "…"))
		view.WindowTitle = windowTitle(model.state.Workspace, model.state.Current.Name)
		return view
	}
	messageRows := 0
	if model.message != "" && model.height > 2 {
		messageRows = 1
	}
	menuOpen := model.popup != nil || model.completion != nil || model.historySearch != nil
	helpRows := 0
	if model.height >= 5+messageRows && !menuOpen {
		helpRows = 1
	}
	statusRows := 1 + messageRows
	if model.height >= 4 {
		statusRows++
	}
	menuReserve := 0
	if menuOpen && model.height >= 3 {
		menuReserve = 1
	}
	queued := []string(nil)
	if model.runtime != nil {
		queued = model.runtime.QueuedPrompts()
	}
	extraBudget := max(0, model.height-statusRows-helpRows-menuReserve-padding-3)
	queueBudget := 0
	if len(queued) > 0 {
		queueBudget = min(4, extraBudget)
	}
	taskBudget := 0
	if model.taskVisible() {
		if len(queued) > 0 {
			queueBudget = min(4, extraBudget/2)
		}
		taskBudget = min(6, extraBudget-queueBudget)
	}
	taskLines := model.taskRailLines(contentWidth, taskBudget, time.Now())
	queueLines := model.queuedLines(queued, contentWidth, queueBudget)
	anchoredRows := len(taskLines) + len(queueLines)
	visible := min(settings.Editor.MaxHeight, max(1, model.height-statusRows-helpRows-menuReserve-padding-anchoredRows), max(1, projection.VisualRows))
	start := max(0, projection.CursorRow-visible+1)
	if start+visible > projection.VisualRows {
		start = max(0, projection.VisualRows-visible)
	}
	lines := make([]string, 0, visible+2)
	menuAvailable := min(8, max(0, model.height-statusRows-helpRows-visible-padding-anchoredRows))
	if model.popup != nil {
		lines = append(lines, model.popup.view(contentWidth, menuAvailable)...)
	}
	if model.completion != nil {
		lines = append(lines, model.completion.view(contentWidth, menuAvailable)...)
	}
	if model.historySearch != nil {
		lines = append(lines, model.historySearch.view(contentWidth, menuAvailable, model.noColor)...)
	}
	lines = append(lines, taskLines...)
	lines = append(lines, queueLines...)
	if len(lines)+visible+statusRows+helpRows+padding < model.height {
		lines = append(lines, model.composerRule(contentWidth))
	}
	if padding > 0 {
		lines = append(lines, "")
	}
	composerOffset := len(lines)
	for row := start; row < start+visible; row++ {
		prefix := "  "
		if row == start {
			prefix = model.ink("> ", model.palette().accent, true)
		}
		lines = append(lines, strings.Repeat(" ", inset)+prefix+model.styleImageChips(projection.SelectedLines[row]))
	}
	if padding > 0 {
		lines = append(lines, "")
	}
	status := model.statusLineWidth(contentWidth)
	lines = append(lines, strings.Split(status, "\n")...)
	if messageRows != 0 {
		lines = append(lines, ansi.Truncate(strings.ReplaceAll(sanitizeTerminal(model.message), "\n", " ↵ "), contentWidth, "…"))
	}
	if helpRows != 0 {
		lines = append(lines, model.ink(ansi.Truncate(" Enter  send   Alt+Enter  steer  ^R history  ^T tools  ^K palette  /help", contentWidth, "…"), model.palette().muted, false))
	}
	transcriptHeight := max(0, model.height-len(lines))
	transcript := model.transcriptView(contentWidth, transcriptHeight)
	composerOffset += len(transcript)
	lines = append(transcript, lines...)
	if sidebarWidth > 0 {
		lines = model.withForkSidebar(lines, sidebarWidth)
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	if model.popup == nil {
		view.Cursor = tea.NewCursor(min(model.width-1, inset+2+projection.CursorColumn), composerOffset+projection.CursorRow-start)
	}
	view.WindowTitle = windowTitle(model.state.Workspace, model.state.Current.Name)
	view.KeyboardEnhancements.ReportAlternateKeys = true
	return view
}

func (model *uiModel) projectDraft(settings Config, contentWidth int) editor.Projection {
	selected, hasSelection := model.draft.Selection()
	width := max(1, contentWidth-2)
	if contentWidth >= 20 {
		width = contentWidth - 6
	}
	markdown := settings.Editor.Mode == "markdown"
	cached := &model.projection
	if cached.valid && cached.version == model.draft.Version() && cached.cursor == model.draft.Cursor() && cached.width == width && cached.markdown == markdown && cached.noColor == model.noColor && cached.hasSelection == hasSelection && (!hasSelection || cached.selection == selected) {
		return cached.value
	}
	var selection []editor.SourceRange
	if hasSelection {
		selection = append(selection, selected)
	}
	if cached.parsedVersion != model.draft.Version() || !cached.valid {
		cached.spans = nil
		if markdown {
			cached.spans = editor.ParseSyntax(model.draft.Source())
		}
		cached.slashSpans = model.slashSyntaxSpans(model.draft.Source())
		cached.parsedVersion = model.draft.Version()
	}
	spans := make([]editor.SyntaxSpan, 0, len(cached.spans)+len(cached.slashSpans))
	spans = append(spans, cached.spans...)
	spans = append(spans, cached.slashSpans...)
	value := editor.ProjectParsed(model.draft.Source(), model.draft.Cursor(), width, markdown, spans, selection...)
	*cached = projectionCache{version: model.draft.Version(), cursor: model.draft.Cursor(), width: width, markdown: markdown, noColor: model.noColor, selection: selected, hasSelection: hasSelection, value: value, valid: true, spans: cached.spans, parsedVersion: cached.parsedVersion}
	return value
}

func (model *uiModel) nextActivityTick() tea.Cmd {
	generation := model.activityGeneration
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return activityTickMsg(generation) })
}

func (model *uiModel) statusLine() string {
	return model.statusLineWidth(model.width)
}

func (model *uiModel) statusLineWidth(width int) string {
	palette := model.palette()
	configured := model.config.Current().Model
	provider, selected := configured.Provider, configured.ID
	if model.router != nil {
		choice := model.router.Selected()
		provider, selected = choice.Provider, choice.ID
		if active := model.router.ActiveSelections(); len(active) > 0 && active[0] != choice {
			selected = active[0].ID + " → " + choice.ID
		}
	}
	if selected == "" {
		selected = "Choose a model with /model"
	}
	selected = strings.NewReplacer("\n", " ", "\t", " ").Replace(sanitizeTerminal(selected))
	effort := configured.ReasoningEffort
	if effort == "" {
		effort = "default"
	}
	queued, questions := 0, 0
	if model.runtime != nil {
		queued = model.runtime.QueueLength()
		questions = len(model.runtime.Questions())
	}
	activity := "● Ready"
	if model.busy {
		label := "Thinking"
		if len(model.runningTools) > 0 {
			label = fmt.Sprintf("Running %d tools", len(model.runningTools))
		}
		if questions > 0 {
			label = "Answer needed"
		}
		activity = activityFrames[model.activityFrame] + " " + label
		if !model.busySince.IsZero() {
			activity += fmt.Sprintf(" %ds", int(time.Since(model.busySince).Seconds()))
		}
	}
	if queued > 0 {
		activity += fmt.Sprintf(" · %d queued", queued)
	}
	first := model.ink(" "+activity, palette.accent, true)
	identity := selected + " · " + effort + " effort"
	if width >= 105 {
		identity += " · " + provider
	}
	remaining := max(0, width-ansi.StringWidth(first)-4)
	first += "   " + model.ink(ansi.Truncate(identity, remaining, "…"), palette.lilac, true)
	first = ansi.Truncate(first, width, "")
	if model.height < 4 {
		return first
	}
	used := model.contextSnapshot.totalTokens()
	capacity, _ := model.contextCapacity()
	contextText := fmt.Sprintf("Context ≈%s tokens · limit unknown", formatTokenCount(used))
	if capacity > 0 {
		percent := 100 * float64(used) / float64(capacity)
		contextText = fmt.Sprintf("Context ≈%s / %s tokens · %.0f%% · %s available", formatTokenCount(used), formatTokenCount(capacity), percent, formatTokenCount(max(0, capacity-used)))
		if width >= 90 {
			filled := min(10, max(0, used*10/capacity))
			contextText = strings.Repeat("━", filled) + strings.Repeat("┄", 10-filled) + "  " + contextText
		}
	}
	second := model.ink(ansi.Truncate(" "+contextText, width, "…"), palette.muted, false)
	if !model.noColor {
		panel := lipgloss.NewStyle().Background(lipgloss.Color(palette.chip)).Width(width)
		first = panel.Foreground(lipgloss.Color(palette.lilac)).Bold(true).Render(ansi.Strip(first))
		second = panel.Foreground(lipgloss.Color(palette.muted)).Render(ansi.Strip(second))
	}
	return first + "\n" + second
}

func renderCompleted(raw string, width int, theme string, noColor bool) string {
	clean := sanitizeTerminal(raw)
	if noColor || !looksLikeMarkdown(clean) {
		return clean
	}
	style := "dark"
	if theme == "light" {
		style = "light"
	}
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(max(20, width)))
	if err != nil {
		return clean
	}
	formatted, err := renderer.Render(clean)
	if err != nil {
		return clean
	}
	return strings.TrimSpace(formatted)
}

func looksLikeMarkdown(source string) bool {
	root := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough)).Parser().Parse(text.NewReader([]byte(source)))
	structured := false
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.(type) {
		case *ast.Heading, *ast.List, *ast.FencedCodeBlock, *ast.CodeBlock,
			*ast.Blockquote, *ast.Emphasis, *ast.Link, *extensionast.Table, *extensionast.Strikethrough:
			structured = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return structured
}

func sanitizeTerminal(source string) string {
	source = ansi.Strip(source)
	var result strings.Builder
	for _, letter := range source {
		if unicode.IsControl(letter) && letter != '\n' && letter != '\t' {
			continue
		}
		result.WriteRune(letter)
	}
	return result.String()
}

func windowTitle(workspace, name string) string {
	flat := strings.NewReplacer("\n", " ", "\t", " ")
	title := "unreal-agent-repl · " + flat.Replace(sanitizeTerminal(filepathBase(workspace))) + " · " + flat.Replace(sanitizeTerminal(name))
	if letters := []rune(title); len(letters) > 160 {
		title = string(letters[:160])
	}
	return title
}

func filepathBase(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}
