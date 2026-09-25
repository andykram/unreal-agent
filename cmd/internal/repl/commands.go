package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
)

type commandChoice struct{ Label, Description, Value string }
type commandSpec struct {
	Name         string
	Description  string
	ArgumentHint string
	Complete     func(*uiModel, string) []commandChoice
	Run          func(*uiModel, string, string) (tea.Model, tea.Cmd)
}

func commandRegistry() []commandSpec {
	return []commandSpec{
		{Name: "/help", Description: "Show commands and keys", Run: (*uiModel).runHelp},
		{Name: "/effort", Description: "Switch reasoning effort for the next request", ArgumentHint: "[default|low|medium|high|xhigh|max]", Complete: completeEffort, Run: (*uiModel).runEffort},
		{Name: "/provider", Description: "Choose a provider and its model", ArgumentHint: "[provider]", Complete: completeProviderArgument, Run: (*uiModel).runProvider},
		{Name: "/model", Description: "Choose provider and model", ArgumentHint: "[provider] [model-id]", Complete: completeModelArgument, Run: (*uiModel).runModel},
		{Name: "/skills", Description: "Search skills by name or description", ArgumentHint: "[search words]", Complete: completeSkills, Run: (*uiModel).runSkills},
		{Name: "/reload-config", Description: "Reload config and re-index skills", Run: (*uiModel).runReloadConfig},
		{Name: "/settings", Description: "Edit persistent settings", Run: (*uiModel).runSettings},
		{Name: "/system-prompt", Description: "Show the effective loaded system prompt", Run: (*uiModel).runSystemPrompt},
		{Name: "/plan", Description: "Review and comment on the latest assistant response", Run: (*uiModel).runPlan},
		{Name: "/workflow", Description: "Compile and run a Python workflow", ArgumentHint: "[name.py | path | resume run-id]", Complete: completeWorkflowArgument, Run: (*uiModel).runWorkflow},
		{Name: "/context", Description: "Show this session's context breakdown", Run: (*uiModel).runContext},
		{Name: "/resume", Description: "Resume a named session", ArgumentHint: "[name-or-id]", Complete: completeResumeArgument, Run: (*uiModel).runResume},
		{Name: "/new", Description: "Start a new session", Run: (*uiModel).runNew},
		{Name: "/fork", Description: "Fork this session at its latest turn", ArgumentHint: "[name]", Run: (*uiModel).runFork},
		{Name: "/rename", Description: "Rename this session", ArgumentHint: "<name>", Run: (*uiModel).runRename},
		{Name: "/attach", Description: "Attach a local image", ArgumentHint: "<path>", Complete: completeAttachmentArgument, Run: (*uiModel).runAttach},
		{Name: "/detach", Description: "Remove the last image chip", Run: (*uiModel).runDetach},
		{Name: "/quit", Description: "Exit when idle", Run: (*uiModel).runQuit},
	}
}

// splitCommand keeps the workflow path intact, including spaces after the colon.
func splitCommand(raw string) (name, argument string, hasArguments bool) {
	if path, ok := strings.CutPrefix(raw, "/workflow:"); ok {
		return "/workflow", path, true
	}
	return strings.Cut(raw, " ")
}

func (model *uiModel) command(raw string) (tea.Model, tea.Cmd) {
	name, argument, _ := splitCommand(raw)
	argument = strings.TrimSpace(argument)
	for _, command := range commandRegistry() {
		if command.Name == name {
			return command.Run(model, argument, raw)
		}
	}
	model.message = "Unknown command: " + name
	return model, nil
}

func (model *uiModel) runHelp(_, _ string) (tea.Model, tea.Cmd) {
	model.draft.Clear()
	var lines []string
	for _, command := range commandRegistry() {
		name, hint := command.Name, command.ArgumentHint
		if name == "/workflow" {
			name, hint = "/workflow", "<path.py> (or resume ID)"
		}
		lines = append(lines, fmt.Sprintf("%s %s · %s", name, hint, command.Description))
	}
	lines = append(lines, "Readline: Ctrl+A/E line edges · Ctrl+B/F character · Alt+B/F word · Ctrl+W/Alt+D delete word · Ctrl+U/Alt+K kill line · Ctrl+Y yank · Ctrl+Z undo · Ctrl+Shift+Z redo")
	lines = append(lines, "Enter send/queue · Alt+Enter steer · Ctrl+K command palette · Ctrl+R history · Ctrl+J newline · Ctrl+G editor · Ctrl+V paste · Ctrl+X remove image · Ctrl+D exit · Ctrl+Q question · Ctrl+T tools · PgUp/PgDn scroll · Ctrl+C twice exit")
	model.appendText("help", strings.Join(lines, "\n"))
	return model, nil
}

func (model *uiModel) runQuit(_, raw string) (tea.Model, tea.Cmd) {
	if model.hasRunningWorkflow() {
		model.message = "A workflow step is running. Press Ctrl+C to stop it first."
		return model, nil
	}
	if model.busy || model.runtime != nil && model.runtime.QueueLength() > 0 {
		model.message = "Work or queued prompts remain. Press Ctrl+C to stop work first."
		return model, nil
	}
	if err := model.recordHistory("command", raw, "immediate"); err != nil {
		model.message = err.Error()
	}
	return model, tea.Quit
}

func (model *uiModel) runRename(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		model.message = "Usage: /rename <name>"
		return model, nil
	}
	if err := model.state.Rename(context.Background(), argument); err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.refreshForkChoices()
	model.message = "Session renamed to " + argument
	return model, nil
}

func (model *uiModel) runModel(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		return model.openModelPopup()
	}
	parts := strings.Fields(argument)
	provider := model.config.Current().Model.Provider
	id := argument
	if len(parts) >= 2 {
		provider = parts[0]
		id = strings.TrimSpace(strings.TrimPrefix(argument, provider))
	}
	command, err := model.chooseModel(provider, id)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.message = "Selected " + provider + "/" + id + " for the next model request."
	return model, command
}

func (model *uiModel) runProvider(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		_ = model.draft.Set("/provider ")
		model.suppressCompletion = false
		model.refreshCompletion()
		return model, nil
	}
	if model.catalog == nil {
		model.message = "Model catalog is unavailable."
		return model, nil
	}
	if _, ok := model.catalog.providers[argument]; !ok {
		model.message = "Unknown provider: " + argument
		return model, nil
	}
	return model.openProviderPopup(argument)
}

func completeProviderArgument(model *uiModel, prefix string) []commandChoice {
	if model.catalog == nil {
		return nil
	}
	var choices []commandChoice
	for _, provider := range model.catalog.ProviderNames() {
		if strings.HasPrefix(provider, prefix) {
			choices = append(choices, commandChoice{Label: provider, Description: "provider", Value: "/provider " + provider})
		}
	}
	return choices
}

func (model *uiModel) runSettings(_, _ string) (tea.Model, tea.Cmd) {
	model.settings = newSettingsForm(model.config, model.width, model.height)
	model.draft.Clear()
	model.message = ""
	return model, model.settings.form.Init()
}

func (model *uiModel) runContext(_, _ string) (tea.Model, tea.Cmd) {
	report, err := buildContextReport(model.ctx, model)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.contextReport = &report
	model.contextSnapshot = report
	model.message = ""
	return model, nil
}

func (model *uiModel) runAttach(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		model.message = "Usage: /attach <path>"
		return model, nil
	}
	attachment, err := attachLocalPath(model.state, argument)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.addAttachment(attachment)
	return model, nil
}

func (model *uiModel) runDetach(_, _ string) (tea.Model, tea.Cmd) {
	if model.removeLastAttachment() {
		model.draft.Clear()
	}
	return model, nil
}

func (model *uiModel) removeLastAttachment() bool {
	if len(model.attachments) == 0 {
		model.message = "No image attachment to remove."
		return false
	}
	last := model.attachments[len(model.attachments)-1]
	if !last.Submitted && last.Path != "" {
		if err := os.Remove(last.Path); err != nil && !os.IsNotExist(err) {
			model.message = err.Error()
			return false
		}
	}
	model.removeImageChip(len(model.attachments))
	model.attachments = model.attachments[:len(model.attachments)-1]
	model.message = "Removed image attachment."
	return true
}

func (model *uiModel) runResume(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		if err := model.openResumePopup(false); err != nil {
			model.message = err.Error()
		}
		return model, nil
	}
	command, err := model.switchSession(argument)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.message = "Resumed " + model.state.Current.Name
	return model, command
}

func (model *uiModel) runNew(_, _ string) (tea.Model, tea.Cmd) {
	command, err := model.newSession()
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.message = "Started a new session."
	return model, command
}

func (model *uiModel) runFork(name, _ string) (tea.Model, tea.Cmd) {
	if model.runtime != nil && (model.runtime.IsBusy() || model.runtime.QueueLength() != 0) {
		model.message = "Finish active work and queued prompts before forking."
		return model, nil
	}
	child, err := model.state.ForkCurrent(model.ctx, name)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	command, err := model.switchSession(child.SessionID)
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.message = "Forked into " + child.Name
	return model, command
}

func completeModelArgument(model *uiModel, argument string) []commandChoice {
	var choices []commandChoice
	parts := strings.Fields(argument)
	if len(parts) < 2 && !strings.Contains(argument, " ") {
		for _, provider := range model.catalog.ConfiguredProviders() {
			if strings.HasPrefix(provider, argument) {
				choices = append(choices, commandChoice{Label: provider, Description: "provider", Value: "/model " + provider + " "})
			}
		}
		current := model.config.Current().Model.Provider
		for _, choice := range model.catalog.Choices() {
			if choice.Ref.Provider == current && strings.HasPrefix(choice.Ref.ID, argument) {
				choices = append(choices, commandChoice{Label: choice.Ref.ID, Description: current + " · " + choice.Source, Value: "/model " + current + " " + choice.Ref.ID})
			}
		}
		return choices
	}
	provider, idPrefix, _ := strings.Cut(argument, " ")
	idPrefix = strings.TrimSpace(idPrefix)
	for _, choice := range model.catalog.Choices() {
		if choice.Ref.Provider == provider && strings.HasPrefix(choice.Ref.ID, idPrefix) {
			choices = append(choices, commandChoice{Label: choice.Ref.ID, Description: provider + " · " + choice.Source, Value: "/model " + provider + " " + choice.Ref.ID})
		}
	}
	return choices
}

func completeResumeArgument(model *uiModel, argument string) []commandChoice {
	entries, err := model.state.List(model.ctx)
	if err != nil {
		return nil
	}
	var choices []commandChoice
	for _, entry := range entries {
		if entry.Issue != nil || entry.Metadata.Workspace != model.state.Workspace {
			continue
		}
		name := entry.Metadata.Name
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(argument)) {
			choices = append(choices, commandChoice{Label: name, Description: entry.Metadata.SessionID, Value: "/resume " + name})
		} else if strings.HasPrefix(entry.Metadata.SessionID, argument) {
			choices = append(choices, commandChoice{Label: entry.Metadata.SessionID, Description: name, Value: "/resume " + entry.Metadata.SessionID})
		}
	}
	return choices
}

func completeAttachmentArgument(model *uiModel, argument string) []commandChoice {
	path := argument
	if strings.HasPrefix(path, `"`) {
		decoded, err := strconv.Unquote(path)
		if err != nil {
			return nil
		}
		path = decoded
	}
	base := model.state.Workspace
	if filepath.IsAbs(path) {
		base = ""
	}
	matches, err := filepath.Glob(filepath.Join(base, path) + "*")
	if err != nil {
		return nil
	}
	var choices []commandChoice
	for _, match := range matches {
		if len(choices) >= 30 {
			break
		}
		value := match
		if base != "" {
			value, _ = filepath.Rel(base, match)
		}
		if strings.ContainsAny(value, " \t") {
			value = strconv.Quote(value)
		}
		choices = append(choices, commandChoice{Label: filepath.Base(match), Description: "local image", Value: "/attach " + value})
	}
	slices.SortFunc(choices, func(a, b commandChoice) int { return strings.Compare(a.Label, b.Label) })
	return choices
}

func (model *uiModel) runSystemPrompt(_, _ string) (tea.Model, tea.Cmd) {
	prompt := ""
	label := "System prompt · loaded for the latest task"
	if model.runtime != nil {
		prompt = model.runtime.SystemPrompt()
	}
	if prompt == "" {
		label = "System prompt · next task preview"
		instructions, err := LoadInstructions(model.ctx, model.state.Workspace, model.getenv)
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		skills, err := DiscoverCLISkills(model.state.Workspace, instructions.ProjectRoot, model.getenv)
		if err != nil {
			model.message = err.Error()
			return model, nil
		}
		builder := contextbuilder.NewBuilder(skills.ModelSkills(nil)...)
		builder.SetSystemPrompt(cliSystemPrompt(model.state.Workspace, instructions.Prompt(), model.config.Current().SystemPrompt))
		prompt = basePromptText(builder)
	}
	model.draft.Clear()
	model.message = ""
	model.transcriptTop = len(model.transcriptRows)
	model.appendText(label, prompt)
	model.followTranscript = false
	return model, nil
}

func (model *uiModel) runSkills(query, _ string) (tea.Model, tea.Cmd) {
	_ = model.draft.Set("/skills " + query)
	model.suppressCompletion = false
	model.refreshCompletion()
	if len(completeSkills(model, query)) == 0 {
		model.message = "No matching user-invocable skills."
	} else {
		model.message = "↑/↓ choose · Enter insert skill · type to search"
	}
	return model, nil
}

func (model *uiModel) runReloadConfig(_, _ string) (tea.Model, tea.Cmd) {
	candidate, err := model.config.read()
	if err != nil {
		model.message = "Reload failed: " + err.Error()
		return model, nil
	}
	instructions, err := LoadInstructions(model.ctx, model.state.Workspace, model.getenv)
	if err != nil {
		model.message = "Reload failed: " + err.Error()
		return model, nil
	}
	skills, err := DiscoverCLISkills(model.state.Workspace, instructions.ProjectRoot, model.getenv)
	if err != nil {
		model.message = "Reload failed: " + err.Error()
		return model, nil
	}
	if candidate.Mode != model.config.Current().Mode && model.runtime != nil && model.runtime.IsBusy() {
		model.message = "Reload failed: stop active work before changing harness mode."
		return model, nil
	}
	var wait tea.Cmd
	if model.router != nil {
		if err := model.router.Reload(candidate); err != nil {
			model.message = "Reload failed: " + err.Error()
			return model, nil
		}
	} else {
		// A reload must also work before credentials are configured.
		model.config.mu.Lock()
		model.config.active = cloneConfig(candidate)
		model.config.mu.Unlock()
		router, err := NewModelRouter(model.config, model.getenv)
		if err == nil {
			runtime, runtimeErr := newAppRuntime(model.ctx, model.state, router, model.getenv, model.heartbeat)
			if runtimeErr == nil {
				model.router, model.runtime, model.setupError = router, runtime, nil
				wait = model.waitRuntime()
			} else {
				_ = router.Close()
				model.setupError = runtimeErr
			}
		} else {
			model.setupError = err
		}
	}
	model.skills = skills
	// Drop provider metadata tied to old endpoints and invalidate pending discovery.
	if model.catalog != nil {
		model.catalog.mu.Lock()
		for provider := range model.catalog.latest {
			model.catalog.latest[provider]++
		}
		clear(model.catalog.discovered)
		model.catalog.mu.Unlock()
	}
	model.draft.Clear()
	model.completion = nil
	model.message = ""
	model.refreshContextSnapshot()
	return model, tea.Batch(wait, model.selectedContextCatalogCmd(), model.appendNotice(fmt.Sprintf("Config reloaded; %d skills indexed. Active tasks retain their loaded instructions until the next task.", len(skills.Entries))))
}

func completeEffort(_ *uiModel, prefix string) []commandChoice {
	var choices []commandChoice
	for _, effort := range []string{"default", "low", "medium", "high", "xhigh", "max"} {
		if strings.HasPrefix(effort, strings.TrimSpace(prefix)) {
			choices = append(choices, commandChoice{Label: effort, Description: "reasoning effort", Value: "/effort " + effort})
		}
	}
	return choices
}
func (model *uiModel) runEffort(argument, _ string) (tea.Model, tea.Cmd) {
	if argument == "" {
		_ = model.draft.Set("/effort ")
		model.suppressCompletion = false
		model.refreshCompletion()
		effort := model.config.Current().Model.ReasoningEffort
		if effort == "" {
			effort = "default"
		}
		model.message = "Current effort: " + effort + " · Choose a level for the next model request."
		return model, nil
	}
	effort := argument
	if effort == "default" {
		effort = ""
	}
	candidate := model.config.Current()
	candidate.Model.ReasoningEffort = effort
	if err := validateConfig(candidate); err != nil {
		model.message = err.Error()
		return model, nil
	}
	changes := map[string]any{"model.reasoning_effort": effort}
	var err error
	if model.router != nil {
		err = model.router.ApplySettings(changes, candidate)
	} else {
		err = model.config.SaveSettings(changes)
	}
	if err != nil {
		model.message = err.Error()
		return model, nil
	}
	model.draft.Clear()
	model.message = ""
	return model, model.appendNotice("Effort set to " + argument + " for the next model request.")
}

func (model *uiModel) runPlan(_, _ string) (tea.Model, tea.Cmd) {
	if model.openPlan() {
		model.draft.Clear()
	}
	return model, nil
}
