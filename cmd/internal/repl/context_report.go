package repl

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

type contextPart struct {
	label  string
	tokens int
}

type contextReport struct {
	parts           []contextPart
	requests        int
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	usageReported   bool
	opaqueItems     int
	capacity        int
	capacitySource  string
}

// Text token counts are estimates. Provider tokenization and hidden image or
// reasoning payloads cannot be reconstructed from the durable transcript.
func estimatedTokens(text string) int { return (len(text) + 3) / 4 }

func buildContextReport(ctx context.Context, model *uiModel) (contextReport, error) {
	instructions, err := LoadInstructions(ctx, model.state.Workspace, model.getenv)
	if err != nil {
		return contextReport{}, err
	}
	catalog, err := DiscoverCLISkills(model.state.Workspace, instructions.ProjectRoot, model.getenv)
	if err != nil {
		return contextReport{}, err
	}
	base := contextbuilder.NewBuilder()
	basePrompt := basePromptText(base)
	withSkills := contextbuilder.NewBuilder(catalog.ModelSkills(nil)...)
	skillTokens := max(0, estimatedTokens(basePromptText(withSkills))-estimatedTokens(basePrompt))
	instructionText := instructions.Prompt()
	coreTokens := estimatedTokens(basePrompt) + estimatedTokens(cliSystemPrompt(model.state.Workspace, "", model.config.Current().SystemPrompt))
	capacity, source := model.contextCapacity()
	report := contextReport{capacity: capacity, capacitySource: source, parts: []contextPart{
		{label: "Core prompt", tokens: coreTokens},
		{label: "Instructions", tokens: estimatedTokens(instructionText)},
		{label: "Skills index", tokens: skillTokens},
		{label: "Tool definitions"},
		{label: "Conversation"},
		{label: "Tool activity"},
	}}
	if model.runtime != nil {
		for _, definition := range model.runtime.options.Tools.StaticDefinitions() {
			encoded, err := json.Marshal(definition.Tool)
			if err != nil {
				return contextReport{}, fmt.Errorf("measure tool %s: %w", definition.Tool.Name, err)
			}
			report.parts[3].tokens += estimatedTokens(string(encoded))
		}
	}
	if model.state.Current.SessionID == "" {
		return report, nil
	}
	latestOperations := make(map[operation.ID]operation.Operation)
	latestErrors := make(map[string]string)
	after := sessionstore.BeforeFirst
	for {
		page, err := model.state.Store.Items(ctx, session.ID(model.state.Current.SessionID), after, 256)
		if err != nil {
			return contextReport{}, fmt.Errorf("read session context: %w", err)
		}
		for _, item := range page.Items {
			report.addItem(item, latestOperations, latestErrors)
		}
		if !page.More {
			break
		}
		if page.NextAfter <= after {
			return contextReport{}, fmt.Errorf("session context did not advance after %d", after)
		}
		after = page.NextAfter
	}
	for _, message := range latestErrors {
		report.parts[5].tokens += estimatedTokens(message)
	}
	for _, value := range latestOperations {
		report.parts[5].tokens += (storedToolOutputBytes(value) + 3) / 4
	}
	return report, nil
}

func basePromptText(builder contextbuilder.Builder) string {
	result, err := builder.Build()
	if err != nil || len(result.Request.Input) == 0 {
		return ""
	}
	message, _ := result.Request.Input[0].Data.(llm.Message)
	return message.Text
}

func (report *contextReport) addItem(item sessionstore.Item, operations map[operation.ID]operation.Operation, errors map[string]string) {
	switch item.Kind {
	case sessionstore.ItemInput:
		input, okay := item.Data.(inbox.Input)
		if !okay || input.Kind != inbox.InputExternal {
			return
		}
		var text string
		if json.Unmarshal(input.Payload, &text) == nil {
			report.parts[4].tokens += estimatedTokens(text)
		}
	case sessionstore.ItemModelResponse:
		value, okay := item.Data.(sessionstore.ModelResponse)
		if !okay {
			return
		}
		report.requests++
		usage := value.Response.Usage
		report.inputTokens += usage.InputTokens
		report.outputTokens += usage.OutputTokens
		report.cachedTokens += usage.CachedInputTokens
		report.reasoningTokens += usage.ReasoningTokens
		report.usageReported = report.usageReported || usage.InputTokens != 0 || usage.OutputTokens != 0
		for _, output := range value.Response.Output {
			switch output.Type {
			case llm.ItemMessage:
				if message, okay := output.Data.(llm.Message); okay {
					report.parts[4].tokens += estimatedTokens(message.Text)
				}
			case llm.ItemToolCall:
				if call, okay := output.Data.(llm.ToolCall); okay {
					report.parts[5].tokens += estimatedTokens(call.Name) + estimatedTokens(call.Arguments)
				}
			case llm.ItemReasoning:
				if reasoning, okay := output.Data.(llm.Reasoning); okay {
					report.parts[4].tokens += estimatedTokens(strings.Join(reasoning.Summary, "\n"))
					if len(reasoning.Raw) != 0 {
						report.opaqueItems++
					}
				}
			}
		}
	case sessionstore.ItemToolCallStatus:
		status, okay := item.Data.(sessionstore.ToolCallStatus)
		if !okay {
			return
		}
		errors[string(status.TurnID)+"/"+status.CallID] = status.Status.Error
		for _, value := range status.Operations {
			operations[value.ID] = value
		}
	}
}

func storedToolOutputBytes(value operation.Operation) int {
	switch value.Type {
	case operation.TypeShell:
		state, err := operation.DecodeShellState(value)
		if err != nil {
			return 0
		}
		if state.Result != nil {
			return len(state.Result.Out) + len(state.Result.Err) + len(state.TerminalError)
		}
		return len(state.InlineOut) + len(state.InlineErr) + len(state.TerminalError)
	case operation.TypeValue:
		value, err := operation.DecodeValue(value)
		if err == nil {
			return len(value)
		}
		return 0
	case operation.TypeRemoteJob:
		state, err := operation.DecodeRemoteJobState(value)
		if err != nil {
			return 0
		}
		return len(state.TerminalResult) + len(state.TerminalError)
	default:
		return 0
	}
}
