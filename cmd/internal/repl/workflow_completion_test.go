package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func workflowFixture(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately invalid Python: discovery must only inspect files.
	if err := os.WriteFile(path, []byte("not valid Python !\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveWorkflowScriptNamesPathsAndAmbiguity(t *testing.T) {
	root := t.TempDir()
	registered := workflowFixture(t, root, ".agents/workflows/release.py")
	spaced := workflowFixture(t, root, "scripts/ship release.py")
	for _, argument := range []string{"release", "release.py", ".agents/workflows/release.py", registered} {
		got, err := resolveWorkflowScript(root, argument)
		if err != nil || got != registered {
			t.Fatalf("resolve %q = %q, %v", argument, got, err)
		}
	}
	for _, argument := range []string{" scripts/ship release.py ", strconv.Quote(spaced)} {
		got, err := resolveWorkflowScript(root, argument)
		if err != nil || got != spaced {
			t.Fatalf("resolve spaced path %q = %q, %v", argument, got, err)
		}
	}
	workflowFixture(t, root, "workflows/release.py")
	for _, argument := range []string{"release", "release.py"} {
		if _, err := resolveWorkflowScript(root, argument); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous name %q: %v", argument, err)
		}
	}
	if got, err := resolveWorkflowScript(root, registered); err != nil || got != registered {
		t.Fatalf("explicit path became ambiguous: %q, %v", got, err)
	}
	local := workflowFixture(t, root, "release.py")
	if got, err := resolveWorkflowScript(root, "./release.py"); err != nil || got != local {
		t.Fatalf("explicit root path = %q, %v", got, err)
	}
}

func TestWorkflowCompletionPathsDirectoriesAndBound(t *testing.T) {
	root := t.TempDir()
	workflowFixture(t, root, ".agents/workflows/release.py")
	workflowFixture(t, root, "workflows/release.py")
	workflowFixture(t, root, "release.py")
	workflowFixture(t, root, "release.txt")
	workflowFixture(t, root, "scripts with spaces/ship release.py")
	model := &uiModel{state: &SessionState{Workspace: root}}
	choices := completeWorkflowArgument(model, "release")
	if len(choices) != 3 {
		t.Fatalf("workflow choices = %#v", choices)
	}
	resolved := map[string]bool{}
	for _, choice := range choices {
		path, err := resolveWorkflowScript(root, strings.TrimPrefix(choice.Value, "/workflow "))
		if err != nil || resolved[path] {
			t.Fatalf("choice does not resolve distinctly: %#v, %v", choice, err)
		}
		resolved[path] = true
	}
	directories := completeWorkflowArgument(model, "scripts")
	if len(directories) != 1 || directories[0].Value != "/workflow scripts with spaces/" {
		t.Fatalf("directory navigation = %#v", directories)
	}
	for _, query := range []string{"scripts with spaces/ship", filepath.Join(root, "scripts with spaces", "ship"), strconv.Quote("scripts with spaces/ship")} {
		choices := completeWorkflowArgument(model, query)
		if len(choices) != 1 {
			t.Fatalf("spaced prefix %q = %#v", query, choices)
		}
		if _, err := resolveWorkflowScript(root, strings.TrimPrefix(choices[0].Value, "/workflow ")); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 40 {
		workflowFixture(t, root, fmt.Sprintf("workflows/bulk-%02d.py", index))
	}
	if got := len(completeWorkflowArgument(model, "workflows/bulk-")); got != workflowCompletionLimit {
		t.Fatalf("completion bound = %d", got)
	}
}

func TestWorkflowResolutionRejectsMissingAndNonScripts(t *testing.T) {
	root := t.TempDir()
	workflowFixture(t, root, "readme.txt")
	if err := os.Mkdir(filepath.Join(root, "directory.py"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, argument := range []string{"", "missing", "readme.txt", "directory.py", `"unfinished`, "readme.txt --option"} {
		if _, err := resolveWorkflowScript(root, argument); err == nil {
			t.Fatalf("accepted invalid script %q", argument)
		}
	}
	model := &uiModel{state: &SessionState{Workspace: root}}
	if choices := completeWorkflowArgument(model, `"unfinished`); choices != nil {
		t.Fatalf("invalid quoted completion = %#v", choices)
	}
}

func TestWorkflowFileTabCompletionAndColonRouting(t *testing.T) {
	root := t.TempDir()
	workflowFixture(t, root, "scripts/ship release.py")
	workflowFixture(t, root, "root.py")
	workflowFixture(t, root, "workflows/root.py")
	model := &uiModel{state: &SessionState{Workspace: root}}
	for _, prefix := range []string{"/work", "/workflow"} {
		model.suppressCompletion = false
		_ = model.draft.Set(prefix)
		model.refreshCompletion()
		model.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if model.draft.Source() != "/workflow " || model.completion == nil {
			t.Fatalf("colon completion: %q", model.draft.Source())
		}
	}
	_ = model.draft.Set("/workflow:scr")
	model.refreshCompletion()
	model.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.draft.Source() != "/workflow scripts/" || model.completion == nil {
		t.Fatal("directory completion did not continue")
	}
	model.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.draft.Source() != `/workflow "scripts/ship release.py"` || model.completion != nil {
		t.Fatalf("file completion: %q", model.draft.Source())
	}
	for _, raw := range []string{`/workflow:"scripts/ship release.py"`, "/workflow:root.py", "/workflow resume run-id"} {
		name, argument, has := splitCommand(raw)
		if name != "/workflow" || !has || argument == "" {
			t.Fatalf("bad dispatch: %q %q", name, argument)
		}
	}
	// A colon is file selection even when a previous graph exists.
	model.workflow = &workflowPanel{}
	model.command("/workflow:")
	if model.draft.Source() != "/workflow " || model.completion == nil || model.workflow.visible {
		t.Fatal("colon reopened graph instead of completing files")
	}
}

func TestWorkflowColonUsesCWDFileDespiteRegisteredName(t *testing.T) {
	root := t.TempDir()
	workflowFixture(t, root, "review.py")
	workflowFixture(t, root, "workflows/review.py")
	model := &uiModel{ctx: t.Context(), state: &SessionState{Workspace: root}, config: &ConfigStore{active: DefaultConfig()}, getenv: testEnv(root, "")}
	_, command := model.command("/workflow:review.py")
	if command == nil || model.workflow == nil {
		t.Fatalf("cwd file rejected: %s", model.message)
	}
	model.closeWorkflow()
}
