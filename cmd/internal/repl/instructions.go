package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const instructionBudget = 256 << 10
const instructionMaxDepth = 16

type instructionPart struct{ Path, Scope, Text string }

type InstructionBundle struct {
	ProjectRoot string
	Parts       []instructionPart
	Nested      []string
}

func (bundle InstructionBundle) Prompt() string {
	var result strings.Builder
	result.WriteString("Instruction files have the scopes shown below. Follow applicable instructions for each file you touch. Explicit user instructions take priority.\n")
	for _, part := range bundle.Parts {
		fmt.Fprintf(&result, "\n[Instructions from %s; scope: %s]\n%s\n", part.Path, part.Scope, part.Text)
	}
	if len(bundle.Nested) != 0 {
		result.WriteString("\nBefore editing or acting inside a listed subtree, read that subtree's AGENTS.md and its imports. Nested files apply only within their directory scopes:\n")
		for _, path := range bundle.Nested {
			fmt.Fprintf(&result, "- %s (scope: %s)\n", path, filepath.Dir(path))
		}
	}
	return result.String()
}

func projectBoundary(workspace string) string {
	for current := workspace; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		if filepath.Dir(current) == current {
			return workspace
		}
	}
}

type instructionLoader struct {
	home      string
	remaining int
	active    map[string]bool
	seen      map[string]bool
	parts     []instructionPart
}

func LoadInstructions(ctx context.Context, workspace string, getenv func(string) string) (InstructionBundle, error) {
	root := projectBoundary(workspace)
	bundle := InstructionBundle{ProjectRoot: root}
	loader := instructionLoader{home: getenv("HOME"), remaining: instructionBudget, active: map[string]bool{}, seen: map[string]bool{}}
	if loader.home != "" {
		if err := loader.load(filepath.Join(loader.home, ".agents", "AGENTS.md"), "global", true, 0); err != nil {
			return bundle, err
		}
	}
	var ancestors []string
	for current := workspace; ; current = filepath.Dir(current) {
		ancestors = append(ancestors, current)
		if current == root || filepath.Dir(current) == current {
			break
		}
	}
	slices.Reverse(ancestors)
	for _, directory := range ancestors {
		if err := loader.load(filepath.Join(directory, "AGENTS.md"), directory, true, 0); err != nil {
			return bundle, err
		}
	}
	bundle.Parts = loader.parts
	nested, err := nestedInstructions(ctx, root)
	if err != nil {
		return bundle, err
	}
	ancestorPaths := map[string]bool{}
	for _, directory := range ancestors {
		ancestorPaths[filepath.Join(directory, "AGENTS.md")] = true
	}
	for _, path := range nested {
		if !ancestorPaths[path] {
			bundle.Nested = append(bundle.Nested, path)
		}
	}
	return bundle, nil
}

func (loader *instructionLoader) load(path, scope string, optional bool, depth int) error {
	if depth > instructionMaxDepth {
		return fmt.Errorf("instruction import depth exceeds %d at %s", instructionMaxDepth, path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) && optional {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve instruction %s: %w", path, err)
	}
	if loader.active[canonical] {
		return fmt.Errorf("instruction import cycle at %s", path)
	}
	if loader.seen[canonical] {
		return nil
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return fmt.Errorf("read instruction %s: %w", path, err)
	}
	loader.remaining -= len(data)
	if loader.remaining < 0 {
		return fmt.Errorf("instruction files exceed %d bytes at %s", instructionBudget, path)
	}
	loader.active[canonical] = true
	defer delete(loader.active, canonical)
	loader.seen[canonical] = true
	lines := strings.SplitAfter(string(data), "\n")
	var segment strings.Builder
	var fence byte
	var fenceLength int
	flush := func() {
		if segment.Len() != 0 {
			loader.parts = append(loader.parts, instructionPart{Path: canonical, Scope: scope, Text: segment.String()})
			segment.Reset()
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker, count := fenceMarker(trimmed); marker != 0 {
			if fence == 0 {
				fence, fenceLength = marker, count
			} else if marker == fence && count >= fenceLength {
				fence = 0
			}
			segment.WriteString(line)
			continue
		}
		if fence == 0 && strings.HasPrefix(trimmed, "@") {
			importPath, err := parseInstructionImport(trimmed, filepath.Dir(canonical), loader.home)
			if err != nil {
				return fmt.Errorf("instruction %s: %w", canonical, err)
			}
			flush()
			if err := loader.load(importPath, scope, false, depth+1); err != nil {
				return fmt.Errorf("imported by %s: %w", canonical, err)
			}
			continue
		}
		segment.WriteString(line)
	}
	flush()
	return nil
}

func fenceMarker(line string) (byte, int) {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0
	}
	index := 0
	for index < len(line) && line[index] == line[0] {
		index++
	}
	if index < 3 {
		return 0, 0
	}
	return line[0], index
}

func parseInstructionImport(line, directory, home string) (string, error) {
	value := strings.TrimPrefix(line, "@")
	if value == "" {
		return "", errors.New("empty @path import")
	}
	if strings.HasPrefix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted import %q: %w", line, err)
		}
		value = decoded
	} else if strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("quote import path containing spaces: %q", line)
	}
	if strings.HasPrefix(value, "~/") {
		if home == "" {
			return "", errors.New("HOME is required for ~/ imports")
		}
		value = filepath.Join(home, value[2:])
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(directory, value)
	}
	return value, nil
}

func nestedInstructions(ctx context.Context, root string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		output, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "AGENTS.md", ":(glob)**/AGENTS.md").Output()
		if err != nil {
			return nil, fmt.Errorf("list scoped instructions: %w", err)
		}
		var paths []string
		for _, name := range bytes.Split(output, []byte{0}) {
			if len(name) > 0 {
				paths = append(paths, filepath.Join(root, string(name)))
			}
		}
		slices.Sort(paths)
		return slices.Compact(paths), nil
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "vendor", "node_modules", "dist", "build":
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() && entry.Name() == "AGENTS.md" {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}
