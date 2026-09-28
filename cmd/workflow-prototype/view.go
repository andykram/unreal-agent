package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func render(g Graph, state State) {
	fmt.Printf("\nPROTOTYPE · %s · SIMULATED execution\n", g.Name)
	for _, s := range g.Steps {
		n := state[s.ID]
		status := n.Status
		if status == "" {
			status = "blocked"
			if ready(s, state) {
				status = "ready"
				if s.Kind == "approval" {
					status = "awaiting approval"
				}
			}
		}
		if n.Outcome != "" {
			status += "/" + n.Outcome
		}
		if n.Phase != "" {
			status += "/" + n.Phase
		}
		fmt.Printf("  %-22s %-12s %-12s ← [%s]\n", status, s.ID, s.Kind, strings.Join(s.Needs, ", "))
		if s.When != nil {
			if len(s.When.Equals) > 0 {
				fmt.Printf("    when %s%v = %s\n", s.When.Step, s.When.Path, s.When.Equals)
			} else {
				fmt.Printf("    when %s outcome = %s\n", s.When.Step, s.When.Outcome)
			}
		}
		if n.Inputs != nil {
			inputs, _ := json.Marshal(n.Inputs)
			fmt.Printf("    inputs %s\n", inputs)
		}
		if n.Status == "failed" {
			fmt.Printf("    error %s\n", strings.Join(n.History, "; "))
		}
		if n.Output != nil {
			result, _ := json.Marshal(n.Output)
			fmt.Printf("    output %s\n", result)
		}
		details, _ := json.Marshal(s.Spec)
		fmt.Printf("    %s\n", details)
		if s.Kind == "repeat_check" {
			fmt.Printf("    repairs %d/%v · %s\n", n.Repairs, s.Spec["max_repairs"], strings.Join(n.History, " → "))
		}
	}
}
