package repl

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
	"github.com/unreallabsai/unreal-agent/harness/tool/viewimage"
)

//go:embed SYSTEM_PROMPT.md
var defaultSystemPrompt string

func RunCLI(ctx context.Context, args []string, getenv func(string) string, input, output *os.File, errorsTo io.Writer) (runErr error) {
	flags := flag.NewFlagSet("unreal-agent-repl", flag.ContinueOnError)
	flags.SetOutput(errorsTo)
	workspaceFlag := flags.String("workspace", ".", "working directory for the agent")
	sessionDirectoryFlag := flags.String("session-directory", "", "override the session store directory")
	heartbeat := flags.Duration("tool-heartbeat-interval", 10*time.Minute, "tool wait heartbeat interval")
	resume, cleaned, err := parseResumeOption(args)
	if err != nil {
		return err
	}
	if err := flags.Parse(cleaned); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; this command requires a terminal", flags.Arg(0))
	}
	if !term.IsTerminal(input.Fd()) || !term.IsTerminal(output.Fd()) {
		return errors.New("unreal-agent-repl needs an interactive terminal; use unreal-agent-runner for JSON or stdin requests")
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		return err
	}
	stateDirectory, err := StateDirectory(getenv)
	if err != nil {
		return err
	}
	state, err := OpenSessionState(stateDirectory, *workspaceFlag)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, state.Close()) }()
	if *sessionDirectoryFlag != "" {
		store, err := localfile.New(*sessionDirectoryFlag)
		if err != nil {
			return err
		}
		state.Store = store
	}
	if resume.Picker {
		// The picker acquires a session lock only after the user chooses one.
	} else if resume.Name != "" {
		_, err = state.Resume(ctx, resume.Name)
	} else {
		_, err = state.New(ctx)
	}
	if err != nil {
		return err
	}
	router, modelError := NewModelRouter(config, getenv)
	catalog := NewModelCatalog(config, getenv)
	if router != nil {
		defer func() { runErr = errors.Join(runErr, router.Close()) }()
	}
	var runtime *Runtime
	if modelError == nil && !resume.Picker {
		runtime, err = newAppRuntime(ctx, state, router, getenv, *heartbeat)
		if err != nil {
			return err
		}
		defer runtime.Close()
	}
	model := newUI(ctx, getenv, *heartbeat, state, config, router, catalog, runtime, modelError)
	defer model.closeWorkflow()
	defer model.cleanupAttachments()
	defer model.cleanupHistoryDraftAttachments()
	defer func() {
		if model.editorPath != "" {
			_ = os.Remove(model.editorPath)
		}
	}()
	if resume.Picker {
		if err := model.openResumePopup(true); err != nil {
			return err
		}
	}
	defer func() {
		if model.runtime != nil && model.runtime != runtime {
			model.runtime.Close()
		}
		if model.router != nil && model.router != router {
			runErr = errors.Join(runErr, model.router.Close())
		}
	}()
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
	_, err = program.Run()
	return err
}

type resumeChoice struct {
	Picker bool
	Name   string
}

func parseResumeOption(args []string) (resumeChoice, []string, error) {
	var result resumeChoice
	cleaned := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		value := args[index]
		if strings.HasPrefix(value, "--resume=") {
			if result.Picker || result.Name != "" {
				return resumeChoice{}, nil, errors.New("--resume was supplied more than once")
			}
			result.Name = strings.TrimPrefix(value, "--resume=")
			if result.Name == "" {
				result.Picker = true
			}
			continue
		}
		if value == "--resume" {
			if result.Picker || result.Name != "" {
				return resumeChoice{}, nil, errors.New("--resume was supplied more than once")
			}
			if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
				index++
				result.Name = args[index]
			} else {
				result.Picker = true
			}
			continue
		}
		cleaned = append(cleaned, value)
	}
	return result, cleaned, nil
}

func newAppRuntime(ctx context.Context, state *SessionState, router *ModelRouter, getenv func(string) string, heartbeat time.Duration) (*Runtime, error) {
	return newAppRuntimeWithFormat(ctx, state, router, getenv, heartbeat, nil)
}

func newAppRuntimeWithFormat(ctx context.Context, state *SessionState, router *ModelRouter, getenv func(string) string, heartbeat time.Duration, format *llm.OutputFormat, systemPromptAppend ...string) (*Runtime, error) {
	provenance := newResponseProvenance(state)
	router.SetProvenance(provenance)
	id := session.ID(state.Current.SessionID)
	operationDirectory := filepath.Join(state.Directory, "operations", string(id))
	if err := os.MkdirAll(operationDirectory, 0700); err != nil {
		return nil, err
	}
	shell := strings.TrimSpace(getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	baseRegistry := tool.NewRegistry(tool.StaticTranslators{
		Bash:      bash.New(bash.Config{Shell: shell, Directory: state.Workspace, BaseDirectory: operationDirectory}),
		ViewImage: viewimage.New(viewimage.Config{Directory: state.Workspace}),
	}, tool.BashName, tool.ViewImageName, tool.SkillUseName)
	skillAccess := &skillRegistry{Registry: baseRegistry}
	registry := &approvalGate{Registry: taskRegistry{Registry: askUserRegistry{Registry: skillAccess}}, config: router.config, ctx: ctx}
	registrations := map[string]tool.RegistrationID{}
	questions := newAskUserHandler()
	managerCtx, managerCancel := context.WithCancel(ctx)
	manager := operation.NewLocalOperationManager(managerCtx, questions, &taskHandler{ctx: managerCtx, store: state.Store, sessionID: id, updates: make(chan operation.Operation, 128)})
	factory := func(input string) (contextbuilder.Builder, func() bool, error) {
		instructions, err := LoadInstructions(ctx, state.Workspace, getenv)
		if err != nil {
			return nil, nil, err
		}
		catalog, err := DiscoverCLISkills(state.Workspace, instructions.ProjectRoot, getenv)
		if err != nil {
			return nil, nil, err
		}
		explicit := explicitSkillNames(input)
		if err := validateExplicitSkills(catalog, explicit); err != nil {
			return nil, nil, err
		}
		for _, registration := range registrations {
			baseRegistry.UnregisterSkill(registration)
		}
		clear(registrations)
		for _, entry := range catalog.Entries {
			registration, err := baseRegistry.RegisterSkill(entry.Skill)
			if err != nil {
				return nil, nil, err
			}
			registrations[entry.Skill.Name] = registration
		}
		skillAccess.SetAllowed(catalog, explicit)
		builder := contextbuilder.NewBuilder(catalog.ModelSkills(explicit)...)
		builder.SetModel(llm.Model{ID: router.Selected().ID, OutputFormat: format})
		prompt := cliSystemPrompt(state.Workspace, instructions.Prompt(), router.config.Current().SystemPrompt, systemPromptAppend...)
		if router.config.Current().Mode == "plan" {
			prompt += "\n\nPlan mode is read-only. Do not change files or run commands. Investigate with read-only tools, then produce a clear, actionable plan for the user to review and revise."
		}
		builder.SetSystemPrompt(prompt)
		for _, definition := range registry.StaticDefinitions() {
			builder.AddTool(definition.Tool)
		}
		return &provenanceBuilder{Builder: builder, provenance: provenance}, func() bool { return questions.Pending() == 0 }, nil
	}
	onSteer := func(input string) error {
		names := explicitSkillNames(input)
		if len(names) == 0 {
			return nil
		}
		catalog, err := DiscoverCLISkills(state.Workspace, projectBoundary(state.Workspace), getenv)
		if err != nil {
			return err
		}
		if err := validateExplicitSkills(catalog, names); err != nil {
			return err
		}
		for name := range names {
			if _, exists := registrations[name]; !exists {
				entry, _ := catalog.UserSkill(name)
				registration, err := baseRegistry.RegisterSkill(entry.Skill)
				if err != nil {
					return err
				}
				registrations[name] = registration
			}
			skillAccess.AllowExplicit(name)
		}
		return nil
	}
	validateInput := func(input string) error {
		names := explicitSkillNames(input)
		if len(names) == 0 {
			return nil
		}
		catalog, err := DiscoverCLISkills(state.Workspace, projectBoundary(state.Workspace), getenv)
		if err != nil {
			return err
		}
		return validateExplicitSkills(catalog, names)
	}
	runtime, err := NewRuntime(ctx, RuntimeOptions{
		SessionID: id, Sessions: state.Store, LLM: router, Tools: registry,
		Operations: manager, NewBuilder: factory, OnSteer: onSteer, ValidateInput: validateInput, ToolHeartbeatInterval: heartbeat, Questions: questions, CloseOperations: managerCancel,
	})
	if err != nil {
		managerCancel()
		return nil, err
	}
	questions.SetNotify(func() { runtime.mailbox.publish(RuntimeEvent{Kind: EventQuestion, Generation: runtime.generation}) })
	registry.notify = func() { runtime.mailbox.publish(RuntimeEvent{Kind: EventApproval, Generation: runtime.generation}) }
	runtime.options.Approvals = registry
	if err := runtime.Recover(); err != nil {
		runtime.Close()
		return nil, err
	}
	return runtime, nil
}

func cliSystemPrompt(workspace, instructions, prompt string, additions ...string) string {
	result := strings.TrimSpace(prompt) + "\n\nWorkspace: " + workspace + "\n\n" + instructions
	for _, addition := range additions {
		if strings.TrimSpace(addition) != "" {
			result += "\n\n" + addition
		}
	}
	return result
}
