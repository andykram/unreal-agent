// Throwaway feasibility prototype. All graph execution is simulated.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	python := flag.String("python", "", "directory containing python.wasm and lib")
	script := flag.String("script", "example.py", "Python workflow file")
	skillsModule := flag.String("skills-module", "", "generated_skills.py file")
	jsonOnly := flag.Bool("json", false, "export graph JSON without creating a run")
	auto := flag.Bool("auto", false, "simulate until approval or structured output is required")
	resume := flag.String("resume", "", "resume a stored run without executing Python")
	stateDir := flag.String("state-dir", defaultStateDir(), "directory for durable workflow state")
	retention := flag.Duration("retention", 168*time.Hour, "retention for completed inactive runs")
	flag.Parse()
	if *retention <= 0 {
		return fmt.Errorf("retention must be positive")
	}
	if *resume != "" {
		incompatible := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "script" || f.Name == "skills-module" {
				incompatible = true
			}
		})
		if incompatible {
			return fmt.Errorf("resume uses the saved graph; do not supply script or skills-module")
		}
	}
	var graph Graph
	var err error
	if *resume == "" {
		graph, err = buildGraph(*script, *python, *skillsModule)
		if err != nil {
			return err
		}
		if *jsonOnly {
			return printGraph(graph)
		}
	}
	if strings.TrimSpace(*stateDir) == "" {
		return fmt.Errorf("state-dir must not be empty")
	}
	store, err := openRunStore(filepath.Join(*stateDir, "workflows.sqlite"))
	if err != nil {
		return err
	}
	defer store.Close()
	state := State{}
	runID := *resume
	var revision int64
	if runID != "" {
		graph, state, revision, err = store.Load(runID)
		if err != nil {
			return err
		}
		if graph.ExecutionMode == "live" {
			return fmt.Errorf("live workflow cannot be resumed in the simulator")
		}
		if err = validate(graph); err != nil {
			return fmt.Errorf("saved graph is incompatible: %w", err)
		}
		if *jsonOnly {
			return printGraph(graph)
		}
	} else {
		runID, revision, err = store.Create(graph, state)
		if err != nil {
			return err
		}
	}
	announce := func() {
		fmt.Fprintf(os.Stderr, "Workflow run: %s\nResume: ./run.sh -state-dir %q -resume %s\n", runID, *stateDir, runID)
	}
	announce()
	maintain := func() {
		if _, err := store.Cleanup(*retention, 100, runID); err != nil {
			fmt.Fprintln(os.Stderr, "workflow cleanup:", err)
		}
	}
	maintain()
	lastCleanup := time.Now()
	committed, err := json.Marshal(state)
	if err != nil {
		return err
	}
	checkpoint := func() error {
		settle(graph, state)
		assignExecutionKeys(graph, state, runID)
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if bytes.Equal(encoded, committed) {
			return nil
		}
		revision, err = store.Save(runID, revision, state)
		if err != nil {
			return fmt.Errorf("checkpoint failed; stopping before acknowledgement: %w", err)
		}
		committed = encoded
		if time.Since(lastCleanup) >= time.Minute {
			maintain()
			lastCleanup = time.Now()
		}
		return nil
	}
	feedback := ""
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	terminalInfo, _ := os.Stdin.Stat()
	interactive := terminalInfo != nil && terminalInfo.Mode()&os.ModeCharDevice != 0 && !*auto
	for {
		if err := checkpoint(); err != nil {
			return err
		}
		if interactive {
			fmt.Print("\033[2J\033[H")
		}
		fmt.Printf("Run %s · checkpoint %d\n", runID, revision)
		render(graph, state)
		if feedback != "" {
			fmt.Println(feedback)
			feedback = ""
		}
		if *auto {
			if !advance(graph, state, true) {
				return nil
			}
			continue
		}
		fmt.Print("\n[n] next  [a] approve  [o ID JSON] output  [p ID] pass  [b ID] bad check  [f ID] execution error  [r] new run  [q] save and exit\n> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		raw := strings.TrimSpace(scanner.Text())
		input := strings.Fields(raw)
		if len(input) == 0 {
			continue
		}
		switch input[0] {
		case "o":
			if len(input) >= 3 {
				payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(raw, "o")), input[1]))
				if err := submitOutput(graph, state, input[1], payload); err != nil {
					feedback = err.Error()
				}
			} else {
				feedback = "usage: o ID JSON"
			}
		case "q":
			return nil
		case "n":
			advance(graph, state, false)
		case "a":
			var readyApprovals []string
			for _, s := range graph.Steps {
				if s.Kind == "approval" && ready(s, state) {
					readyApprovals = append(readyApprovals, s.ID)
				}
			}
			for _, id := range readyApprovals {
				state[id] = Node{Status: "completed", Outcome: "approved"}
			}
		case "r":
			state = State{}
			runID, revision, err = store.Create(graph, state)
			if err != nil {
				return err
			}
			committed, err = json.Marshal(state)
			if err != nil {
				return err
			}
			announce()
		case "p", "b":
			if len(input) == 2 {
				finishCheck(graph, state, input[1], input[0] == "p")
			}
		case "f":
			if len(input) == 2 {
				for _, s := range graph.Steps {
					if s.ID == input[1] && (ready(s, state) || state[s.ID].Status == "running") {
						node := state[s.ID]
						node.Status = "failed"
						node.History = append(node.History, "execution error during "+node.Phase)
						state[s.ID] = node
					}
				}
			}
		}
	}
}
func defaultStateDir() string {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		return filepath.Join(root, "unreal-agent", "workflows")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "unreal-agent", "workflows")
}
func printGraph(graph Graph) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(graph)
}
func buildGraph(script, python, skillsModule string) (Graph, error) {
	start := time.Now()
	graph, err := workflow.Compile(context.Background(), script, python, skillsModule)
	if err != nil {
		return Graph{}, err
	}
	fmt.Fprintf(os.Stderr, "CPython %s · %s · compile + export %s · cgo disabled by run.sh\n", strings.SplitN(graph.Python, " ", 2)[0], graph.Platform, time.Since(start).Round(time.Millisecond))
	return graph, nil
}
