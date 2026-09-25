package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schemas are self-contained. Never resolve workflow-provided references using
// host files or the network; Pydantic's local definitions remain available.
type localSchemaOnly struct{}

func (localSchemaOnly) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference is unsupported: %s", url)
}

func compileOutputSchema(value any) (*jsonschema.Schema, error) {
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("output schema must be an object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localSchemaOnly{})
	compiler.AssertFormat()
	const location = "https://workflow.invalid/output.json"
	if err := compiler.AddResource(location, value); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func validOutputSchema(value any) error {
	_, err := compileOutputSchema(value)
	return err
}

func validateOutput(value any, raw any, path string) error {
	schema, err := compileOutputSchema(raw)
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func SubmitOutput(g Graph, state State, id, payload string) error {
	n := state[id]
	if n.Status != "running" || n.Phase != "output" {
		return fmt.Errorf("%s is not waiting for structured output", id)
	}
	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("output must be one valid JSON value")
	}
	var result any
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return err
	}
	for _, s := range g.Steps {
		if s.ID != id {
			continue
		}
		if err := validateOutput(result, s.Spec["output_schema"], "$"); err != nil {
			return err
		}
		n.Status = "completed"
		n.Outcome = "passed"
		n.Phase = ""
		n.Output = result
		state[id] = n
		return nil
	}
	return fmt.Errorf("unknown step %s", id)
}
