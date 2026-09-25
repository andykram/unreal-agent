package repl

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
)

type providerSettingValues struct {
	baseURL string
	models  string
}

type settingsValues struct {
	mode          string
	provider      string
	modelID       string
	effort        string
	attempts      string
	contextWindow string
	editorMode    string
	editorHeight  string
	editorCommand string
	theme         string
	historyCount  string
	providers     map[string]*providerSettingValues
}

type settingsForm struct {
	form    *huh.Form
	values  settingsValues
	initial Config
	path    string
}

func newSettingsForm(config *ConfigStore, width, height int) *settingsForm {
	return newSettingsFormWithValues(config, width, height, nil)
}

func newSettingsFormWithValues(config *ConfigStore, width, height int, preset *settingsValues) *settingsForm {
	initial := config.Current()
	current := &settingsForm{initial: initial, path: config.Path()}
	values := &current.values
	values.mode = initial.Mode
	values.provider, values.modelID, values.effort = initial.Model.Provider, initial.Model.ID, initial.Model.ReasoningEffort
	values.attempts = strconv.Itoa(initial.Model.MaxAttempts)
	values.contextWindow = strconv.Itoa(initial.Model.ContextWindowTokens)
	values.editorMode, values.editorHeight, values.editorCommand = initial.Editor.Mode, strconv.Itoa(initial.Editor.MaxHeight), initial.Editor.Command
	values.theme, values.historyCount = initial.Appearance.Theme, strconv.Itoa(initial.History.MaxEntries)
	values.providers = make(map[string]*providerSettingValues)
	var providerOptions []huh.Option[string]
	for _, provider := range agentrunner.DefaultProviders() {
		providerOptions = append(providerOptions, huh.NewOption(provider.Name, provider.Name))
		configured := initial.Providers[provider.Name]
		values.providers[provider.Name] = &providerSettingValues{baseURL: configured.BaseURL, models: strings.Join(configured.Models, "\n")}
	}
	if preset != nil {
		*values = *preset
		values.providers = make(map[string]*providerSettingValues, len(preset.providers))
		for name, fields := range preset.providers {
			copy := *fields
			values.providers[name] = &copy
		}
	}
	modelGroup := huh.NewGroup(
		huh.NewSelect[string]().Title("Harness mode").Options(huh.NewOption("Default (current behavior)", "default"), huh.NewOption("All actions", "all"), huh.NewOption("File edits; ask before Bash", "edit"), huh.NewOption("Plan only (read-only)", "plan")).Value(&values.mode),
		huh.NewSelect[string]().Title("Provider").Options(providerOptions...).Value(&values.provider),
		huh.NewInput().Title("Model ID (blank uses provider default)").Value(&values.modelID),
		huh.NewSelect[string]().Title("Reasoning effort").Options(
			huh.NewOption("Provider default", ""), huh.NewOption("Low", "low"), huh.NewOption("Medium", "medium"), huh.NewOption("High", "high"), huh.NewOption("XHigh", "xhigh"), huh.NewOption("Max", "max"),
		).Value(&values.effort),
		huh.NewInput().Title("Maximum attempts").Value(&values.attempts).Validate(positiveNumber("maximum attempts")),
		huh.NewInput().Title("Context window tokens (0 if unknown)").Value(&values.contextWindow).Validate(func(value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return fmt.Errorf("context window must be zero or positive")
			}
			return nil
		}),
	).Title("Model settings · credentials remain in environment variables")
	editorGroup := huh.NewGroup(
		huh.NewSelect[string]().Title("Editor mode").Options(huh.NewOption("Plain", "plain"), huh.NewOption("Markdown preview", "markdown")).Value(&values.editorMode),
		huh.NewInput().Title("Maximum visible editor rows (1–100)").Value(&values.editorHeight).Validate(func(value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("editor height must be between 1 and 100")
			}
			return nil
		}),
		huh.NewInput().Title("External editor command (blank uses EDITOR, VISUAL, vi)").Value(&values.editorCommand),
		huh.NewSelect[string]().Title("Appearance").Options(huh.NewOption("Automatic", "auto"), huh.NewOption("Light", "light"), huh.NewOption("Dark", "dark")).Value(&values.theme),
	).Title("Editor and appearance")
	historyGroup := huh.NewGroup(huh.NewInput().Title("History entries to retain").Value(&values.historyCount).Validate(positiveNumber("history retention"))).Title("History")
	groups := []*huh.Group{modelGroup, editorGroup, historyGroup}
	for _, provider := range agentrunner.DefaultProviders() {
		providerValues := values.providers[provider.Name]
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Base URL (blank uses provider default)").Value(&providerValues.baseURL),
			huh.NewText().Title("Configured model IDs, one per line").Value(&providerValues.models),
		).Title("Provider · "+provider.Name))
	}
	confirmed := false
	groups = append(groups, huh.NewGroup(
		huh.NewNote().Title("Review").Description("Save changes to "+config.Path()+"\nCredentials are read from environment variables and are never displayed here."),
		huh.NewConfirm().Title("Save these settings?").Affirmative("Save").Negative("Go back").Value(&confirmed).Validate(func(value bool) error {
			if !value {
				return fmt.Errorf("select Save, or go back to edit")
			}
			return nil
		}),
	).Title("Save or press Esc to cancel"))
	current.form = huh.NewForm(groups...).WithWidth(max(20, width-2)).WithHeight(max(6, height-3))
	return current
}

func positiveNumber(label string) func(string) error {
	return func(value string) error {
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 {
			return fmt.Errorf("%s must be positive", label)
		}
		return nil
	}
}

func (current *settingsForm) Update(message tea.Msg) tea.Cmd {
	updated, command := current.form.Update(message)
	if form, ok := updated.(*huh.Form); ok {
		current.form = form
	}
	return command
}

func (current *settingsForm) candidate() (Config, map[string]any, error) {
	value := cloneConfig(current.initial)
	value.Mode = current.values.mode
	value.Model.Provider, value.Model.ID, value.Model.ReasoningEffort = current.values.provider, current.values.modelID, current.values.effort
	var err error
	value.Model.MaxAttempts, err = strconv.Atoi(current.values.attempts)
	if err != nil {
		return Config{}, nil, err
	}
	value.Model.ContextWindowTokens, err = strconv.Atoi(current.values.contextWindow)
	if err != nil {
		return Config{}, nil, err
	}
	if (value.Model.Provider != current.initial.Model.Provider || value.Model.ID != current.initial.Model.ID) && value.Model.ContextWindowTokens == current.initial.Model.ContextWindowTokens {
		value.Model.ContextWindowTokens = 0
	}
	value.Editor.Mode, value.Editor.Command = current.values.editorMode, current.values.editorCommand
	value.Editor.MaxHeight, err = strconv.Atoi(current.values.editorHeight)
	if err != nil {
		return Config{}, nil, err
	}
	value.Appearance.Theme = current.values.theme
	value.History.MaxEntries, err = strconv.Atoi(current.values.historyCount)
	if err != nil {
		return Config{}, nil, err
	}
	changes := make(map[string]any)
	if value.Mode != current.initial.Mode {
		changes["mode"] = value.Mode
	}
	if value.Model.Provider != current.initial.Model.Provider {
		changes["model.provider"] = value.Model.Provider
	}
	if value.Model.ID != current.initial.Model.ID {
		changes["model.id"] = value.Model.ID
	}
	if value.Model.ReasoningEffort != current.initial.Model.ReasoningEffort {
		changes["model.reasoning_effort"] = value.Model.ReasoningEffort
	}
	if value.Model.MaxAttempts != current.initial.Model.MaxAttempts {
		changes["model.max_attempts"] = value.Model.MaxAttempts
	}
	if value.Model.ContextWindowTokens != current.initial.Model.ContextWindowTokens {
		changes["model.context_window_tokens"] = value.Model.ContextWindowTokens
	}
	if value.Editor.Mode != current.initial.Editor.Mode {
		changes["editor.mode"] = value.Editor.Mode
	}
	if value.Editor.MaxHeight != current.initial.Editor.MaxHeight {
		changes["editor.max_height"] = value.Editor.MaxHeight
	}
	if value.Editor.Command != current.initial.Editor.Command {
		changes["editor.command"] = value.Editor.Command
	}
	if value.Appearance.Theme != current.initial.Appearance.Theme {
		changes["appearance.theme"] = value.Appearance.Theme
	}
	if value.History.MaxEntries != current.initial.History.MaxEntries {
		changes["history.max_entries"] = value.History.MaxEntries
	}
	for name, fields := range current.values.providers {
		configured := value.Providers[name]
		configured.BaseURL = strings.TrimSpace(fields.baseURL)
		configured.Models = nil
		for _, line := range strings.Split(fields.models, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				configured.Models = append(configured.Models, trimmed)
			}
		}
		value.Providers[name] = configured
		previous := current.initial.Providers[name]
		if configured.BaseURL != previous.BaseURL {
			changes["providers."+name+".base_url"] = configured.BaseURL
		}
		if !slices.Equal(configured.Models, previous.Models) {
			changes["providers."+name+".models"] = configured.Models
		}
	}
	if err := validateConfig(value); err != nil {
		return Config{}, nil, err
	}
	return value, changes, nil
}
