package repl

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestCommandRegistryCompletionAndUnicodeReplacement(t *testing.T) {
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	config, err := LoadConfig(testEnv(root, ""))
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), testEnv(root, ""), 0, state, config, nil, NewModelCatalog(config, testEnv(root, "")), nil, nil)
	if err := model.draft.Set("  /re"); err != nil {
		t.Fatal(err)
	}
	model.refreshCompletion()
	if model.completion == nil || len(model.completion.choices) < 2 {
		t.Fatalf("root choices = %#v", model.completion)
	}
	for index, choice := range model.completion.choices {
		if choice.Value == "/rename" {
			model.completion.selected = index
		}
	}
	model.acceptCompletion()
	if model.draft.Source() != "  /rename" || !model.suppressCompletion {
		t.Fatalf("accepted completion = %q", model.draft.Source())
	}
	if err := model.draft.Restore("/rename José", len("/rename José")); err != nil {
		t.Fatal(err)
	}
	model.suppressCompletion = false
	model.refreshCompletion()
	if model.completion == nil || model.completion.hint != "<name>" {
		t.Fatalf("rename hint = %#v", model.completion)
	}
	model.acceptCompletion()
	if model.completion != nil || model.draft.Source() != "/rename José" {
		t.Fatal("hint completion changed free text")
	}
	names := map[string]bool{}
	for _, command := range commandRegistry() {
		if names[command.Name] || command.Run == nil || !strings.HasPrefix(command.Name, "/") {
			t.Fatalf("invalid command registry entry: %#v", command)
		}
		names[command.Name] = true
	}
}

func TestSkillSlashCompletionSearchAndInvocation(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Check database migrations\n---\nReview carefully.")
	writeInstructionFixture(t, filepath.Join(root, ".agents", "skills", "secret", "SKILL.md"), "---\nname: secret\ndescription: Hidden\nuser-invocable: false\n---\n")
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	runtime, adapter, _, _ := newRuntimeTestHost(t)
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, runtime, nil)
	_ = model.draft.Set("/rev")
	model.refreshCompletion()
	if model.completion == nil || len(model.completion.choices) != 1 || model.completion.choices[0].Value != "/review " {
		t.Fatalf("slash skills = %#v", model.completion)
	}
	model.acceptCompletion()
	if model.draft.Source() != "/review " {
		t.Fatal("skill completion")
	}
	model.runSkills("database", "")
	if model.completion == nil || len(model.completion.choices) != 1 {
		t.Fatal("description search failed")
	}
	for _, choice := range completeSkills(model, "") {
		if choice.Label == "/secret" {
			t.Fatal("hidden skill exposed")
		}
	}
	_ = model.draft.Set("/review check these changes")
	model.submit(false)
	call := nextRuntimeCall(t, adapter)
	found := false
	for _, item := range call.request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Text == "$review check these changes" {
			found = true
		}
	}
	answerRuntimeCall(call)
	if !found {
		t.Fatal("slash alias did not submit a named skill invocation")
	}
}

func TestInlineSlashCompletionPreservesDraft(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Check changes\n---\n")
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, _ = state.New(t.Context())
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	for _, tc := range []struct {
		raw          string
		cursor       int
		choice, want string
	}{
		{"Please /rev now", len("Please /rev"), "/review ", "Please /review  now"},
		{"José\nrun /mo later", len("José\nrun /mo"), "/model", "José\nrun /model later"},
	} {
		if err := model.draft.Restore(tc.raw, tc.cursor); err != nil {
			t.Fatal(err)
		}
		model.suppressCompletion = false
		model.refreshCompletion()
		if model.completion == nil {
			t.Fatalf("no completion for %q", tc.raw)
		}
		found := false
		for i, choice := range model.completion.choices {
			if choice.Value == tc.choice {
				model.completion.selected = i
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no choice %q in %#v", tc.choice, model.completion.choices)
		}
		model.acceptCompletion()
		if model.draft.Source() != tc.want {
			t.Fatalf("got %q want %q", model.draft.Source(), tc.want)
		}
		if model.draft.Cursor() != len(tc.want)-len(tc.raw[tc.cursor:]) {
			t.Fatalf("cursor = %d for %q", model.draft.Cursor(), tc.want)
		}
	}
	if got := inlineSkillAliases("Please /review this. Then /review.\nthen /model later", model.skills); got != "Please $review this. Then $review.\nthen /model later" {
		t.Fatalf("aliases = %q", got)
	} else if !explicitSkillNames(got)["review"] {
		t.Fatal("punctuation entered the skill name")
	}
}

func TestProviderCommandCompletionAndPicker(t *testing.T) {
	root := t.TempDir()
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, NewModelCatalog(config, getenv), nil, nil)
	_ = model.draft.Set("/provider oll")
	model.refreshCompletion()
	if model.completion == nil || len(model.completion.choices) != 2 {
		t.Fatalf("provider completion: %+v", model.completion)
	}
	_, cmd := model.runProvider("ollama-cloud", "")
	if cmd == nil || model.popup == nil || len(model.popup.providers) != 1 {
		t.Fatal("provider picker did not start discovery")
	}
	if _, ok := model.popup.providers["ollama-cloud"]; !ok {
		t.Fatal("wrong provider")
	}
	if config.Current().Model.Provider != "openai" {
		t.Fatal("provider changed before choosing a model")
	}
	model.popup = nil
	model.openPowerBar()
	found := false
	for _, choice := range model.powerBar.choices {
		if choice.kind == "provider" && choice.Value == "ollama-cloud" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("palette omits ollama-cloud provider")
	}
	model.powerBar.query = "provider ollama-cloud"
	model.updatePowerBar(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.popup == nil || len(model.popup.providers) != 1 {
		t.Fatal("palette did not open provider picker")
	}
}

func TestResolveSlashTokenPrecedence(t *testing.T) {
	catalog := SkillCatalog{Entries: []SkillEntry{
		{Skill: tool.Skill{Name: "review"}, UserInvocable: true},
		{Skill: tool.Skill{Name: "model"}, UserInvocable: true},
		{Skill: tool.Skill{Name: "secret"}},
	}}
	source := "Try /review and /model. Then /model and /review. plus /secret and /help. and //review"
	resolved := map[string]string{}
	for _, match := range inlineSlashSkill.FindAllStringIndex(source, -1) {
		token := source[match[0]:match[1]]
		if resolution, ok := resolveSlashToken(source, match, catalog); ok {
			resolved[token] = resolution.kind
		}
	}
	want := map[string]string{
		" /review":  "slash_skill",   // user-invocable skill reference
		" /model":   "slash_command", // built-in command wins the collision
		" /review.": "slash_skill",   // trailing punctuation stays outside the name
	}
	if len(resolved) != len(want) {
		t.Fatalf("resolved = %#v", resolved)
	}
	for token, kind := range want {
		if resolved[token] != kind {
			t.Fatalf("%q = %q, want %q", token, resolved[token], kind)
		}
	}
}

func TestSlashSyntaxSpansExcludeSuffixAndHiddenSkills(t *testing.T) {
	root := t.TempDir()
	writeInstructionFixture(t, filepath.Join(root, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Check changes\n---\n")
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(root, "")
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, nil, nil, nil)
	spans := model.slashSyntaxSpans("Run /review. then /help now")
	if len(spans) != 2 {
		t.Fatalf("spans = %#v", spans)
	}
	if spans[0].Kind != "slash_skill" || spans[0].Source.Start != 4 || spans[0].Source.End != 11 {
		t.Fatalf("skill span = %#v", spans[0])
	}
	if spans[1].Kind != "slash_command" || spans[1].Source.Start != 18 || spans[1].Source.End != 23 {
		t.Fatalf("command span = %#v", spans[1])
	}
	if spans[1].SGR == "" {
		t.Fatal("command span has no color")
	}
}
