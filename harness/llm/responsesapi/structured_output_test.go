package responsesapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func structuredFormat() *llm.OutputFormat {
	return &llm.OutputFormat{
		Name: "review_result", Strict: true,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"approved": map[string]any{"type": "boolean"}},
			"required":   []any{"approved"}, "additionalProperties": false,
		},
	}
}

func TestOutputFormatSurvivesModelRoundTripAndBuilderOnWire(t *testing.T) {
	for _, strict := range []bool{false, true} {
		format := structuredFormat()
		format.Strict = strict
		model := llm.Model{ID: "gpt-test", OutputFormat: format}
		encoded, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}
		var restored llm.Model
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		builder := contextbuilder.NewBuilder()
		builder.SetModel(restored)
		built, err := builder.Build()
		if err != nil {
			t.Fatal(err)
		}
		body, err := requestBody(built.Request, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Text struct {
				Format struct {
					Type   string         `json:"type"`
					Name   string         `json:"name"`
					Schema map[string]any `json:"schema"`
					Strict *bool          `json:"strict"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		got := wire.Text.Format
		if got.Type != "json_schema" || got.Name != format.Name || got.Strict == nil || *got.Strict != strict || !reflect.DeepEqual(got.Schema, format.Schema) {
			t.Fatalf("output format changed on wire: %s", body)
		}
		if _, err := requestBody(built.Request, "", map[string]jsontext.Value{"text": jsontext.Value(`{}`)}); err == nil {
			t.Fatal("extension silently replaced requested output format")
		}
	}
}

func TestUnsetOutputFormatPreservesLegacyRequests(t *testing.T) {
	var model llm.Model
	if err := json.Unmarshal([]byte(`{"ID":"gpt-test","ReasoningEffort":"high"}`), &model); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "OutputFormat") {
		t.Fatalf("nil output format changed persisted representation: %s", encoded)
	}
	body, err := requestBody(llm.Request{Model: model}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["text"]; exists {
		t.Fatalf("legacy request acquired a text format: %s", body)
	}
}

func TestInvalidOutputFormatReturnsEncodingError(t *testing.T) {
	for _, format := range []*llm.OutputFormat{
		{},
		{Name: "invalid name", Schema: map[string]any{}},
		{Name: strings.Repeat("x", 65), Schema: map[string]any{}},
		{Name: "missing_schema"},
		{Name: "bad_schema", Schema: map[string]any{"type": make(chan int)}},
	} {
		body, err := requestBody(llm.Request{Model: llm.Model{OutputFormat: format}}, "", nil)
		if err == nil || body != nil {
			t.Fatalf("invalid format produced request: %s, error: %v", body, err)
		}
	}
}

func TestProviderStructuredOutputRejectionIsNotRetriedAsPlainText(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		var body map[string]any
		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		if _, present := body["text"]; !present {
			t.Error("structured output was silently dropped")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"json_schema unsupported by this model","type":"invalid_request_error"}}`))
	}))
	defer server.Close()
	adapter := newTestAdapter(t, server.URL+"/responses")
	request := validRequest()
	request.Model.OutputFormat = structuredFormat()
	_, err := adapter.Respond(t.Context(), request, llm.RequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "json_schema unsupported") {
		t.Fatalf("provider rejection not surfaced: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("provider rejection made %d attempts, want 1", requests.Load())
	}
}

func TestStructuredOutputCompletedRefusalOnWire(t *testing.T) {
	const item = `{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"I cannot help with that."}]}`
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("item_fallback=%t", fallback), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				output := item
				if fallback {
					// Codex can supply the item only through output_item.done.
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
					output = ""
				}
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", output)
			}))
			defer server.Close()
			adapter := newTestAdapter(t, server.URL)
			request := validRequest()
			request.Model.OutputFormat = structuredFormat()
			got, err := adapter.Respond(t.Context(), request, llm.RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Stop != llm.StopRefused || got.Failure != nil || len(got.Output) != 1 {
				t.Fatalf("response = %#v", got)
			}
			want := llm.Item{ProviderID: "msg-1", Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "I cannot help with that."}}
			if !reflect.DeepEqual(got.Output[0], want) {
				t.Fatalf("output = %#v", got.Output)
			}
		})
	}
}

func TestRefusalStopReasonPreservesOtherTerminalStates(t *testing.T) {
	for _, test := range []struct {
		name, status, extra, content, text string
		stop                               llm.StopReason
		failed                             bool
	}{
		{name: "empty refusal", status: "completed", content: `{"type":"refusal","refusal":""}`, stop: llm.StopRefused},
		{name: "mixed content", status: "completed", content: `{"type":"output_text","text":"prefix "},{"type":"refusal","refusal":"denied"},{"type":"output_text","text":" suffix"}`, text: "prefix denied suffix", stop: llm.StopRefused},
		{name: "ordinary text", status: "completed", content: `{"type":"output_text","text":"I cannot help with that."}`, text: "I cannot help with that.", stop: llm.StopComplete},
		{name: "schema output", status: "completed", content: `{"type":"output_text","text":"{\"approved\":true}"}`, text: `{"approved":true}`, stop: llm.StopComplete},
		{name: "truncated refusal", status: "incomplete", extra: `,"incomplete_details":{"reason":"max_output_tokens"}`, content: `{"type":"refusal","refusal":"partial"}`, text: "partial", stop: llm.StopMaxOutputTokens},
		{name: "filtered refusal", status: "incomplete", extra: `,"incomplete_details":{"reason":"content_filter"}`, content: `{"type":"refusal","refusal":"denied"}`, text: "denied", stop: llm.StopRefused},
		{name: "failed refusal", status: "failed", extra: `,"error":{"code":"server_error","message":"failed"}`, content: `{"type":"refusal","refusal":"denied"}`, text: "denied", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"resp-1","status":%q%s,"output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[%s]}]}`, test.status, test.extra, test.content)
			got, err := decodeResponse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if got.Stop != test.stop || (got.Failure != nil) != test.failed || len(got.Output) != 1 {
				t.Fatalf("response = %#v", got)
			}
			if message, ok := got.Output[0].Data.(llm.Message); !ok || message.Text != test.text {
				t.Fatalf("output = %#v", got.Output)
			}
		})
	}
}
