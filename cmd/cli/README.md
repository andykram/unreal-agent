# unreal-agent-repl

`unreal-agent-repl` is the interactive terminal client for the local unreal-agent harness. It needs a real terminal. The existing `unreal-agent-runner` remains the JSON and stdin interface.

## Build and run

From the repository root:

```sh
make build
bin/unreal-agent-repl --workspace .
```

`go install ./cmd/cli` installs a binary named `cli`; use `make build` for the named binary. `--resume` opens a picker for this workspace. `--resume 'Session name'` and `--resume=<session-id>` select directly. `--session-directory` overrides the harness session store path; `--tool-heartbeat-interval` sets the operation heartbeat (default `10m`). `--help` lists startup flags.

Set a provider credential before starting: `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, or `FIREWORKS_API_KEY`, or `OLLAMA_API_KEY` for `ollama-cloud`. Local `ollama` uses its local server. `openai-codex` uses the existing Codex authentication configuration. Provider-specific base URLs can be set in `/settings`. The first launch can open without a usable model; `/model` or `/settings` can resolve setup.

## Working in the REPL

Type a prompt and press Enter. A prompt submitted while work is active is queued until idle. Alt+Enter steers the current work. The activity rail animates while a task runs and shows the model, effort, and estimated context usage. A full-screen transcript keeps the composer fixed while you scroll with the mouse or Page Up and Page Down. Ctrl+End returns to the latest output. Ctrl+D exits immediately, canceling active work and leaving queued prompts unsent. Ctrl+C stops active work or clears the draft, then asks for a second consecutive Ctrl+C to exit. Any other key, paste, or mouse input cancels that confirmation. `/quit` exits when no work is active or queued. The terminal title includes the workspace and session.

| Key | Action |
| --- | --- |
| Ctrl+J or Shift+Enter | Add a draft line |
| Up/Down | Move in a multiline draft; browse history at its edges |
| Ctrl+P / Ctrl+N | Browse accepted prompt and command history |
| Ctrl+R | Search prompt and command history |
| Ctrl+K / terminal-reported Command+K | Global command palette for commands, sessions, forks, models, and skills |
| Ctrl+Z or Ctrl+_ / Ctrl+Shift+Z or Alt+_ | Undo / redo |
| Ctrl+A / Ctrl+E | Start / end of line |
| Ctrl+B / Ctrl+F | Move one character |
| Alt+B / Alt+F | Move one word |
| Ctrl+W / Alt+Backspace / Alt+D | Kill previous whitespace word / previous word / next word |
| Ctrl+U / Alt+K | Kill to line start / end |
| Ctrl+Y | Yank the last killed text |
| Shift+arrows / Shift+Home / Shift+End | Select source text; typing replaces it |
| Ctrl+Shift+A | Select the whole draft where the terminal sends this key |
| Ctrl+G | Edit the raw draft in an external editor |
| Ctrl+V | Paste an image first, otherwise UTF-8 text |
| Ctrl+X | Remove the last image chip |
| Ctrl+D | Exit immediately |
| Ctrl+C twice | Stop/clear, then exit on the second consecutive press |
| Ctrl+Q | Return to a pending question from the composer |
| Ctrl+T | Expand/collapse tool cards; leave a question form while keeping it pending |
| Page Up / Page Down | Scroll the transcript |
| Ctrl+Home / Ctrl+End | Jump to the oldest / latest transcript output |

The composer stores raw Markdown in plain and Markdown modes. Markdown mode hides supported punctuation until the cursor or selection reaches its construct. Its visible height defaults to ten rows and scrolls within the draft; the source can be much longer (up to 4 MiB). The external editor receives a private temporary Markdown file. Set `editor.command` in `/settings`, or use `$EDITOR`, `$VISUAL`, then `vi`. The command is parsed into arguments without a shell; use a blocking editor command such as `code --wait`.

Tool calls appear as compact cards with their command and result status. Click a card to expand its arguments and output, or press Ctrl+T to expand or collapse all cards. Long output keeps its existing size limit and full-output file reference.

Slash commands are completed in the composer. Enter accepts a highlighted completion; press Enter again to run it. Esc or deleting the slash closes the menu. `//` at the start of a prompt sends a literal leading `/`. The composer colorizes any `/command` and any `/skill-name` reference that the submit path will honor; partial or unknown names stay unstyled.

| Command | Action |
| --- | --- |
| `/help` | Show commands and keys |
| `/workflow path.py` / `/workflow resume ID` | Compile, execute, inspect, or resume a Python workflow |
| `/effort [level]` | Select reasoning effort for the next model request |
| `/model [provider] [model-id]` | Browse or select a model for the next request |
| `/provider [provider]` | Complete a provider name, then choose one of its models |
| `/settings` | Edit durable configuration |
| `/reload-config` | Reload TOML and re-index skills without restarting |
| `/skills [search words]` | Search skill names and descriptions; arrows select and Enter inserts |
| `/system-prompt` | Show the effective prompt loaded for the latest task, or the next-task preview before the first task |
| `/context` | Show current session token totals and a visual source breakdown |
| `/new` | Start a new session |
| `/fork [name]` | Fork from the latest turn and switch to the child |
| `/resume [name-or-id]` | Browse or resume a saved session |
| `/rename <name>` | Name the current session |
| `/attach <path>` | Attach a local image |
| `/detach` | Remove the last image chip |
| `/quit` | Exit when idle |

The resume picker shows saved session names, IDs, prior providers when known, unfinished operation counts, and unavailable records. Names are unique within a workspace. A session is locked against simultaneous use by another REPL. Resuming restores its transcript in the scrollable view and retains the current configured model for future requests. Provider-specific opaque reasoning is only reused when its recorded origin matches the active provider endpoint; unknown or foreign opaque items are dropped.

When the agent asks a question, its form pauses the next model request. Submit the form to continue. Esc opens dismissal choices; Ctrl+T leaves the form pending while the composer remains usable. Unanswered questions are recovered after restarting the same session.

`/context` shows the sum of provider-reported input and output tokens across this session's completed responses. Cached input and reasoning output are included in those totals and shown separately. It also estimates the current stored context in tokens, split among the core prompt, instructions, skills, tool definitions, conversation, and tool activity. The model window comes from `model.context_window_tokens`, a provider and model override in TOML, provider catalog metadata when available, or the exact documented GPT-6 Astra, Sol, and Luna model IDs on the official OpenAI API endpoint. The Codex subscription catalog supplies its own `context_window`; it is fetched automatically on launch and model selection. Other models without catalog metadata also show an unknown window. Text estimates use four bytes per token. Actual provider tokenization may differ; opaque reasoning and image payloads are excluded. Press Esc or Enter to return to the transcript. The status line shows the selected effort and the same estimated used and available context.

`/fork [name]` creates a child session from the latest turn and switches to it. The full-height right sidebar lists the current fork family on wide terminals. Click a session to switch, or right-click it for a context menu with Rename and Switch to session. Drag its left divider to resize the sidebar. Ctrl+K opens the command palette; choose Forks to open the family picker, or search for any session by name. Switching sessions requires active work and queued prompts to finish. Forks preserve completed tool outputs. Calls that were unfinished at the fork get an explicit unavailable-result marker and are not restarted in the child.

Images appear as styled `[Image #1]` references inside the draft. Images from Ctrl+V must be PNG. `/attach` accepts PNG, JPEG, BMP, TIFF, and WebP. Each image is fully decoded and limited to 32 MiB and 32 million pixels. Files are copied into private session storage, and the prompt gives the agent a `ViewImage` path. An attachment is a file reference, not an inline vision message. History restores image chips when files remain available and marks missing files; remove missing chips with Ctrl+X or `/detach` before sending. Native clipboard access depends on the host's macOS, X11, or Wayland clipboard support.

## Configuration and local data

The config file is `${XDG_CONFIG_HOME:-$HOME/.config}/unreal-agent-repl/config.toml`. `/settings` saves selected values there with private file permissions. For example:

```toml
# Optional mode: default keeps current behavior. Choose all, edit, or plan.
# mode = "edit"
# Optional: replace the default agent instructions. Empty string disables them.
# system_prompt = "Your custom instructions here."

[model]
provider = "openai"
id = "gpt-6-astra"
reasoning_effort = "high"
max_attempts = 5
# For models without a discoverable window, set context_window_tokens here.

[editor]
mode = "markdown"
max_height = 10
command = "code --wait"

[appearance]
theme = "auto"

[history]
max_entries = 1000

[providers.ollama]
base_url = "http://localhost:11434/v1"
models = ["qwen3"]

[providers.ollama.context_windows]
"qwen3" = 32768 # Example; use the effective Ollama context setting
```

Supported providers are `openai`, `openai-codex`, `openrouter`, `fireworks`, and `ollama` (local) and `ollama-cloud` (hosted at `https://ollama.com/v1`). Set `OLLAMA_API_KEY` and use `/provider ollama-cloud` to fetch and choose a cloud model. `/model` accepts a manual ID even if discovery has no listing. Explicit TOML model values override `UNREAL_HARNESS_LLM_PROVIDER`, `UNREAL_HARNESS_LLM_MODEL`, and `UNREAL_HARNESS_LLM_MAX_ATTEMPTS` defaults, so a selection survives relaunch. Credentials stay in environment and authentication files; they are not written to TOML.

Remote model listings are cached without expiration in `${XDG_CACHE_HOME:-$HOME/.cache}/unreal-agent-repl/models`. Opening `/model` or looking up the selected model's context window reuses the cache across launches. Press F5 in the model picker to fetch a fresh listing. Cache files are private and keyed by provider, endpoint, and a hash of the credential or Codex account identity; the credential itself is not stored. A missing or damaged cache is fetched again. Ollama Cloud listings come from `https://ollama.com/api/tags` and expire after 24 hours; each listing also reports the per-model context window from `POST /api/show`, so the status line and `/context` show the real limit instead of an unknown one. Local Ollama listings are fetched from the local server each time.

The default agent instructions are embedded from [SYSTEM_PROMPT.md](../internal/repl/SYSTEM_PROMPT.md). Set the top-level `system_prompt` string in TOML to replace those instructions. The harness protocol, workspace path, applicable project instructions, and skill index are still provided. Run `/reload-config` after editing TOML externally. Invalid TOML or skill metadata leaves the active config and skill index unchanged. Active requests keep their client snapshots. The next model request uses the reloaded provider settings; the next task loads updated system instructions and skills.

The agent has `TaskWrite` and `TaskRead` tools for ordered task lists. The default prompt directs it to use them for multi-step work, preserve added scope, record completion evidence, and reconcile unfinished work before stopping. Dependencies must precede their dependents; active and completed tasks require completed dependencies. Task lists are stored in session history and fork independently. These instructions guide the model; they do not guarantee that every model will follow them.

Session metadata, transcript data, history, image copies, and operation files live under `${XDG_STATE_HOME:-$HOME/.local/state}/unreal-agent-repl`. Workspace paths are canonicalized and sessions are matched to the current workspace. History has a workspace-specific JSONL file and suppresses consecutive duplicate entries.

At each task, the client reads `$HOME/.agents/AGENTS.md` and applicable project ancestor `AGENTS.md` files, expanding `@` imports outside fenced blocks. It indexes nested project instructions for the agent to read when entering their scopes. It discovers `SKILL.md` files from global and project `.agents/skills` directories. YAML frontmatter requires `name` and `description`; `disable-model-invocation` hides a skill from automatic tool selection, and `user-invocable: false` hides it from slash completion and user skill search. Explicit `$skill-name` invocation can expose an otherwise model-hidden, user-invocable skill for that task. Project skill definitions override global ones with the same name.

This CLI runs on macOS and Linux. Build and automated tests cover the core workflow; clipboard behavior still needs a host clipboard and display server. There is no browser or WASM terminal client.

The CLI owns model catalog, selection, routing, settings, and its `AskUser` operation. Its only coordinator extension is the optional `CanRequestModel` predicate that keeps a question unanswered until its tool result is durable. A nil predicate retains the runner's existing behavior. The CLI does not change the session store format or the runner request protocol.

### History and sidebar controls

Ctrl+R opens history with the newest item selected. Up/Down wrap through results before or after typing a filter. Enter loads the selection. Loading history adds a transcript notice. After one minute, adjacent expired notices fold into “N items hidden” without entering model context.

An active task list appears above the composer after the agent stores a TaskWrite update. Checks animate when tasks complete or reopen. When all tasks are complete, the list stays for 15 seconds, fades over four frames, then hides. A later task update cancels the fade. The active state and completion time come from stored task updates, so unfinished lists reappear after resume and fork. A recently completed list finishes its 15-second hold after reopening. The session format does not change. Queued prompts share a muted group below the task list and above the composer. They remain there in FIFO order until each prompt is admitted, then appear in the transcript. At small terminal heights, the groups show counts before extra entries; the composer keeps priority. Queued prompts remain in memory only, as before.

The right sidebar stays visible with an Unreal title, including when there is only one session. On very narrow terminals, the command palette remains available. The right sidebar has a Sessions titlebar. Drag its left divider to resize it. Right-click a session for its context menu. Rename supports the same readline word movement, deletion, undo, and yank as the composer. Enter saves and Esc cancels. Alt+K deletes to the line end. Ctrl+K opens the global command palette, including from Rename. Ctrl+T and Ctrl+D retain their tool-card and exit actions.

Skills appear alongside commands when you type `/`. Select one to insert `/skill-name`, add your task, and send. Completion works after other words and on later lines. Typing `/skill-name your task` directly also invokes the skill. Built-in commands take precedence over skill names; `/skills` and `$skill-name` remain available for collisions. `/skills database migration` searches both names and descriptions and requires all search words to match.

The model calls `SkillUse` with `{"name":"skill-name"}`. The harness resolves the registered name to its file. The prompt index contains names and descriptions, without full file locations. The loaded result includes its source path so relative resources can be resolved. Reload refreshes completion and search immediately; active tasks keep their skill registration until the next task.

### Model skill search

The model has a `SkillSearch` tool with a required `query` string. It ranks available skill names and descriptions with local TF-IDF cosine similarity. Character trigrams help match partial names and typos. This is lexical similarity; no embeddings or external search service are used. Up to five matches return names, descriptions, and scores. The model then calls `SkillUse` with a selected name. Model-hidden skills stay excluded unless explicitly enabled for the task. Search results are saved as durable value operations so recovery retains the original ranked results.

Set top-level `mode = "all"` for automatic tool calls, `mode = "edit"` to approve each Bash command, or `mode = "plan"` for read-only tool use and plan review. The default mode keeps prior behavior. In edit mode, Bash commands require approval even when they edit files; there is no separate file-edit tool today. A pending approval shows the command with scroll controls. Press Y to allow it once or N/Esc to deny it. The gate runs in the harness and works with tool-capable providers; it does not use a provider-specific approval API. Plan mode rejects Bash and TaskWrite at the tool boundary. The latest assistant response opens in plan review when work goes idle. Use Up/Down and PgUp/PgDn to scroll, C to comment on a line, and R to request a revision. `/plan` reopens the latest response.

`/effort` opens level completion; `/effort high` changes and saves the level directly. Providers validate the levels they support. Active requests retain their previous settings. `/model` opens the model picker; `/model provider model-id` switches directly. `/provider` offers provider completion; `/provider name` opens a model picker scoped to that provider. Cmd+K (or Ctrl+K) also lists providers and opens the same picker. Choosing a model saves the provider and model together.

The command palette supports query filtering, Up/Down, Enter, and Esc. Esc restores the underlying view and draft. Command+K requires a terminal that forwards the Super/Meta key; Ctrl+K works as the portable shortcut. Ctrl+T keeps the visible tool in view when toggling details, including when cards have mixed expanded states.

The command palette lists the current fork family first and highlights the active fork. Use Up/Down and Enter to jump directly. F2 or Ctrl+R renames the highlighted session without switching to it. Saving returns to the refreshed palette with that session selected; Esc cancels and restores the previous query and selection. Opening the palette and pressing F2 immediately renames the active fork.


## Python workflows

Type `/workflow` and press Tab to insert `/workflow ` and open file completion.
Suggestions include Python workflows in `.agents/workflows`, `workflows`, and the
current working directory. Tab on a directory continues completion inside it.
Use `/workflow workflows/review.py`, an absolute path, or a path containing spaces.
The optional `/workflow:workflows/review.py` form resolves its path from the current
working directory. Bare `/workflow` opens the last graph or workflow completion.
The first compilation installs the checksum-pinned WASI runtime on demand using
Go. No host Python, Go toolchain, curl, or C compiler is needed by the built REPL.
Completed runtime installations are cached under XDG_CACHE_HOME indefinitely.

This command executes real work: command subprocesses, child harness agents with
tools/questions, and Worktrunk-created worktrees. Worktree hooks are disabled;
put setup commands in the workflow. Every new or resumed live run asks whether
to approve all its tool operations or approve each one. This run-specific choice
does not change global configuration or bypass explicit workflow approval nodes.
Plan mode rejects execution. The graph panel
supports Space to run/pause, N for the next step, A for approval, and Esc to hide and pause.
Steps execute sequentially within each run; separate workflows can run concurrently.
Each run pauses for approvals or errors. D toggles readable step details and raw
debugging data. P shows the captured full system and user prompts, including loaded
instructions and expanded workflow inputs. Captured prompts are saved with execution
receipts for resume; older runs without snapshots show an authored preview.
Approval choices support mouse clicks and uppercase or lowercase shortcuts.

Agent steps and reusable `Agent` definitions accept `system_prompt_append="..."`.
Repeat checks accept `repair_system_prompt_append="..."` for their repair agent.
These append to the configured system prompt and loaded instructions.

Each opened workflow has its own RHS thread and command-palette entry. Names and
run IDs distinguish runs of the same workflow. Switching between workflows or a
conversation preserves each run's state and automatic advancement. Threads show
when approval or an answer is needed. Persisted runs can be reopened with
`/workflow resume ID`.

`/workflow resume ID` restores saved progress without compilation or runtime
installation. Workflow state lives at `$XDG_STATE_HOME/unreal-agent/workflows`,
falling back to `~/.local/state/unreal-agent/workflows`. Completed steps are reused.
An interrupted dispatched step requires reconciliation and is not automatically
retried. Press R on the interrupted step to inspect evidence and record a verified
result, mark failure, or explicitly allow retry. Recovery opens automatically on
interrupted resume. Every change requires a reason and confirmation; retry requires
`retry STEP_ID`. The run stays paused afterward. Ensure the old executor stopped
before retrying. Matching saved receipts and verified worktrees suggest results
but never commit them automatically. Terminal receipts older than seven days are cleaned in batches only after their
run is absent; unresolved and failed evidence is preserved.
Simulation runs cannot be resumed
as live runs. Commands receive `UNREAL_WORKFLOW_IDEMPOTENCY_KEY`; adapters must
honor it to deduplicate external effects.

See the [workflow guide](../workflow-prototype/docs/src/content/docs/guides/live-workflows.md)
for authoring and execution details. The standalone `cmd/workflow-prototype/run.sh`
remains a simulator; its example command paths are not necessarily executable.
