package repl

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func TestTerminalTitleIsSingleLineAndValidUTF8(t *testing.T) {
	title := windowTitle("/tmp/work\nspace", strings.Repeat("🌿", 100)+"\x1b]2;injected\a")
	if strings.ContainsAny(title, "\n\t\x1b\a") || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 160 {
		t.Fatalf("unsafe terminal title %q", title)
	}
}

func TestCompletedMarkdownClassificationIncludesTables(t *testing.T) {
	if !looksLikeMarkdown("| A | B |\n| - | - |\n| 1 | 2 |") {
		t.Fatal("table was treated as plain text")
	}
	if looksLikeMarkdown("A plain sentence with | punctuation.") {
		t.Fatal("plain text was treated as structured Markdown")
	}
}

func TestCompletedToolOutputIsBoundedAndPointsToFullFile(t *testing.T) {
	state := operation.ShellState{Result: &operation.ShellResult{Out: strings.Repeat("x", 9000), ExitCode: 2}, OutPath: "/tmp/full-output", OutTruncated: true}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	current := operation.Operation{Type: operation.TypeShell, Version: operation.VersionShell, Status: operation.StatusCompleted, MaxOutputLength: operation.DefaultMaxOutputLength, State: encoded}
	view := renderToolOperation(current)
	if !strings.Contains(view, "exit 2") || !strings.Contains(view, "/tmp/full-output") || strings.Contains(view, strings.Repeat("x", 9000)) {
		t.Fatalf("tool view = %q", view)
	}
	call := briefToolCall(llm.ToolCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`})
	if call != "Bash: go test ./..." {
		t.Fatalf("tool call = %q", call)
	}
}
