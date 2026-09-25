---
title: Run workflows in the REPL
description: Execute Python workflow graphs with real harness agents, commands, and isolated worktrees.
---

`/workflow` compiles Python authoring code inside WASI, then executes the graph
through the harness. This differs from `run.sh`, which remains a simulator.

:::caution[Real operations]
REPL workflows create worktrees, execute command argument vectors, and run agents
with harness tools. Simulation examples can name nonexistent programs or illustrative
deployments. Review and adapt them before live execution. Plan mode rejects execution.
:::

## Invoke a workflow

Type `/workflow` and press Tab to insert `/workflow ` and open file completion.
Suggestions include Python workflows in `.agents/workflows`, `workflows`, and the
current working directory. Selecting a directory continues completion inside it.
Absolute paths and paths containing spaces also work. The optional colon form
resolves its path from the current working directory.

```text
/workflow workflows/review.py
/workflow /absolute/path/to/review.py
/workflow:workflows/review.py
/workflow resume RUN_ID
```

Bare `/workflow` opens the previous workflow panel, or starts workflow completion
when no panel exists. Before starting, choose A to approve all tool operations
for this workflow run or Y to approve each operation. Resume asks again. This
choice does not change global configuration, and explicit workflow approval nodes
still stop. Plan mode remains blocked. After the choice, execution advances
automatically and sequentially, even when the graph permits concurrent work.
This repository includes `workflows/review.py`, a live example discoverable as
`/workflow workflows/review.py`. It reviews committed code in an isolated worktree and returns
structured findings for acknowledgement. It requires a configured model.

The first compilation downloads checksum-pinned CPython WASI and pure Python
Pydantic dependencies. Installation uses Go, requires no host Python, curl, or C
compiler, and caches completed installations indefinitely. Cache location:
`$XDG_CACHE_HOME/unreal-agent/workflow-prototype`, falling back to `~/.cache`.
The graph-authoring SDK is embedded in the REPL binary; no repository checkout is
needed to supply it.

## Inspect and control execution

The graph panel shows step state and details. Use its selection controls to inspect
inputs, results, errors, and dependencies. Details use a readable summary by
default. D switches between the summary and raw debugging details. P shows the
full captured system and user prompts for the selected agent attempt. Snapshots
are saved with execution receipts and restored on resume; runs without snapshots
show a labeled authored preview. Approval choices also accept mouse clicks.

| Key | Action |
| --- | --- |
| Space | Toggle automatic advancement |
| N | Advance the next step |
| A | Approve the selected ready approval |
| R | Recover the selected interrupted step |
| D | Toggle readable and raw details |
| P | Toggle the full captured system and user prompts |
| Esc | Hide the panel and stop automatic advancement |
| Up/Down, PgUp/PgDn | Select steps in dependency order |
| Left/Right | Scroll the selected step's details |
| Ctrl+C | Cancel the current operation; press twice consecutively to exit |

Automatic execution pauses for required approval or after an error. Pausing or
hiding prevents later steps from starting; it does not cancel an operation already
in progress. Agent steps use child harness sessions, including
tools and human-question handling. Structured agents request a native schema
response, then validate the returned JSON locally. Provider schema support varies.

## Navigate workflow and conversation threads

Each opened workflow registers as a separate thread in the full-height RHS menu.
Click an entry to open its graph; click a conversation session to return to that
conversation. The command palette lists every open workflow by name and run ID,
so repeated runs of the same workflow remain distinguishable. Switching threads
preserves each workflow's state and automatic advancement. It does not cancel or
restart a run. Thread status calls out pending approvals and questions.

Starting or resuming another workflow keeps existing panels available. After a
REPL restart, reopen persisted runs with `/workflow resume RUN_ID`.

## Workspace and command behavior

The default pluggable worktree backend uses Worktrunk. Branch names are stable for
a workflow attempt, and the base resolves to a commit before creation. Existing
matching worktrees can be reused. Creation uses `--no-cd --no-hooks`: declare setup
as workflow steps rather than relying on automatic Worktrunk hooks.
The run pins workspace bases to commits before execution. Uncommitted changes in
the source checkout are not copied into those worktrees.

Commands run their argument vector directly in the selected worktree. They do not
implicitly use a shell. A shell command requires an explicit shell executable.
The adapter adds `UNREAL_WORKFLOW_IDEMPOTENCY_KEY` to the inherited environment.
This is an external operation key; internal checkpoint keys are not exported.
In edit mode, direct commands and worktree creation also require approval.

## Resume without repeating completed work

Workflow runs use the shared SQLite store under
`$XDG_STATE_HOME/unreal-agent/workflows`, or `~/.local/state/unreal-agent/workflows`.
Copy the run ID from the panel and invoke `/workflow resume RUN_ID`.
Resume loads the immutable saved graph without Python compilation or downloading
the runtime. Simulator runs and live runs cannot be resumed in the opposite mode.
Resume requires the original canonical workspace and restores the saved model,
provider, prompt, and other execution settings. Credentials remain in their normal
environment or login store rather than in the workflow snapshot.

The runner commits dispatch intent before an external operation. Successful
results are saved before completion is reported. Completed steps retain their
results and are not rerun on resume.

An interrupted dispatched operation can have succeeded externally without a saved
result. The runner stops with `ErrReconciliationRequired` rather than retrying it.
Recovery opens automatically on an interrupted resume, or with R on the selected
interrupted step. Automatic execution stays paused until you make and confirm a
decision. Keys alone cannot prove exactly-once effects; adapters must honor their
own deduplication and reconciliation contracts.

See [storage semantics](/explanation/storage/) for retention and transaction boundaries.


## Reconcile an interrupted step

Recovery begins with an asynchronous, read-only evidence check. It does not rerun
commands, create worktrees, or change the workflow checkpoint.

A saved terminal receipt must match the operation identity, inputs, and executor
configuration before its result becomes a candidate. Worktree inspection can also
suggest a workspace when the exact workflow branch is registered, its checkout is
clean, and its HEAD matches the saved base. Suggestions are never committed automatically.

A pending command receipt does not prove success or failure. A mapped agent
session identifies where to inspect evidence; its existence does not prove a final
result. Verify unresolved outcomes externally before selecting an action.

| Action | Effect |
| --- | --- |
| Check saved evidence | Refresh read-only evidence without replaying work |
| Record verified result | Validate and save a result you independently verified |
| Mark failed | Save a failed outcome and leave dependent work blocked |
| Retry this attempt | Permit another dispatch using the same keys and inputs |

A verified result uses the executor envelope, not only the agent's output:

```json
{"output": {"approved": true}, "workspace": "", "exit_code": 0}
```

Use the step's actual output schema. A worktree result supplies `workspace`;
a command result includes its observed `exit_code`. The form validates the envelope
and applicable schema. It does not determine whether your evidence is true.

Every state-changing decision requires a reason, review, and explicit confirmation.
The reason is persisted in the recovery audit. Fields support the same readline
editing controls as the composer. Tab changes fields. Ctrl+Enter or Alt+Enter opens
review; Enter from the reason field also reviews. Retry additionally requires typing
exactly `retry STEP_ID`, with the selected step's ID, before confirmation.

Before retrying, ensure the previous external executor has stopped and verify its
effects or deduplication contract. Reusing a key cannot make a non-idempotent
external service safe. The run remains paused after recovery; press N or Space to
continue when ready.

Execution receipts are separate evidence files. After run cleanup, up to 100
terminal receipts older than seven days are removed only when their run is absent.
Active, unresolved, failed, malformed, and legacy receipts without run identity
are preserved. This policy is not a hard disk quota.
