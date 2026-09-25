package workflow

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
)

func validSkill(value any) error {
	if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
		return nil
	}
	if call, ok := value.(map[string]any); ok && len(call) == 2 {
		name, n := call["name"].(string)
		_, a := call["arguments"].(string)
		if n && a && strings.TrimSpace(name) != "" {
			return nil
		}
	}
	return fmt.Errorf("skill requires a name or {name, arguments} descriptor")
}
func outputReference(value map[string]any) (string, []any, error) {
	r, ok := value["$output"].(map[string]any)
	if !ok || len(value) != 1 || len(r) != 2 {
		return "", nil, fmt.Errorf("malformed output reference")
	}
	name, ok := r["step"].(string)
	path, p := r["path"].([]any)
	if !ok || !p {
		return "", nil, fmt.Errorf("reference needs a step and path")
	}
	return name, path, nil
}
func validateRefs(value any, needs []string, steps map[string]Step) error {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["$output"]; ok {
			id, path, err := outputReference(v)
			if err != nil {
				return err
			}
			found := false
			for _, d := range needs {
				if d == id {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("reference %s is not a dependency", id)
			}
			_, err = schemaPath(steps[id].Spec["output_schema"], path)
			return err
		}
		for _, child := range v {
			if err := validateRefs(child, needs, steps); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := validateRefs(child, needs, steps); err != nil {
				return err
			}
		}
	}
	return nil
}
func schemaPath(root any, path []any) (any, error) {
	if root == nil {
		return nil, fmt.Errorf("output reference requires a structured source")
	}
	current := root
	for _, part := range path {
		s, err := dereferenceSchema(current, root)
		if err != nil {
			return nil, err
		}
		switch p := part.(type) {
		case string:
			props, _ := s["properties"].(map[string]any)
			child, ok := props[p]
			if !ok {
				child, ok = s["additionalProperties"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("unknown output field %q", p)
				}
			}
			current = child
		case float64:
			if p < 0 || p != float64(int(p)) || s["type"] != "array" {
				return nil, fmt.Errorf("invalid output array index")
			}
			if tuple, ok := s["items"].([]any); ok {
				if p >= float64(len(tuple)) {
					return nil, fmt.Errorf("output tuple index outside schema")
				}
				current = tuple[int(p)]
			} else {
				current = s["items"]
			}
		default:
			return nil, fmt.Errorf("path needs field names or nonnegative indexes")
		}
	}
	return current, nil
}
func dereferenceSchema(value, root any) (map[string]any, error) {
	for depth := 0; depth < 32; depth++ {
		s, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported output schema path")
		}
		if ref, ok := s["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/") {
				return nil, fmt.Errorf("only local schema references are supported")
			}
			decoded, err := url.PathUnescape(strings.TrimPrefix(ref, "#/"))
			if err != nil {
				return nil, err
			}
			value = root
			for _, key := range strings.Split(decoded, "/") {
				key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
				m, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid schema reference")
				}
				value = m[key]
			}
			continue
		}
		if variants, ok := s["allOf"].([]any); ok {
			if len(variants) != 1 {
				return nil, fmt.Errorf("ambiguous allOf field reference")
			}
			value = variants[0]
			continue
		}
		if variants, ok := s["anyOf"].([]any); ok {
			var candidates []any
			for _, v := range variants {
				m, _ := v.(map[string]any)
				if m["type"] != "null" {
					candidates = append(candidates, v)
				}
			}
			if len(candidates) != 1 {
				return nil, fmt.Errorf("ambiguous union field reference")
			}
			value = candidates[0]
			continue
		}
		return s, nil
	}
	return nil, fmt.Errorf("schema reference depth exceeded")
}
func fieldValue(value any, path []any) (any, error) {
	for _, part := range path {
		switch p := part.(type) {
		case string:
			m, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("output value is not an object at %s", p)
			}
			v, ok := m[p]
			if !ok {
				return nil, fmt.Errorf("output field %s is missing", p)
			}
			value = v
		case float64:
			a, ok := value.([]any)
			if !ok || p < 0 || p != float64(int(p)) || p >= float64(len(a)) {
				return nil, fmt.Errorf("output array index unavailable")
			}
			value = a[int(p)]
		default:
			return nil, fmt.Errorf("invalid output path")
		}
	}
	return value, nil
}
func resolveInputs(value any, state State) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["$output"]; ok {
			id, path, err := outputReference(v)
			if err != nil {
				return nil, err
			}
			n := state[id]
			if n.Status != "completed" {
				return nil, fmt.Errorf("output %s is not completed", id)
			}
			return fieldValue(n.Output, path)
		}
		result := map[string]any{}
		for key, child := range v {
			r, err := resolveInputs(child, state)
			if err != nil {
				return nil, err
			}
			result[key] = r
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			r, err := resolveInputs(child, state)
			if err != nil {
				return nil, err
			}
			result[i] = r
		}
		return result, nil
	default:
		return value, nil
	}
}
func conditionMatches(c *Condition, state State) (bool, error) {
	if c == nil {
		return true, nil
	}
	n := state[c.Step]
	if n.Status != "completed" {
		return false, nil
	}
	if len(c.Equals) == 0 {
		return n.Outcome == c.Outcome, nil
	}
	actual, err := fieldValue(n.Output, c.Path)
	if err != nil {
		return false, err
	}
	var expected any
	decoder := json.NewDecoder(strings.NewReader(string(c.Equals)))
	decoder.UseNumber()
	if err := decoder.Decode(&expected); err != nil {
		return false, err
	}
	if a, ok := actual.(json.Number); ok {
		b, ok := expected.(json.Number)
		if !ok {
			return false, nil
		}
		x, xok := new(big.Rat).SetString(string(a))
		y, yok := new(big.Rat).SetString(string(b))
		return xok && yok && x.Cmp(y) == 0, nil
	}
	switch v := actual.(type) {
	case nil:
		return expected == nil, nil
	case bool:
		return expected == v, nil
	case string:
		return expected == v, nil
	}
	return false, fmt.Errorf("condition selected a non-scalar output")
}
