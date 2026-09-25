package repl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func workflowDetailText(value string) string {
	value = strings.ReplaceAll(sanitizeTerminal(value), "\t", "  ")
	runes := []rune(value)
	if len(runes) > 16000 {
		value = string(runes[:16000]) + "\n… preview truncated"
	}
	return value
}

func (model *uiModel) workflowDetails(width int) []string {
	panel := model.workflow
	if len(panel.graph.Steps) == 0 {
		return nil
	}
	step := panel.graph.Steps[min(max(0, panel.selected), len(panel.graph.Steps)-1)]
	node := panel.state[step.ID]
	palette := model.palette()
	_, status := workflowNodeLabel(step, panel.state, panel.loading)
	lines := []string{model.ink(workflowFlat(step.ID), palette.lilac, true), model.ink(workflowFlat(step.Kind)+" · Status: "+status, palette.muted, false)}
	add := func(label, text string) {
		if text != "" {
			lines = append(lines, "", model.ink(label, palette.lilac, true))
			lines = append(lines, strings.Split(workflowDetailText(text), "\n")...)
		}
	}
	dependencies := "none"
	if len(step.Needs) > 0 {
		dependencies = strings.Join(step.Needs, ", ")
	}
	lines = append(lines, "Requires: "+workflowFlat(dependencies))
	if node.Phase != "" {
		lines = append(lines, "Phase: "+workflowFlat(node.Phase))
	}
	workspace := node.Workspace
	if workspace == "" {
		id, _ := step.Spec["workspace"].(string)
		workspace = panel.state[id].Workspace
		if workspace == "" {
			workspace = id
		}
	}
	if workspace != "" {
		lines = append(lines, "Workspace: "+workflowFlat(workspace))
	}
	raw := func(label string, value any) {
		if value == nil {
			return
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			add(label, err.Error())
			return
		}
		lines = append(lines, "", model.ink(label, palette.lilac, true))
		// Sanitize source before adding our own trusted ANSI syntax colors.
		for _, line := range strings.Split(workflowDetailText(string(data)), "\n") {
			lines = append(lines, model.workflowJSONLine(line))
		}
	}
	if panel.rawDetails {
		lines = append(lines, model.ink("RAW JSON · D returns to task details", palette.accent, false))
		raw("DEFINITION", step)
		raw("INPUTS", node.Inputs)
		raw("OUTPUT", node.Output)
		if len(node.Reconciliations) > 0 {
			raw("RECOVERY DECISIONS", node.Reconciliations)
		}
	} else if panel.fullPrompt {
		lines = append(lines, "", model.ink("FULL PROMPT", palette.accent, true))
		if snapshot, available := panel.prompts[step.ID]; available {
			lines = append(lines, model.ink("Captured for this attempt · system and user messages", palette.muted, false))
			addFull := func(label, text string) {
				lines = append(lines, "", model.ink(label, palette.lilac, true))
				if text == "" {
					text = "(empty message)"
				}
				// Actual request contents intentionally bypass the preview size cap.
				text = strings.ReplaceAll(sanitizeTerminal(text), "\t", "  ")
				lines = append(lines, strings.Split(text, "\n")...)
			}
			addFull("SYSTEM MESSAGE", snapshot.System)
			addFull("USER MESSAGE", snapshot.User)
		} else {
			add("NOT CAPTURED", "Full prompt becomes available when this agent starts.")
			for _, field := range []struct{ key, label string }{
				{"prompt", "AUTHORED PROMPT"},
				{"system_prompt_append", "SYSTEM PROMPT ADDITION"},
				{"repair_prompt", "AUTHORED REPAIR PROMPT"},
				{"repair_system_prompt_append", "REPAIR SYSTEM PROMPT ADDITION"},
			} {
				value, _ := step.Spec[field.key].(string)
				add(field.label, value)
			}
		}
	} else {
		prompt, _ := step.Spec["prompt"].(string)
		add("PROMPT", prompt)
		addition, _ := step.Spec["system_prompt_append"].(string)
		add("SYSTEM PROMPT ADDITION", addition)
		repairAddition, _ := step.Spec["repair_system_prompt_append"].(string)
		add("REPAIR SYSTEM PROMPT ADDITION", repairAddition)
		repair, _ := step.Spec["repair_prompt"].(string)
		add("REPAIR PROMPT", repair)
		if base, ok := step.Spec["base"].(string); ok {
			add("BASE REVISION", base)
		}
		if limit, ok := step.Spec["max_repairs"]; ok {
			add("REPAIR BUDGET", fmt.Sprintf("%d used · %v allowed", node.Repairs, limit))
		}
		if skills, ok := step.Spec["repair_skills"]; ok {
			add("REPAIR SKILLS", workflowHumanValue(skills, 0))
		}
		if command, ok := step.Spec["argv"]; ok {
			add("COMMAND", workflowHumanValue(command, 0))
		}
		if skills, ok := step.Spec["skills"]; ok {
			add("SKILLS", workflowHumanValue(skills, 0))
		}
		if node.Inputs != nil {
			add("INPUTS", workflowHumanValue(node.Inputs, 0))
		} else if inputs, ok := step.Spec["inputs"]; ok {
			add("INPUTS · awaiting dependencies", workflowHumanValue(inputs, 0))
		}
		if node.Output != nil {
			add("OUTPUT", workflowHumanValue(node.Output, 0))
		}
		if schema, ok := step.Spec["output_schema"].(map[string]any); ok {
			add("EXPECTED RESULT", workflowSchemaSummary(schema))
		}
		if step.When != nil {
			add("RUN WHEN", workflowHumanValue(step.When, 0))
		}
		for _, record := range node.Reconciliations {
			add("RECOVERY · "+strings.ToUpper(record.Action), record.Reason)
		}
	}
	if len(node.History) > 0 {
		add("HISTORY", strings.Join(node.History, "\n"))
	}
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, max(1, width), true), "\n")...)
	}
	// A two-cell glyph cannot fit a one-cell pane, even after wrapping.
	for index, line := range wrapped {
		wrapped[index] = ansi.Truncate(line, max(1, width), "…")
	}
	return wrapped
}

// workflowHumanValue turns structured values into labeled text without changing
// persisted data. JSON normalization preserves numbers and supports typed slices.
func workflowHumanValue(value any, depth int) string {
	if depth > 6 {
		return "… open raw details for nested content"
	}
	switch value := value.(type) {
	case nil:
		return "none"
	case string:
		return value
	case json.Number:
		return value.String()
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var rows []string
		for _, key := range keys {
			content := workflowHumanValue(value[key], depth+1)
			if strings.Contains(content, "\n") {
				rows = append(rows, key+":\n  "+strings.ReplaceAll(content, "\n", "\n  "))
			} else {
				rows = append(rows, key+": "+content)
			}
		}
		if len(rows) == 0 {
			return "(empty object)"
		}
		return strings.Join(rows, "\n")
	case []any:
		var rows []string
		for _, entry := range value {
			rows = append(rows, "• "+strings.ReplaceAll(workflowHumanValue(entry, depth+1), "\n", "\n  "))
		}
		if len(rows) == 0 {
			return "(empty list)"
		}
		return strings.Join(rows, "\n")
	case bool:
		return fmt.Sprint(value)
	case float64, float32, int, int64, int32, uint, uint64:
		return fmt.Sprint(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return "(unavailable)"
		}
		var normalized any
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		if decoder.Decode(&normalized) != nil {
			return "(unavailable)"
		}
		return workflowHumanValue(normalized, depth+1)
	}
}

func workflowSchemaSummary(schema map[string]any) string {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return workflowSchemaType(schema)
	}
	required := map[string]bool{}
	if values, ok := schema["required"].([]any); ok {
		for _, value := range values {
			if key, ok := value.(string); ok {
				required[key] = true
			}
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var rows []string
	for _, key := range keys {
		field, _ := properties[key].(map[string]any)
		suffix := "optional"
		if required[key] {
			suffix = "required"
		}
		row := key + " · " + workflowSchemaType(field) + " · " + suffix
		if description, ok := field["description"].(string); ok && description != "" {
			row += "\n  " + description
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}
func workflowSchemaType(schema map[string]any) string {
	if ref, ok := schema["$ref"].(string); ok {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	if choices, ok := schema["enum"].([]any); ok {
		var values []string
		for _, value := range choices {
			values = append(values, fmt.Sprint(value))
		}
		return strings.Join(values, " | ")
	}
	if choices, ok := schema["anyOf"].([]any); ok {
		var values []string
		for _, value := range choices {
			field, _ := value.(map[string]any)
			values = append(values, workflowSchemaType(field))
		}
		return strings.Join(values, " or ")
	}
	kind, _ := schema["type"].(string)
	if kind == "array" {
		item, _ := schema["items"].(map[string]any)
		return "list of " + workflowSchemaType(item)
	}
	if kind == "" {
		return "value"
	}
	return kind
}

var workflowJSONTokens = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|\b(?:true|false|null)\b`)

func (model *uiModel) workflowJSONLine(line string) string {
	if model.noColor {
		return line
	}
	palette := model.palette()
	var result strings.Builder
	end := 0
	for _, span := range workflowJSONTokens.FindAllStringIndex(line, -1) {
		result.WriteString(line[end:span[0]])
		token := line[span[0]:span[1]]
		color := palette.accent
		if strings.HasPrefix(token, "\"") {
			color = palette.text
			if strings.HasPrefix(strings.TrimSpace(line[span[1]:]), ":") {
				color = palette.lilac
			}
		}
		if token == "null" {
			color = palette.muted
		}
		result.WriteString(model.ink(token, color, false))
		end = span[1]
	}
	result.WriteString(line[end:])
	return result.String()
}
