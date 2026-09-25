package repl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/workflow"
)

func TestWorkflowAgentApprovalRoutesThroughWorkflowGate(t *testing.T) {
	t.Run("ask_each", func(t *testing.T) { testWorkflowAgentApproval(t, false) })
	t.Run("approve_all", func(t *testing.T) { testWorkflowAgentApproval(t, true) })
}

func testWorkflowAgentApproval(t *testing.T, approveAll bool) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil || !strings.Contains(string(body), "BASE WORKFLOW SYSTEM") || !strings.Contains(string(body), "APPENDED WORKFLOW RULE") {
			t.Error("request lost configured or appended system prompt")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"tool","status":"completed","output":[{"type":"function_call","call_id":"call","name":"Bash","arguments":"{\"command\":\"printf approved > marker\"}"}]}}`+"\n\n")
		} else {
			fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"final","status":"completed","output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}}`+"\n\n")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	config := DefaultConfig()
	config.Mode = "edit"
	config.SystemPrompt = "BASE WORKFLOW SYSTEM"
	config.Model.Provider = "openai"
	config.Model.ID = "fixture"
	config.Model.MaxAttempts = 1
	config.Providers["openai"] = ProviderConfig{BaseURL: server.URL}
	getenv := func(key string) string {
		switch key {
		case "HOME":
			return root
		case "SHELL":
			return "/bin/sh"
		case "UNREAL_HARNESS_LLM_API_KEY":
			return "fixture"
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	settings := &ConfigStore{active: config}
	executor := &replWorkflowExecutor{directory: t.TempDir(), config: settings, getenv: getenv, notices: make(chan workflowAgentNotice, 16), approvals: &approvalGate{config: settings, ctx: ctx}}
	if approveAll {
		executor.approvals.AllowAll()
	}
	panel := &workflowPanel{executor: executor, loading: true, visible: true}
	model := &uiModel{ctx: ctx, workflow: panel, width: 100, height: 30, config: settings}
	request := workflow.ExecutionRequest{RunID: "prompt-run", ExternalKey: "prompt-key", Node: workflow.Node{Phase: "execute"}, State: workflow.State{"workspace": {Workspace: root}}, Step: workflow.Step{ID: "agent", Kind: "agent", Spec: map[string]any{"prompt": "Execute command", "workspace": "workspace", "system_prompt_append": "APPENDED WORKFLOW RULE"}}}
	done := make(chan error, 1)
	go func() {
		_, err := executor.Execute(ctx, request)
		done <- err
	}()
	if !approveAll {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			model.advanceWorkflowTick(workflowTickMsg{panel: panel})
			if panel.agent != nil && panel.agent.options.Approvals.Pending() != nil {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("agent finished before approval: %v", err)
			default:
			}
			time.Sleep(time.Millisecond)
		}
		if panel.agent == nil || panel.agent.options.Approvals.Pending() == nil {
			t.Fatal("agent did not request approval")
		}
		if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
			t.Fatal("tool ran before approval", err)
		}
		if !strings.Contains(model.View().Content, "printf approved") {
			t.Fatal("approval missing from workflow view", model.View().Content)
		}
		pending := executor.approvals.Pending()
		if pending == nil {
			t.Fatal("agent approval is isolated from workflow gate")
		}
		executor.approvals.Decide(pending, true)

	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("approved agent did not complete")
	}
	snapshots := loadWorkflowPromptSnapshots(executor.directory, request.RunID, workflow.State{"agent": {ExternalKey: request.ExternalKey}})
	snapshot, ok := snapshots["agent"]
	if !ok || !strings.Contains(snapshot.System, "BASE WORKFLOW SYSTEM") || !strings.Contains(snapshot.System, "APPENDED WORKFLOW RULE") || !strings.Contains(snapshot.User, "Execute command") {
		t.Fatal("captured prompts not restored from receipt")
	}
	value, err := os.ReadFile(filepath.Join(root, "marker"))
	if err != nil || string(value) != "approved" || calls.Load() != 2 {
		t.Fatal("approved tool roundtrip failed", string(value), err, calls.Load())
	}
}
