package workflow

import "testing"

func TestValidateSystemPromptAppend(t *testing.T) {
	for _, test := range []struct {
		kind, field string
		value       any
		valid       bool
	}{
		{"agent", "system_prompt_append", "Additional guidance", true},
		{"agent", "system_prompt_append", false, false},
		{"command", "system_prompt_append", "text", false},
		{"repeat_check", "repair_system_prompt_append", "Repair guidance", true},
		{"repeat_check", "repair_system_prompt_append", nil, false},
		{"agent", "repair_system_prompt_append", "text", false},
	} {
		t.Run(test.kind+"/"+test.field, func(t *testing.T) {
			spec := map[string]any{test.field: test.value}
			if test.kind == "repeat_check" {
				spec["max_repairs"] = float64(2)
			}
			err := Validate(Graph{Version: 1, Name: "prompts", Steps: []Step{{ID: "step", Kind: test.kind, Spec: spec}}})
			if (err == nil) != test.valid {
				t.Fatalf("Validate=%v valid=%v", err, test.valid)
			}
		})
	}
}
