# Workflow UI and interaction ownership

Use this reference when changing workflow threads, approvals, recovery overlays,
progress reporting, or prompt inspection. For checkpoint and retry semantics,
read [workflow-durability](../../workflow-durability/SKILL.md).

## Separate selection from execution

The selected panel is a view, not the owner of all running work. Route ticks,
results, prompt snapshots, and reconciliation replies to their originating panel.
Accept events for retained background panels; reject events for discarded panels.
Do not switch focus when background work finishes or needs attention.

Give each thread a stable identity before compilation finishes. Distinguish runs
with the same display name. Resuming an already open run selects it without
creating another executor. Navigation preserves automatic advancement, outputs,
and recovery forms. Cancellation targets the selected workflow; application exit
cancels all retained runs. Preserve ordinary conversation admission checks.

When selecting a thread, route questions and approvals to that thread's runtime.
Clear stale question UI before loading the selected runtime's pending question.
Background requests should appear as attention states in the sidebar.

Inspect [panel ownership](../../../../cmd/internal/repl/workflow_panels.go),
[event routing](../../../../cmd/internal/repl/workflow.go), and
[thread navigation](../../../../cmd/internal/repl/workflow_sidebar.go).

## Keep approvals scoped and reachable

A run's child-agent Bash tools and direct workflow commands share its approval
gate. Approve-all applies to that open run, without changing global configuration.
A newly restored run asks again. Explicit workflow review nodes still require
approval. Navigation must neither grant permission nor transfer it between runs.

Keep Ctrl+K reachable while approval or recovery is open. Rendering priority and
input priority must agree: an invisible overlay must not consume keys. A visible
shortcut must accept the terminal's lowercase, uppercase, and Shift forms where
applicable. Route clickable choices through the same action as their keyboard
counterparts. Derive hit regions from rendered, wrapped rows; exclude clipped rows
and the sidebar.

Use [approval input tests](../../../../cmd/internal/repl/workflow_approval_input_test.go)
and [agent approval integration](../../../../cmd/internal/repl/workflow_agent_approval_test.go).
Tests should enter through `uiModel.Update` and verify the resulting transition or
effect. Calling only a leaf handler misses overlay interception and stalled ticks.

## Preserve geometry and readable evidence

Use [workflow overlay rendering](../../../../cmd/internal/repl/workflow_overlay_view.go)
for the shared sidebar layout. Subtract its width once in a copied view model;
keep global terminal dimensions intact. Use that same content width for mouse
coordinates. Sanitize untrusted text before adding trusted ANSI styles.

Default details should explain the task, dependencies, workspace, and result.
Offer raw schema/JSON separately. Omit empty diagnostic sections. Label authored
previews separately from captured execution prompts. A full-prompt view must not
silently apply the short-preview truncation limit.

A static “running” screenshot does not establish a deadlock or approval wait.
Inspect the saved child session and operation results before diagnosing the cause.
Use runtime events to distinguish model waits, tool execution, questions, and
approval waits; do not replace meaningful activity with a session ID alone.

## Verify the interaction boundary

Choose cases relevant to the change:

- Two runs progress while each is unselected; finishing one does not overwrite the other.
- Switch through the palette and sidebar while another run awaits permission.
- Approve or answer in one thread while the other remains waiting.
- Mouse and uppercase shortcuts reach execution, not just a changed view flag.
- Resize an overlay and click its rendered choices without hitting another control.
- Final result handling retains prompt evidence without replaying an older state notice.

Use [multi-run tests](../../../../cmd/internal/repl/workflow_panels_test.go),
[sidebar tests](../../../../cmd/internal/repl/workflow_sidebar_test.go), and
[overlay tests](../../../../cmd/internal/repl/workflow_overlay_view_test.go).
For an interaction regression, confirm the changed path in an isolated real terminal.
Report that evidence separately from HTTP model fixtures and live provider calls.
