package responsesapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
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
	if requests != 1 {
		t.Fatalf("provider rejection made %d attempts, want 1", requests)
	}
}
