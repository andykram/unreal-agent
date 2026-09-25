package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const workflowCompletionLimit = 30

var workflowDirectories = []string{filepath.Join(".agents", "workflows"), "workflows"}

// workflowArgument treats the entire argument as one path, including spaces.
// Complete double-quoted paths use the same decoding as /attach. No shell or
// Python parser is involved, and completion never imports or executes scripts.
func workflowArgument(argument string) (string, error) {
	argument = strings.TrimSpace(argument)
	if strings.HasPrefix(argument, `"`) {
		path, err := strconv.Unquote(argument)
		if err != nil {
			return "", fmt.Errorf("invalid quoted workflow path: %w", err)
		}
		return path, nil
	}
	return argument, nil
}

// resolveWorkflowScript accepts explicit paths, or top-level script names from
// .agents/workflows and workflows. Bare names can omit .py. A workspace-root
// foo.py also participates in bare-name resolution; use ./foo.py to disambiguate.
func resolveWorkflowScript(workspace, argument string) (string, error) {
	path, err := workflowArgument(argument)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("provide a workflow name or .py path")
	}
	var candidates []string
	if !strings.ContainsRune(path, filepath.Separator) && path != "." && path != ".." {
		name := path
		if filepath.Ext(name) == "" {
			name += ".py"
		}
		for _, directory := range workflowDirectories {
			candidates = append(candidates, filepath.Join(workspace, directory, name))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	candidates = append(candidates, path)
	var matches []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		if seen[absolute] || filepath.Ext(absolute) != ".py" {
			continue
		}
		seen[absolute] = true
		info, err := os.Stat(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("inspect workflow %q: %w", absolute, err)
		}
		if info.Mode().IsRegular() {
			matches = append(matches, absolute)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("workflow %q must name an existing regular .py file", argument)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous workflow %q; use an explicit path: %s", argument, strings.Join(matches, ", "))
	}
}

func completeWorkflowArgument(model *uiModel, argument string) []commandChoice {
	if model.state == nil {
		return nil
	}
	path, err := workflowArgument(argument)
	if err != nil {
		return nil
	}
	workspace := model.state.Workspace
	choices := make([]commandChoice, 0, workflowCompletionLimit)
	seen := map[string]bool{}
	appendDirectory := func(directory, prefix string, includeDirectories bool) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if len(choices) >= workflowCompletionLimit {
				return
			}
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			full := filepath.Join(directory, entry.Name())
			info, err := os.Stat(full)
			if err != nil || seen[full] {
				continue
			}
			isDirectory := info.IsDir()
			if isDirectory && !includeDirectories || !isDirectory && (!info.Mode().IsRegular() || filepath.Ext(full) != ".py") {
				continue
			}
			value := full
			if !filepath.IsAbs(path) {
				value, err = filepath.Rel(workspace, full)
				if err != nil {
					continue
				}

				if !strings.ContainsRune(value, filepath.Separator) && !isDirectory {
					value = "." + string(filepath.Separator) + value
				}
			}
			label, description := entry.Name(), "Python workflow"
			if isDirectory {
				value += string(filepath.Separator)
				label += string(filepath.Separator)
				description = "directory"
			} else if strings.ContainsAny(value, " \t\r\n\"") {
				value = strconv.Quote(value)
			}
			// Directory spaces stay literal so users can append a filename.
			// The command consumes the entire path rather than shell words.
			seen[full] = true
			choices = append(choices, commandChoice{Label: label, Description: description, Value: "/workflow " + value})
		}
	}

	if !filepath.IsAbs(path) && !strings.ContainsRune(path, filepath.Separator) {
		for _, directory := range workflowDirectories {
			appendDirectory(filepath.Join(workspace, directory), path, false)
		}
	}
	directory, prefix := filepath.Split(path)
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(workspace, directory)
	}
	appendDirectory(directory, prefix, true)
	return choices
}
