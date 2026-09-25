---
name: repl-engineering
description: Improve the Go terminal REPL's editor, viewport, popups, tool cards, session and workflow navigation, approvals, model status, or configuration reload. Use for CLI interaction changes and regressions in cmd/internal/repl, not workflow DSL or provider execution changes alone.
---

# REPL engineering

Use current source and regression tests as the behavioral contract. Historical screenshots show previous symptoms, not necessarily current defects.
Paths below are relative to this skill directory. Run commands from the checkout containing the changes.

## Own terminal geometry

- Keep transcript scrolling inside the owned alternate screen. Do not append transient menus to native terminal scrollback.
- Reserve space for the composer, status, popups, and task rails before sizing the transcript. Recalculate cursor and mouse coordinates after resizing.
- Preserve the draft and cursor when opening or closing settings, history, and other overlays.
- Preserve the reader's transcript position when output arrives while scrolled back. Follow new output only when following is enabled.
- For tool folding, anchor a visible tool and its screen row. Mixed expanded/collapsed cards must still toggle predictably.
- Hit-test rendered rows and sidebar bounds. Wrapped text, expanded cards, and dragged dividers invalidate fixed row assumptions.

Inspect [ui.go](../../../cmd/internal/repl/ui.go), [transcript.go](../../../cmd/internal/repl/transcript.go), and [ui_view_test.go](../../../cmd/internal/repl/ui_view_test.go).
Useful regression cases include `TestTranscriptScrollKeepsComposerAndToolsExpand`, `TestToolToggleAnchorsVisibleToolAndHandlesMixedCards`, and `TestTaskAndQueueRailKeepSmallComposerUsable`.

## Reuse source editing and popup behavior

- Keep raw prompt bytes in `editor.Buffer`. Markdown projection maps source spans into display cells. Editing rendered text loses delimiters and Unicode positions.
- Reuse [readline.go](../../../cmd/internal/repl/readline.go) and [editor/readline.go](../../../cmd/internal/repl/editor/readline.go) for composer and rename forms.
  Preserve grapheme-aware motion, selection replacement, undo, kill, and yank. Existing global bindings intentionally reserve Ctrl+K for the palette and Alt+K for line-end deletion.
- Model popup opening, filtering, dismissal, selection, and return focus explicitly. Empty history search must support arrows and Enter before typing.
- Slash completion closes when its trigger disappears. Escape must close it without immediately reopening from the unchanged draft.
- Preserve palette query and highlighted session across rename cancellation or save. Renaming the highlighted fork must not silently switch the active session.
- Keep image chips mapped to stored attachments and missing-file state. A chip is not an inline provider vision payload.

Read [editor/projection.go](../../../cmd/internal/repl/editor/projection.go), [history_search_test.go](../../../cmd/internal/repl/history_search_test.go), [completion_test.go](../../../cmd/internal/repl/completion_test.go), and [power_bar.go](../../../cmd/internal/repl/power_bar.go).
Use `TestPaletteForkJumpAndRenameSelection` and `TestSidebarResizeAndRenameReadline` when changing modal routing.
Do not reuse Ctrl+D for image removal. Current exit behavior is Ctrl+D immediately, or two consecutive Ctrl+C presses.

## Keep session navigation distinct from execution

The sidebar and palette select persisted session IDs, not labels or list indexes. Renaming must preserve identity and ancestry.
Current family discovery is scoped to one canonical workspace. A conversation fork does not create a Git worktree.
Session switching observes active and queued work constraints. Keep those checks when adding new navigation surfaces.

Read [sessions.go](../../../cmd/internal/repl/sessions.go), [sessions_test.go](../../../cmd/internal/repl/sessions_test.go), and [session_menu.go](../../../cmd/internal/repl/session_menu.go).
When changing fork behavior, verify the inherited transcript still contains usable tool results. A sidebar rendering test does not establish provider replay correctness.

## Workflow threads and overlays

Before changing workflow navigation, approvals, recovery views, or prompt inspection,
read [workflow UI](references/workflow-ui.md). It defines background-panel ownership,
run-scoped permissions, overlay routing, and the interaction tests that catch hidden controls.

## Report context and metadata honestly

- Keep model identity paired with provider identity. Different providers and endpoints may expose different limits for similar model names.
- Distinguish estimated stored-text tokens, cumulative provider usage, and model context capacity. Do not present cumulative billed usage as current context occupancy.
- Use [context_capacity.go](../../../cmd/internal/repl/context_capacity.go) for capacity precedence. Preserve explicit configuration, provider/model overrides, catalog metadata, and unknown capacity behavior.
- Reuse [catalog_cache.go](../../../cmd/internal/repl/catalog_cache.go). Catalog metadata persists under XDG cache until explicit refresh, with provider, endpoint, and credential identity separated.
- Preserve discovery generation checks so late results cannot overwrite newer selections or refreshed configuration. Failure should retain the last usable catalog.

Regression references: [context_report_test.go](../../../cmd/internal/repl/context_report_test.go) and [catalog_test.go](../../../cmd/internal/repl/catalog_test.go).
Do not copy one provider's context limit into a generic model-family fallback.

## Reload at the correct boundary

Read `runReloadConfig` in [commands.go](../../../cmd/internal/repl/commands.go) and routing in [models.go](../../../cmd/internal/repl/models.go).
Parse configuration and discover instructions/skills before replacing usable state. Preserve existing handling for missing credentials and active mode changes.
Invalidate metadata tied to old endpoints and pending discovery generations. Reindex user-facing skill completion as part of reload.
Active model requests retain their prepared client. Active tasks retain loaded instructions until the next task. Do not mutate either snapshot halfway through execution.
Verify this with `TestReloadConfigReindexesSkillsAndPreservesDiskAndActiveRequest` and the model-router tests in [models_test.go](../../../cmd/internal/repl/models_test.go).

## Validate the interaction that changed

Use focused regression tests before broad checks. For example:

```sh
go test -race ./cmd/internal/repl/... -run 'Test(SlashDismissal|TranscriptScroll|ToolToggle|PaletteFork|SidebarResize|EmptyHistory|ContextCapacity|RemoteModelCatalog|ReloadConfig)'
```

After changing Go code, follow the repository's formatting and vet requirements in the affected module:

```sh
go fmt ./...
go vet ./...
```

Use the complete REPL suite when runtime or session integration warrants it:

```sh
go test -race ./cmd/internal/repl/...
```

If sandbox caches are unwritable, set `GOCACHE` and `GOMODCACHE` to writable temporary directories. Report blocked checks accurately.

For geometry or input changes, exercise a real terminal with a long transcript, narrow resize, scrolling, popup dismissal, and the changed shortcut.
Use temporary XDG configuration and state so smoke runs do not modify existing sessions or credentials.
Check actual terminal forwarding for Super/Meta shortcuts. Unit key messages cannot prove a terminal sends them.

Keep evidence separate: automated tests, cgo-free cross-builds, terminal interaction, provider requests, and clipboard/display-server checks establish different things.
Mark missing macOS PNG, Linux X11, or Wayland clipboard checks unverified. Never infer native clipboard success from a build or an `/attach` smoke.
The [CLI guide](../../../cmd/cli/README.md) documents current user-visible controls and platform requirements.
