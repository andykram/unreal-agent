package coordinator

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestCoordinatorForkStopsWhenChildIsIdle(t *testing.T) {
	for _, parentStage := range []string{"unscheduled", "running", "completed", "legacy completed", "compaction", "compaction response"} {
		for _, childTool := range []bool{false, true} {
			t.Run(fmt.Sprintf("parent=%s/child-tool=%t", parentStage, childTool), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					parent := newStopTestRun(t, 1)
					if parentStage == "unscheduled" {
						parent.store.items = parent.store.items[:2]
					}
					directory := t.TempDir()
					store := persistForkFixture(t, parent, directory)
					previousTurn := session.TurnID("turn-1")
					if parentStage == "completed" || parentStage == "legacy completed" {
						status := parent.store.items[2].Data.(sessionstore.ToolCallStatus)
						status.Operations[0].Status = operation.StatusCompleted
						status.Operations[0].State = jsontext.Value(`{"output":"actual parent result"}`)
						if err := store.SaveOperation(t.Context(), "session-1", status.Operations[0]); err != nil {
							t.Fatal(err)
						}
						if err := store.AppendToolCallStatus(t.Context(), "session-1", status); err != nil {
							t.Fatal(err)
						}
						previousTurn = "parent-final"
						if err := store.AppendTurn(t.Context(), "session-1", session.Turn{ID: previousTurn, PreviousTurnID: "turn-1", Type: session.TurnRegular}); err != nil {
							t.Fatal(err)
						}
						if err := store.AppendModelResponse(t.Context(), "session-1", sessionstore.ModelResponse{TurnID: previousTurn, Response: textResponse("Parent done.")}); err != nil {
							t.Fatal(err)
						}
					}
					compaction := parentStage == "compaction" || parentStage == "compaction response"
					if compaction {
						previousTurn = "compact"
						if err := store.AppendTurn(t.Context(), "session-1", session.Turn{ID: previousTurn, PreviousTurnID: "turn-1", Type: session.TurnCompaction}); err != nil {
							t.Fatal(err)
						}
						if parentStage == "compaction response" {
							if err := store.AppendModelResponse(t.Context(), "session-1", sessionstore.ModelResponse{TurnID: previousTurn, Response: textResponse("Summary")}); err != nil {
								t.Fatal(err)
							}
						}
					}
					if _, err := store.Fork(t.Context(), "child", "session-1", previousTurn); err != nil {
						t.Fatal(err)
					}
					if parentStage == "legacy completed" {
						stripLegacyForkSnapshots(t, filepath.Join(directory, "child.session.jsonl"))
						var err error
						store, err = localfile.New(directory)
						if err != nil {
							t.Fatal(err)
						}
					}
					restored, err := store.Resume(t.Context(), "child")
					if err != nil {
						t.Fatal(err)
					}
					child := newStopTestRun(t, 0)
					child.current.dependencies.SessionID = "child"
					child.current.dependencies.Sessions, child.current.dependencies.Restored = store, restored
					spec, err := operation.NewValueSpec(jsontext.Value(`{"value":1}`))
					if err != nil {
						t.Fatal(err)
					}
					registry := tool.NewRegistry(tool.StaticTranslators{Bash: &submittingTranslator{specs: []operation.Spec{spec}}, ViewImage: forkSnapshotTranslator{}}, tool.BashName, tool.ViewImageName)
					child.current.dependencies.Tools = registry
					child.start(t)
					child.assertRunning(t)
					if len(child.operations.adds) != 0 || len(child.current.state.toolCalls) != 0 {
						t.Fatal("inherited tool call became active child work")
					}
					if child.current.state.currentTurnID != previousTurn {
						t.Fatal("fork lost the parent turn boundary")
					}
					if (child.current.state.currentTurnType == session.TurnCompaction) != compaction || child.current.pendingInputs() != 0 || len(child.calls) != 0 {
						t.Fatal("fork changed the parent turn kind or started inherited work")
					}
					child.input(t, externalEvent(t, 0, "child-user", "hello"), stopInput(t, "child-stop", inbox.StopWhenIdle))
					if child.current.state.currentTurnType != session.TurnRegular {
						t.Fatal("child turn did not establish its own type")
					}
					assertForkPayload(t, child.calls[0].request, parentStage)
					inheritedCall := false
					inheritedResult := false
					for _, item := range child.calls[0].request.Input {
						if item.Type == llm.ItemToolResult && item.Data.(llm.ToolResult).CallID == "call-0" {
							inheritedResult = true
						}
						if item.Type == llm.ItemToolCall && item.Data.(llm.ToolCall).CallID == "call-0" {
							inheritedCall = true
						}
					}
					if !inheritedResult {
						t.Fatal("fork request contains inherited call without a matching result")
					}
					if !inheritedCall {
						t.Fatal("fork lost its parent context")
					}
					if childTool {
						child.respond(t, 0, llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{
							CallID: "child-call", Name: tool.BashName, Arguments: `{}`,
						}}}})
						child.assertRunning(t)
						if len(child.operations.adds) != 1 || len(child.current.state.toolCalls) != 1 {
							t.Fatal("child tool call was not tracked")
						}
						completed := child.operations.adds[0]
						completed.Status = operation.StatusCompleted
						child.operations.updates <- completed
						synctest.Wait()
						synctest.Sleep(2 * slurpIdleTimeout)
						child.assertRunning(t)
						child.respond(t, 1, textResponse("Child tool done."))
					} else {
						child.respond(t, 0, textResponse("Child done."))
					}
					child.assertStopped(t)
					if _, err := store.Fork(t.Context(), "grandchild", "child", child.current.state.currentTurnID); err != nil {
						t.Fatal(err)
					}
					reopened, err := localfile.New(directory)
					if err != nil {
						t.Fatal(err)
					}
					nestedState, err := reopened.Resume(t.Context(), "grandchild")
					if err != nil {
						t.Fatal(err)
					}
					if len(nestedState.Operations) != 0 {
						t.Fatal("nested fork owns inherited operations")
					}
					nested := newStopTestRun(t, 0)
					nested.current.dependencies.SessionID = "grandchild"
					nested.current.dependencies.Sessions, nested.current.dependencies.Restored = reopened, nestedState
					nested.current.dependencies.Tools = registry
					nested.start(t)
					if len(nested.operations.adds) != 0 || len(nested.calls) != 0 {
						t.Fatal("nested replay dispatched inherited work")
					}
					nested.input(t, externalEvent(t, 0, "grandchild-user", "continue"), stopInput(t, "grandchild-stop", inbox.StopWhenIdle))
					assertForkPayload(t, nested.calls[0].request, parentStage)
					nested.respond(t, 0, textResponse("Grandchild done."))
					nested.assertStopped(t)
				})
			})
		}
	}
}

// The fixture returns actual persisted operation payloads, not a constant result.
type forkSnapshotTranslator struct{ testTranslator }

func (forkSnapshotTranslator) TranslateResult(_ string, _ tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	text := "pending"
	for _, op := range operations {
		if op.Status == operation.StatusCompleted {
			var value struct {
				Output string `json:"output"`
			}
			if err := json.Unmarshal(op.State, &value); err != nil {
				return llm.ToolResult{}, err
			}
			text = value.Output
		}
	}
	return llm.ToolResult{Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
}
func assertForkPayload(t *testing.T, request llm.Request, stage string) {
	t.Helper()
	results, actual, unavailable := 0, 0, 0
	for _, item := range request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result := item.Data.(llm.ToolResult)
		if result.CallID != "call-0" {
			continue
		}
		results++
		for _, output := range result.Output {
			if output.Value == "actual parent result" {
				actual++
			}
			if strings.Contains(output.Value, "unavailable") {
				unavailable++
			}
		}
	}
	if stage == "completed" {
		if results != 1 || actual != 1 || unavailable != 0 {
			t.Fatalf("completed inherited result lost or duplicated: results=%d actual=%d unavailable=%d", results, actual, unavailable)
		}
	} else {
		if unavailable != 1 || actual != 0 {
			t.Fatalf("missing or duplicate unavailable result: results=%d actual=%d unavailable=%d", results, actual, unavailable)
		}
		if (stage == "running" || stage == "unscheduled") && results != 1 {
			t.Fatalf("unresolved call has %d outputs, want exactly one", results)
		}
	}
}
func persistForkFixture(t *testing.T, run *stopTestRun, directory string) *localfile.Store {
	t.Helper()
	store, err := localfile.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(t.Context(), "session-1"); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendTurn(t.Context(), "session-1", run.store.items[0].Data.(session.Turn)); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendModelResponse(t.Context(), "session-1", run.store.items[1].Data.(sessionstore.ModelResponse)); err != nil {
		t.Fatal(err)
	}
	for _, item := range run.store.items[2:] {
		if err = store.AppendToolCallStatus(t.Context(), "session-1", item.Data.(sessionstore.ToolCallStatus)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// Old fork logs retained statuses but discarded their operation snapshots.
func stripLegacyForkSnapshots(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["type"] != "item" {
			continue
		}
		item := record["data"].(map[string]any)["Item"].(map[string]any)
		if item["Kind"] != "tool_call_status" {
			continue
		}
		delete(item["Data"].(map[string]any), "Operations")
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(encoded)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
