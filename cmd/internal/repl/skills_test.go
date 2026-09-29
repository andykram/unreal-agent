package repl

import (
	"encoding/json/v2"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type skillTestContext struct{}

func (skillTestContext) Submit(operation.Spec) operation.ID { return "operation" }

func TestSkillDiscoveryPrecedenceAndYAMLFlags(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	writeInstructionFixture(t, filepath.Join(home, ".agents", "skills", "alpha", "SKILL.md"), "---\nname: alpha\ndescription: |\n  Global\n  description\nuser-invocable: true\n---\nGlobal\n")
	writeInstructionFixture(t, filepath.Join(project, ".agents", "skills", "alpha", "SKILL.md"), "---\nname: 'alpha'\ndescription: 'Project override'\ndisable-model-invocation: true\n---\nProject\n")
	writeInstructionFixture(t, filepath.Join(project, ".agents", "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: Hidden from users\nuser-invocable: false\n---\nHidden\n")
	catalog, err := DiscoverCLISkills(project, project, func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries) != 2 || catalog.Entries[0].Skill.Description != "Project override" || catalog.Entries[0].ModelVisible {
		t.Fatalf("catalog = %#v", catalog.Entries)
	}
	if _, okay := catalog.UserSkill("hidden"); okay {
		t.Fatal("non-user-invocable skill exposed")
	}
	if len(catalog.ModelSkills(nil)) != 1 || len(catalog.ModelSkills(map[string]bool{"alpha": true})) != 2 {
		t.Fatal("model visibility filtering failed")
	}
	if err := validateExplicitSkills(catalog, map[string]bool{"alpha": true}); err != nil {
		t.Fatal(err)
	}
	if err := validateExplicitSkills(catalog, map[string]bool{"hidden": true}); err == nil {
		t.Fatal("hidden user skill was accepted")
	}
}

func TestSkillDuplicateNameAndSymlinkDeduplication(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, ".agents", "skills")
	writeInstructionFixture(t, filepath.Join(skills, "one", "SKILL.md"), "---\nname: same\ndescription: One\n---\n")
	if err := os.Symlink("one", filepath.Join(skills, "alias")); err != nil {
		t.Fatal(err)
	}
	catalog, err := DiscoverCLISkills(root, root, func(string) string { return "" })
	if err != nil || len(catalog.Entries) != 1 {
		t.Fatalf("symlink dedupe = %#v, %v", catalog, err)
	}
	writeInstructionFixture(t, filepath.Join(skills, "two", "SKILL.md"), "---\nname: same\ndescription: Two\n---\n")
	if _, err := DiscoverCLISkills(root, root, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate name error = %v", err)
	}
}

func TestSkillRegistryBlocksUnavailableSkillUse(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.SkillUseName)
	registry := &skillRegistry{Registry: base}
	if _, err := base.RegisterSkill(tool.Skill{Name: "alpha", Description: "Alpha", Path: "/tmp/alpha/SKILL.md"}); err != nil {
		t.Fatal(err)
	}
	translator, okay := registry.Resolve(tool.SkillUseName)
	if !okay {
		t.Fatal("SkillUse unavailable")
	}
	call := llm.ToolCall{Name: tool.SkillUseName, CallID: "call", Arguments: `{"name":"alpha"}`}
	if status := translator.Translate(skillTestContext{}, call); !strings.Contains(status.Error, "not available") {
		t.Fatalf("denial = %#v", status)
	}
	registry.AllowExplicit("alpha")
	if status := translator.Translate(skillTestContext{}, call); status.Error != "" {
		t.Fatalf("allowed skill error = %#v", status)
	}
}

func TestSkillSimilarityRanksAndFilters(t *testing.T) {
	skills := []tool.Skill{{Name: "sql", Description: "Database migrations and schema changes"}, {Name: "design", Description: "Design visual websites and typography"}, {Name: "review", Description: "Review code changes"}}
	for _, query := range []string{"database migration", "databse migrations", "SQL"} {
		matches := similarSkills(skills, query)
		if len(matches) == 0 || matches[0].Name != "sql" || matches[0].Score <= 0 || matches[0].Score > 1 {
			t.Fatalf("%q = %#v", query, matches)
		}
	}
	if got := similarSkills(skills, "zzzzzz"); len(got) != 0 {
		t.Fatalf("unrelated query: %#v", got)
	}
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.SkillUseName)
	for _, skill := range skills {
		skill.Path = "/tmp/" + skill.Name + "/SKILL.md"
		if _, err := base.RegisterSkill(skill); err != nil {
			t.Fatal(err)
		}
	}
	registry := &skillRegistry{Registry: base, allowed: map[string]bool{"design": true}}
	translator, _ := registry.Resolve("SkillSearch")
	ctx := &captureSkillSearchContext{}
	status := translator.Translate(ctx, llm.ToolCall{Name: "SkillSearch", Arguments: `{"query":"database migrations"}`})
	if status.Error != "" || ctx.spec.Type != operation.TypeValue {
		t.Fatalf("search: %#v", status)
	}
	op := operation.Operation{Type: ctx.spec.Type, Version: ctx.spec.Version, State: ctx.spec.State, Status: operation.StatusCompleted}
	value, err := operation.DecodeValue(op)
	if err != nil {
		t.Fatal(err)
	}
	var result skillSearchResult
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	for _, match := range result.Matches {
		if match.Name != "design" {
			t.Fatal("search exposed a model-hidden skill")
		}
	}
	registry.AllowExplicit("sql")
	status = translator.Translate(ctx, llm.ToolCall{Arguments: `{"query":"database migrations"}`})
	if status.Error != "" {
		t.Fatal(status.Error)
	}
	op.State = ctx.spec.State
	translated, err := translator.TranslateResult("search", status, []operation.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(translated.Output[0].Value, `"name":"sql"`) || strings.Contains(translated.Output[0].Value, "/tmp/") {
		t.Fatal("search result missing name or leaked path")
	}
	if rejected := translator.Translate(ctx, llm.ToolCall{Arguments: `{"query":" "}`}); rejected.Error == "" {
		t.Fatal("empty query accepted")
	}
}

type captureSkillSearchContext struct{ spec operation.Spec }

func (ctx *captureSkillSearchContext) Submit(spec operation.Spec) operation.ID {
	ctx.spec = spec
	return "search"
}

func TestModelSkillSearchThenLoad(t *testing.T) {
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	path := filepath.Join(t.TempDir(), "SKILL.md")
	writeInstructionFixture(t, path, "Unique skill instructions for database changes.")
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.SkillUseName)
	_, err := base.RegisterSkill(tool.Skill{Name: "database", Description: "Database migrations", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	registry := &skillRegistry{Registry: base, allowed: map[string]bool{"database": true}}
	runtime.options.Tools = registry
	runtime.options.NewBuilder = func(string) (contextbuilder.Builder, func() bool, error) {
		b := contextbuilder.NewBuilder()
		for _, d := range registry.StaticDefinitions() {
			b.AddTool(d.Tool)
		}
		return b, nil, nil
	}
	if _, _, err := runtime.Submit(t.Context(), "find a skill", false); err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeCall(t, adapter)
	advertised := false
	for _, d := range first.request.Tools {
		if d.Name == "SkillSearch" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("search not advertised")
	}
	first.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "search", Name: "SkillSearch", Arguments: `{"query":"database migration"}`}}}}
	next := nextRuntimeCall(t, adapter)
	found := false
	for _, item := range next.request.Input {
		if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == "search" && strings.Contains(r.Output[0].Value, `"name":"database"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("model did not receive search result")
	}
	next.response <- llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "load", Name: "SkillUse", Arguments: `{"name":"database"}`}}}}
	loaded := nextRuntimeCall(t, adapter)
	found = false
	for _, item := range loaded.request.Input {
		if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == "load" && strings.Contains(r.Output[0].Value, "Unique skill instructions") {
			found = true
		}
	}
	answerRuntimeCall(loaded)
	if !found {
		t.Fatal("model did not receive loaded skill")
	}
	waitRuntimeTasks(t, runtime, 1)
}

func TestApprovalGatePlanAndEditModes(t *testing.T) {
	config, err := LoadConfig(testEnv(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	base := tool.NewRegistry(tool.StaticTranslators{Bash: fakeGateTranslator{}}, tool.BashName)
	gate := &approvalGate{Registry: base, config: config, ctx: t.Context()}
	translator, ok := gate.Resolve(tool.BashName)
	if !ok {
		t.Fatal("Bash missing")
	}
	call := llm.ToolCall{Name: tool.BashName, Arguments: `{"command":"echo safe"}`}
	if status := translator.Translate(skillTestContext{}, call); status.Error != "" {
		t.Fatalf("default denied: %#v", status)
	}
	if err := config.SaveSetting("mode", "plan"); err != nil {
		t.Fatal(err)
	}
	if status := translator.Translate(skillTestContext{}, call); status.Error == "" {
		t.Fatal("plan permitted Bash")
	}
	if err := config.SaveSetting("mode", "edit"); err != nil {
		t.Fatal(err)
	}
	done := make(chan tool.CallStatus, 1)
	go func() { done <- translator.Translate(skillTestContext{}, call) }()
	var pending *approvalRequest
	deadline := time.After(time.Second)
	for pending == nil {
		select {
		case <-deadline:
			t.Fatal("no approval request")
		default:
			pending = gate.Pending()
			runtime.Gosched()
		}
	}
	select {
	case <-done:
		t.Fatal("Bash ran before approval")
	default:
	}
	gate.Decide(pending, false)
	if status := <-done; status.Error == "" {
		t.Fatal("denial ran Bash")
	}
	go func() { done <- translator.Translate(skillTestContext{}, call) }()
	pending = nil
	for pending == nil {
		select {
		case <-deadline:
			t.Fatal("no second approval request")
		default:
			pending = gate.Pending()
			runtime.Gosched()
		}
	}
	gate.Decide(pending, true)
	if status := <-done; status.Error != "" {
		t.Fatalf("approved command denied: %#v", status)
	}
}

type fakeGateTranslator struct{}

func (fakeGateTranslator) Translate(tool.Context, llm.ToolCall) tool.CallStatus {
	return tool.CallStatus{}
}
func (fakeGateTranslator) TranslateResult(string, tool.CallStatus, []operation.Operation) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func TestApprovalGatePlanBlocksTaskWrites(t *testing.T) {
	config, err := LoadConfig(testEnv(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("mode", "plan"); err != nil {
		t.Fatal(err)
	}
	gate := &approvalGate{Registry: taskRegistry{Registry: tool.NewRegistry(tool.StaticTranslators{})}, config: config, ctx: t.Context()}
	write, ok := gate.Resolve("TaskWrite")
	if !ok {
		t.Fatal("TaskWrite missing")
	}
	if status := write.Translate(skillTestContext{}, llm.ToolCall{Name: "TaskWrite", Arguments: `{}`}); status.Error == "" {
		t.Fatal("plan permitted task mutation")
	}
	read, ok := gate.Resolve("TaskRead")
	if !ok {
		t.Fatal("TaskRead missing")
	}
	if status := read.Translate(skillTestContext{}, llm.ToolCall{Name: "TaskRead", Arguments: `{}`}); status.Error != "" {
		t.Fatalf("plan blocked read: %s", status.Error)
	}
}
