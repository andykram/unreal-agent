package repl

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestWorkflowCommandRunsInWorkspaceWithExternalKey(t *testing.T) {
	workspace := t.TempDir()
	result, err := runWorkflowCommand(t.Context(), workspace, []string{"/bin/sh", "-c", `printf '%s' "$UNREAL_WORKFLOW_IDEMPOTENCY_KEY"; printf actual > marker`}, "ext-v1-test")
	if err != nil || result.ExitCode != 0 || result.Output != "ext-v1-test" {
		t.Fatalf("result %#v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "marker"))
	if err != nil || string(data) != "actual" {
		t.Fatal("command did not execute in workspace", err)
	}
	result, err = runWorkflowCommand(t.Context(), workspace, []string{"/bin/sh", "-c", "exit 7"}, "ext-v1-test")
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("exit code lost %#v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = runWorkflowCommand(ctx, workspace, []string{"/bin/sh", "-c", "sleep 30"}, "ext-v1-test")
	if err == nil || ctx.Err() == nil {
		t.Fatal("cancellation lost")
	}
}

func TestWorkflowAgentUsesHarnessAndNativeSchema(t *testing.T) {
	requests := make(chan map[string]any, 4)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-tool\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"workflow-tool-call\",\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"printf tool-executed > workflow-tool-marker\\\"}\"}]}}\n\n")
			return
		}
		fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"response-workflow","status":"completed","output":[{"id":"message-workflow","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"count\":9007199254740993}"}]}]}}`+"\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	config := DefaultConfig()
	config.Model.Provider = "openai"
	config.Model.ID = "test-workflow"
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
	executor := &replWorkflowExecutor{directory: t.TempDir(), config: &ConfigStore{active: config}, getenv: getenv, notices: make(chan workflowAgentNotice, 16)}
	schema := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "required": []any{"count"}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := executor.agent(ctx, workflow.ExecutionRequest{Step: workflow.Step{ID: "analyze", Kind: "agent", Spec: map[string]any{"prompt": "Analyze", "output_schema": schema}}, Node: workflow.Node{Inputs: map[string]any{"source": "typed"}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.(map[string]any)["count"] != json.Number("9007199254740993") {
		t.Fatalf("precision lost %#v", result.Output)
	}
	marker, err := os.ReadFile(filepath.Join(root, "workflow-tool-marker"))
	if err != nil || string(marker) != "tool-executed" {
		t.Fatalf("harness tool did not execute: %s %v", marker, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected tool round trip, got %d calls", calls.Load())
	}
	request := <-requests
	format := request["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "workflow_result" {
		t.Fatal("schema not sent", format)
	}
	encoded, _ := json.Marshal(request["input"])
	if !strings.Contains(string(encoded), "typed") {
		t.Fatal("resolved inputs not sent")
	}
	entries, err := os.ReadDir(filepath.Join(executor.directory, "agent-sessions"))
	if err != nil || len(entries) == 0 {
		t.Fatal("no durable harness session", err)
	}
}

func TestWorkflowDirectCommandHonorsEditApproval(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "edit"
	executor := &replWorkflowExecutor{config: &ConfigStore{active: cfg}, approvals: &approvalGate{}, notices: make(chan workflowAgentNotice, 16)}
	done := make(chan error, 1)
	go func() { done <- executor.approve(t.Context(), "touch reviewed") }()
	deadline := time.Now().Add(time.Second)
	for executor.approvals.Pending() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	request := executor.approvals.Pending()
	if request == nil {
		t.Fatal("no approval")
	}
	select {
	case <-done:
		t.Fatal("executed before approval")
	default:
	}
	executor.approvals.Decide(request, false)
	if err := <-done; err == nil {
		t.Fatal("denial lost")
	}
}
