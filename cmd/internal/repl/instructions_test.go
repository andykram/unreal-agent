package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInstructionFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionImportsAndScopedIndex(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, "AGENTS.md"), "root before\n@\"notes with spaces.md\"\n```text\n@missing.md\n```\nroot after\n")
	writeInstructionFixture(t, filepath.Join(root, "notes with spaces.md"), "imported text\n")
	writeInstructionFixture(t, filepath.Join(root, "nested", "AGENTS.md"), "nested rule\n")
	bundle, err := LoadInstructions(t.Context(), root, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	prompt := bundle.Prompt()
	if !strings.Contains(prompt, "root before") || !strings.Contains(prompt, "imported text") || !strings.Contains(prompt, "@missing.md") || !strings.Contains(prompt, "nested/AGENTS.md") {
		t.Fatalf("instruction prompt = %s", prompt)
	}
	if strings.Index(prompt, "root before") > strings.Index(prompt, "imported text") || strings.Index(prompt, "imported text") > strings.Index(prompt, "root after") {
		t.Fatal("imports were not expanded at their source position")
	}
	if strings.Contains(prompt, "nested rule") {
		t.Fatal("nested instructions leaked into global scope")
	}
}

func TestInstructionImportCycleAndMissingImport(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, "AGENTS.md"), "@first.md\n")
	writeInstructionFixture(t, filepath.Join(root, "first.md"), "@second.md\n")
	if _, err := LoadInstructions(t.Context(), root, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "second.md") {
		t.Fatalf("missing import error = %v", err)
	}
	writeInstructionFixture(t, filepath.Join(root, "second.md"), "@first.md\n")
	if _, err := LoadInstructions(t.Context(), root, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "second.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "second.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstructions(t.Context(), root, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("symlink cycle error = %v", err)
	}
}
