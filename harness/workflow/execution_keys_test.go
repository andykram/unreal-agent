package workflow

import "testing"

func TestExecutorKeyIsStableAndIsolated(t *testing.T) {
	graph := Graph{Steps: []Step{{ID: "work", Kind: "command"}}}
	state := State{"work": {Status: "running"}}
	AssignExecutionKeys(graph, state, "run-one")
	node := state["work"]
	options := ExecutorKeyOptions{Executor: "command", Operation: "launch"}
	key, err := ExecutorKey(node, options)
	if err != nil || key == "" || key == node.AttemptKey {
		t.Fatalf("derived key %q: %v", key, err)
	}
	retry, err := ExecutorKey(node, options)
	if err != nil || retry != key {
		t.Fatalf("retry key changed: %q -> %q: %v", key, retry, err)
	}
	for _, different := range []struct {
		node    Node
		options ExecutorKeyOptions
	}{
		{node, ExecutorKeyOptions{Executor: "agent", Operation: "launch"}},
		{node, ExecutorKeyOptions{Executor: "command", Operation: "poll"}},
		{Node{AttemptKey: executionKey("run-two", "work")}, options},
	} {
		other, err := ExecutorKey(different.node, different.options)
		if err != nil || other == key {
			t.Fatalf("distinct operation reused key %q: %v", other, err)
		}
	}
	for _, invalid := range []struct {
		node    Node
		options ExecutorKeyOptions
	}{
		{Node{}, options},
		{node, ExecutorKeyOptions{Operation: "launch"}},
		{node, ExecutorKeyOptions{Executor: "command"}},
	} {
		if _, err := ExecutorKey(invalid.node, invalid.options); err == nil {
			t.Fatal("accepted incomplete execution identity")
		}
	}
	raw := options
	raw.UnsafeRawKey = "caller-chosen"
	if overridden, err := ExecutorKey(node, raw); err != nil || overridden != raw.UnsafeRawKey {
		t.Fatalf("raw override %q: %v", overridden, err)
	}
}
