package repl

import (
	"encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/cmd/internal/repl/editor"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

var inlineSlashSkill = regexp.MustCompile(`(?:^|[[:space:]])/([a-zA-Z0-9][a-zA-Z0-9._:/-]*)`)

func inlineSkillAliases(text string, catalog SkillCatalog) string {
	var rewritten strings.Builder
	last := 0
	for _, match := range inlineSlashSkill.FindAllStringIndex(text, -1) {
		resolution, ok := resolveSlashToken(text, match, catalog)
		if !ok || resolution.kind != "slash_skill" {
			continue
		}
		rewritten.WriteString(text[last:resolution.slashAt])
		rewritten.WriteString("$" + resolution.name + resolution.suffix)
		last = resolution.slashAt + 1 + len(resolution.name) + len(resolution.suffix)
	}
	if last == 0 {
		return text
	}
	rewritten.WriteString(text[last:])
	return rewritten.String()
}

// slashResolution is one inline slash token resolved with the same precedence
// as submit time: an exact built-in command name wins, otherwise a
// user-invocable skill name with optional trailing punctuation becomes an
// explicit $name reference. Suffix punctuation stays literal.
type slashResolution struct {
	kind    string // "slash_command" or "slash_skill"
	name    string // resolved name without the slash
	suffix  string // trailing punctuation kept outside name
	slashAt int    // byte offset of the token's slash
}

func resolveSlashToken(text string, match []int, catalog SkillCatalog) (slashResolution, bool) {
	slashAt := match[0]
	if text[slashAt] != '/' {
		slashAt++
	}
	name := text[slashAt+1 : match[1]]
	for _, command := range commandRegistry() {
		if command.Name == "/"+name {
			return slashResolution{kind: "slash_command", name: name, slashAt: slashAt}, true
		}
	}
	resolved, suffix := name, ""
	if _, ok := catalog.UserSkill(name); !ok {
		trimmed := strings.TrimRight(name, ".:")
		if trimmed == name || !userSkillExists(catalog, trimmed) {
			return slashResolution{}, false
		}
		resolved, suffix = trimmed, name[len(trimmed):]
	}
	// The submit path keeps a skill reference literal when its resolved name is
	// also a built-in command, so leave that token uncolored too.
	for _, command := range commandRegistry() {
		if command.Name == "/"+resolved {
			return slashResolution{}, false
		}
	}
	return slashResolution{kind: "slash_skill", name: resolved, suffix: suffix, slashAt: slashAt}, true
}

func userSkillExists(catalog SkillCatalog, name string) bool {
	_, ok := catalog.UserSkill(name)
	return ok
}

// slashSyntaxSpans maps resolved inline slash tokens to editor spans so the
// composer can colorize the references the submit path will honor.
func (model *uiModel) slashSyntaxSpans(source string) []editor.SyntaxSpan {
	spans := make([]editor.SyntaxSpan, 0)
	for _, match := range inlineSlashSkill.FindAllStringIndex(source, -1) {
		resolution, ok := resolveSlashToken(source, match, model.skills)
		if !ok {
			continue
		}
		span := editor.SyntaxSpan{
			Kind:   resolution.kind,
			Source: editor.SourceRange{Start: resolution.slashAt, End: resolution.slashAt + 1 + len(resolution.name)},
		}
		if !model.noColor {
			palette := model.palette()
			if resolution.kind == "slash_command" {
				span.SGR = hexSGR(palette.accent)
			} else {
				span.SGR = hexSGR(palette.lilac)
			}
		}
		spans = append(spans, span)
	}
	return spans
}

var skillToken = regexp.MustCompile(`(?:^|[[:space:]])\$([a-zA-Z0-9][a-zA-Z0-9_:/-]*(?:\.[a-zA-Z0-9_:/-]+)*)`)

func explicitSkillNames(text string) map[string]bool {
	names := map[string]bool{}
	for _, match := range skillToken.FindAllStringSubmatch(text, -1) {
		names[match[1]] = true
	}
	return names
}

type skillRegistry struct {
	tool.Registry
	mu      sync.RWMutex
	allowed map[string]bool
}

func (registry *skillRegistry) SetAllowed(catalog SkillCatalog, explicit map[string]bool) {
	allowed := map[string]bool{}
	for _, entry := range catalog.Entries {
		if entry.ModelVisible || explicit[entry.Skill.Name] {
			allowed[entry.Skill.Name] = true
		}
	}
	registry.mu.Lock()
	registry.allowed = allowed
	registry.mu.Unlock()
}

func (registry *skillRegistry) AllowExplicit(name string) {
	registry.mu.Lock()
	if registry.allowed == nil {
		registry.allowed = map[string]bool{}
	}
	registry.allowed[name] = true
	registry.mu.Unlock()
}

func (registry *skillRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == "SkillSearch" {
		return skillSearchTranslator{registry: registry}, true
	}
	translator, exists := registry.Registry.Resolve(name)
	if !exists || name != tool.SkillUseName {
		return translator, exists
	}
	return restrictedSkillTranslator{Translator: translator, registry: registry}, true
}

type restrictedSkillTranslator struct {
	tool.Translator
	registry *skillRegistry
}

func (translator restrictedSkillTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	var arguments struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return tool.ErrorStatus(fmt.Sprintf("decode SkillUse arguments: %v", err), operation.DefaultMaxOutputLength)
	}
	translator.registry.mu.RLock()
	allowed := translator.registry.allowed[arguments.Name]
	translator.registry.mu.RUnlock()
	if !allowed {
		return tool.ErrorStatus(fmt.Sprintf("skill %q is not available for this task", arguments.Name), operation.DefaultMaxOutputLength)
	}
	return translator.Translator.Translate(ctx, call)
}

func validateExplicitSkills(catalog SkillCatalog, names map[string]bool) error {
	for name := range names {
		if _, exists := catalog.UserSkill(name); !exists {
			return fmt.Errorf("unknown or non-user-invocable skill $%s", name)
		}
	}
	return nil
}

func (registry *skillRegistry) StaticDefinitions() []tool.Definition {
	return append(registry.Registry.StaticDefinitions(), skillSearchDefinition())
}
