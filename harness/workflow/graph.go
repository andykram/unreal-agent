package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Condition struct {
	Step    string          `json:"step"`
	Outcome string          `json:"outcome,omitempty"`
	Path    []any           `json:"path,omitempty"`
	Equals  json.RawMessage `json:"equals,omitempty"`
}
type Step struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind"`
	Needs []string       `json:"needs"`
	Spec  map[string]any `json:"spec"`
	When  *Condition     `json:"when,omitempty"`
}
type Graph struct {
	ExecutionMode   string          `json:"execution_mode,omitempty"`
	Workspace       string          `json:"workspace,omitempty"`
	ExecutionConfig json.RawMessage `json:"execution_config,omitempty"`
	Version         int             `json:"version"`
	Name            string          `json:"name"`
	Python          string          `json:"python"`
	Platform        string          `json:"platform"`
	Steps           []Step          `json:"steps"`
}
type Node struct {
	Reconciliations []ReconciliationRecord `json:",omitempty"`
	Workspace       string                 `json:",omitempty"`
	ExternalKey     string                 `json:",omitempty"`
	DispatchStarted bool                   `json:",omitempty"`
	Status          string
	Outcome         string
	Phase           string
	Repairs         int
	History         []string
	Output          any
	Inputs          any
	IdempotencyKey  string
	AttemptKey      string
	AttemptKeys     []string
}
type State map[string]Node

func Validate(g Graph) error {
	if g.ExecutionMode != "" && g.ExecutionMode != "simulate" && g.ExecutionMode != "live" {
		return fmt.Errorf("unsupported execution mode %q", g.ExecutionMode)
	}
	if g.Version != 1 || g.Name == "" || len(g.Steps) == 0 {
		return fmt.Errorf("expected a named version 1 graph with steps")
	}
	ids := map[string]bool{}
	byID := map[string]Step{}
	for _, s := range g.Steps {
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("empty or duplicate step ID %q", s.ID)
		}
		ids[s.ID] = true
		byID[s.ID] = s
		if schema, exists := s.Spec["output_schema"]; exists {
			if s.Kind != "agent" {
				return fmt.Errorf("%s: output_schema requires an agent", s.ID)
			}
			if err := validOutputSchema(schema); err != nil {
				return fmt.Errorf("%s: %w", s.ID, err)
			}
		}
		for field, kind := range map[string]string{"system_prompt_append": "agent", "repair_system_prompt_append": "repeat_check"} {
			if value, exists := s.Spec[field]; exists {
				if s.Kind != kind {
					return fmt.Errorf("%s: %s requires %s", s.ID, field, kind)
				}
				if _, ok := value.(string); !ok {
					return fmt.Errorf("%s: %s must be a string", s.ID, field)
				}
			}
		}
		for _, field := range []string{"skills", "repair_skills"} {
			if value, exists := s.Spec[field]; exists {
				names, ok := value.([]any)
				if !ok {
					return fmt.Errorf("%s: %s must be an array of names", s.ID, field)
				}
				for _, value := range names {
					if err := validSkill(value); err != nil {
						return fmt.Errorf("%s: %w", s.ID, err)
					}
				}
			}
		}
		switch s.Kind {
		case "worktree", "agent", "command", "approval":
		case "join":
			if len(s.Needs) == 0 || s.When != nil {
				return fmt.Errorf("%s: join needs inputs and cannot have a condition", s.ID)
			}
		case "repeat_check":
			n, ok := s.Spec["max_repairs"].(float64)
			if !ok || n < 0 || n > 10 || n != float64(int(n)) {
				return fmt.Errorf("%s: max_repairs must be an integer from 0 to 10", s.ID)
			}
		default:
			return fmt.Errorf("unknown kind %q", s.Kind)
		}
	}
	for _, s := range g.Steps {
		found := false
		for _, d := range s.Needs {
			if !ids[d] {
				return fmt.Errorf("%s: missing dependency %s", s.ID, d)
			}
			if s.When != nil && d == s.When.Step {
				found = true
			}
		}
		if s.When != nil {
			if !found {
				return fmt.Errorf("%s: condition source must be a dependency", s.ID)
			}
			if len(s.When.Equals) > 0 {
				if s.When.Outcome != "" {
					return fmt.Errorf("%s: use outcome or field equality, not both", s.ID)
				}
				if _, err := schemaPath(byID[s.When.Step].Spec["output_schema"], s.When.Path); err != nil {
					return fmt.Errorf("%s condition: %w", s.ID, err)
				}
				var expected any
				decoder := json.NewDecoder(strings.NewReader(string(s.When.Equals)))
				decoder.UseNumber()
				if err := decoder.Decode(&expected); err != nil {
					return err
				}
				switch expected.(type) {
				case nil, string, bool, json.Number:
				default:
					return fmt.Errorf("conditions require scalar equality")
				}
			} else if byID[s.When.Step].Kind != "repeat_check" || (s.When.Outcome != "passed" && s.When.Outcome != "failed") || len(s.When.Path) > 0 {
				return fmt.Errorf("%s: invalid outcome condition", s.ID)
			}
		}
		if err := validateRefs(s.Spec["inputs"], s.Needs, byID); err != nil {
			return fmt.Errorf("%s inputs: %w", s.ID, err)
		}
	}
	done := map[string]bool{}
	for {
		changed := false
		for _, s := range g.Steps {
			if done[s.ID] {
				continue
			}
			ok := true
			for _, d := range s.Needs {
				if !done[d] {
					ok = false
				}
			}
			if ok {
				done[s.ID] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	if len(done) != len(g.Steps) {
		return fmt.Errorf("workflow contains a dependency cycle")
	}
	return nil
}
func Ready(s Step, state State) bool {
	if state[s.ID].Status != "" {
		return false
	}
	if s.Kind == "join" {
		completed := false
		for _, d := range s.Needs {
			switch state[d].Status {
			case "completed":
				completed = true
			case "skipped":
			default:
				return false
			}
		}
		return completed
	}
	for _, d := range s.Needs {
		if state[d].Status != "completed" {
			return false
		}
	}
	matched, err := conditionMatches(s.When, state)
	return err == nil && matched
}

// Settle propagates unselected branches. Execution errors stay blocked, never skipped.
func Settle(g Graph, state State) {
	for {
		changed := false
		for _, s := range g.Steps {
			if state[s.ID].Status != "" {
				continue
			}
			if s.Kind == "join" {
				allSkipped := len(s.Needs) > 0
				for _, d := range s.Needs {
					if state[d].Status != "skipped" {
						allSkipped = false
					}
				}
				if allSkipped {
					state[s.ID] = Node{Status: "skipped"}
					changed = true
				}
				continue
			}
			skip := false
			for _, d := range s.Needs {
				if state[d].Status == "skipped" {
					skip = true
				}
			}
			if s.When != nil {
				source := state[s.When.Step]
				if source.Status == "completed" && !skip {
					matched, err := conditionMatches(s.When, state)
					if err != nil {
						state[s.ID] = Node{Status: "failed", History: []string{err.Error()}}
						changed = true
						continue
					}
					skip = !matched
				}
			}
			if skip {
				state[s.ID] = Node{Status: "skipped"}
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}
func Advance(g Graph, state State, auto bool) bool {
	var batch []Step
	for _, s := range g.Steps {
		if (Ready(s, state) && s.Kind != "approval") || state[s.ID].Status == "running" {
			batch = append(batch, s)
		}
	}
	changed := false
	for _, s := range batch {
		if state[s.ID].Status == "" {
			inputs, err := resolveInputs(s.Spec["inputs"], state)
			if err != nil {
				state[s.ID] = Node{Status: "failed", History: []string{err.Error()}}
				changed = true
				continue
			}
			if inputs != nil {
				node := state[s.ID]
				node.Inputs = inputs
				state[s.ID] = node
			}
		}
		if s.Kind == "agent" && s.Spec["output_schema"] != nil {
			if state[s.ID].Status == "" {
				node := state[s.ID]
				node.Status = "running"
				node.Phase = "output"
				state[s.ID] = node
				changed = true
			}
			continue
		}
		if s.Kind != "repeat_check" {
			node := state[s.ID]
			node.Status = "completed"
			node.Outcome = "passed"
			state[s.ID] = node
			changed = true
			continue
		}
		n := state[s.ID]
		switch n.Phase {
		case "":
			n.Status = "running"
			n.Phase = "check"
			n.History = append(n.History, "check 1 ready")
			state[s.ID] = n
			changed = true
		case "repair":
			n.Repairs++
			n.Phase = "check"
			n.History = append(n.History, fmt.Sprintf("repair %d completed; check %d ready", n.Repairs, n.Repairs+1))
			state[s.ID] = n
			changed = true
		case "check":
			if auto {
				FinishCheck(g, state, s.ID, true)
				changed = true
			}
		}
	}
	return changed
}
func FinishCheck(g Graph, state State, id string, passed bool) {
	n := state[id]
	if n.Status != "running" || n.Phase != "check" {
		return
	}
	for _, s := range g.Steps {
		if s.ID != id || s.Kind != "repeat_check" {
			continue
		}
		result := "failed"
		if passed {
			result = "passed"
		}
		n.History = append(n.History, fmt.Sprintf("check %d %s", n.Repairs+1, result))
		if passed || n.Repairs >= int(s.Spec["max_repairs"].(float64)) {
			n.Status = "completed"
			n.Outcome = result
			n.Phase = ""
			if !passed {
				n.History = append(n.History, "repair budget exhausted")
			}
		} else {
			n.Phase = "repair"
		}
		state[id] = n
		return
	}
}
