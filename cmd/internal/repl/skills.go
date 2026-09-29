package repl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/tool"
	"gopkg.in/yaml.v3"
)

type SkillEntry struct {
	Skill         tool.Skill
	UserInvocable bool
	ModelVisible  bool
	Root          string
}

type SkillCatalog struct{ Entries []SkillEntry }

type skillMetadata struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	UserInvocable          *bool  `yaml:"user-invocable"`
}

func parseSkillMetadata(contents []byte) (skillMetadata, error) {
	lines := strings.Split(string(contents), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return skillMetadata{}, errors.New("missing YAML frontmatter")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return skillMetadata{}, errors.New("missing closing YAML frontmatter delimiter")
	}
	var metadata skillMetadata
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &metadata); err != nil {
		return skillMetadata{}, err
	}
	metadata.Name, metadata.Description = strings.TrimSpace(metadata.Name), strings.TrimSpace(metadata.Description)
	if metadata.Name == "" || metadata.Description == "" {
		return skillMetadata{}, errors.New("name and description are required")
	}
	if strings.ContainsAny(metadata.Name, "\r\n") {
		return skillMetadata{}, errors.New("skill name must be one line")
	}
	return metadata, nil
}

func DiscoverCLISkills(workspace, projectRoot string, getenv func(string) string) (SkillCatalog, error) {
	var roots []string
	if home := getenv("HOME"); home != "" {
		roots = append(roots, filepath.Join(home, ".agents", "skills"))
	}
	var ancestors []string
	for current := workspace; ; current = filepath.Dir(current) {
		ancestors = append(ancestors, filepath.Join(current, ".agents", "skills"))
		if current == projectRoot || filepath.Dir(current) == current {
			break
		}
	}
	slices.Reverse(ancestors)
	roots = append(roots, ancestors...)
	byName := map[string]SkillEntry{}
	canonicalPaths := map[string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return SkillCatalog{}, fmt.Errorf("read skills in %s: %w", root, err)
		}
		localNames := map[string]bool{}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(root, entry.Name(), "SKILL.md")
			canonical, err := filepath.EvalSymlinks(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return SkillCatalog{}, fmt.Errorf("resolve skill %s: %w", path, err)
			}
			if canonicalPaths[canonical] {
				continue
			}
			contents, err := os.ReadFile(canonical)
			if err != nil {
				return SkillCatalog{}, fmt.Errorf("read skill %s: %w", path, err)
			}
			metadata, err := parseSkillMetadata(contents)
			if err != nil {
				return SkillCatalog{}, fmt.Errorf("parse skill %s: %w", path, err)
			}
			if localNames[metadata.Name] {
				return SkillCatalog{}, fmt.Errorf("duplicate skill name %q in %s", metadata.Name, root)
			}
			localNames[metadata.Name] = true
			canonicalPaths[canonical] = true
			userInvocable := metadata.UserInvocable == nil || *metadata.UserInvocable
			byName[metadata.Name] = SkillEntry{Skill: tool.Skill{Name: metadata.Name, Description: metadata.Description, Path: canonical}, UserInvocable: userInvocable, ModelVisible: !metadata.DisableModelInvocation, Root: root}
		}
	}
	result := SkillCatalog{Entries: make([]SkillEntry, 0, len(byName))}
	for _, entry := range byName {
		result.Entries = append(result.Entries, entry)
	}
	slices.SortFunc(result.Entries, func(a, b SkillEntry) int { return strings.Compare(a.Skill.Name, b.Skill.Name) })
	return result, nil
}

func (catalog SkillCatalog) ModelSkills(explicit map[string]bool) []tool.Skill {
	var skills []tool.Skill
	for _, entry := range catalog.Entries {
		if entry.ModelVisible || explicit[entry.Skill.Name] {
			skills = append(skills, entry.Skill)
		}
	}
	return skills
}

func (catalog SkillCatalog) UserSkill(name string) (SkillEntry, bool) {
	for _, entry := range catalog.Entries {
		if entry.Skill.Name == name && entry.UserInvocable {
			return entry, true
		}
	}
	return SkillEntry{}, false
}
