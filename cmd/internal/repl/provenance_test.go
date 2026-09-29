package repl

import (
	"context"
	"encoding/json/jsontext"
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestResponseProvenanceProjectsAcrossProvidersAndResume(t *testing.T) {
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := state.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{ID: "response-1", Output: []llm.Item{
		{ProviderID: "message-1", Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "hello"}},
		{ProviderID: "reasoning-1", Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"encrypted_content":"private"}`)}},
		{ProviderID: "call-1", Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-id", Name: "bash", Arguments: `{}`}},
	}}
	first := newResponseProvenance(state)
	first.record(response, "openai|https://api.openai.com/v1")
	request := llm.Request{Input: append(append([]llm.Item{}, response.Output...), llm.Item{
		Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call-id", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "ok"}}},
	})}
	same := first.project(request, "openai|https://api.openai.com/v1")
	if len(same.Input) != 4 || same.Input[1].ProviderID != "reasoning-1" || same.Input[2].ProviderID != "call-1" {
		t.Fatalf("same-provider projection: %#v", same.Input)
	}
	foreign := first.project(request, "openrouter|https://openrouter.ai/api/v1")
	if len(foreign.Input) != 3 || foreign.Input[0].ProviderID != "" || foreign.Input[1].ProviderID != "" {
		t.Fatalf("foreign-provider projection: %#v", foreign.Input)
	}
	if foreign.Input[1].Data.(llm.ToolCall).CallID != "call-id" || request.Input[0].ProviderID != "message-1" {
		t.Fatal("projection damaged portable tool identity or source request")
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Resume(context.Background(), metadata.SessionID); err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	second := newResponseProvenance(resumed)
	builder := &provenanceBuilder{Builder: contextbuilder.NewBuilder(), provenance: second}
	builder.AddModelResponse(response)
	if got := second.project(request, "openai|https://api.openai.com/v1"); len(got.Input) != 4 {
		t.Fatalf("restored provenance lost: %#v", got.Input)
	}
	unknown := request
	unknown.Input = append([]llm.Item{}, request.Input...)
	unknown.Input[0].ProviderID = "unmapped"
	unknown.Input[1].ProviderID = "unmapped-reasoning"
	if got := second.project(unknown, "openai|https://api.openai.com/v1"); len(got.Input) != 3 || got.Input[0].ProviderID != "" {
		t.Fatalf("unknown origin was not sanitized: %#v", got.Input)
	}
}
