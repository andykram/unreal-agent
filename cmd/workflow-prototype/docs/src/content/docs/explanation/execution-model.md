---
title: Execution model
description: Understand static graphs, runtime outcomes, skipped branches, and prototype limits.
---

This page describes the standalone simulator. The shared graph, validation, and
storage implementation also powers [live REPL workflows](/guides/live-workflows/).
Live execution uses real adapters and commits dispatch intent before external work.

## Python describes work; Go owns state

Python builds a complete graph and exits before simulation begins. Python loops
can generate multiple steps during authoring. They do not wait for step results.
Conditions in the graph let Go choose branches from later runtime outcomes.

```text
Python script → WASI interpreter → JSON graph → Go validation → simulation
```

This separation makes every declared dependency available to the viewer. It does
not require keeping the Python interpreter alive during simulated execution.

## Dependencies and batches

An ordinary step is ready when every dependency has status `completed` and its
condition, if present, matches. An empty `after` list permits a root step.

`n` takes a snapshot of ready nonapproval steps. These steps advance together.
Newly ready children wait for a later batch. This models possible concurrency;
it does not launch parallel processes or agents.

Workspace metadata alone does not enforce a dependency. The `agent` helper adds
its workspace only when `after` is empty. Commands and checks require explicit
dependency arguments.

## Check results differ from execution errors

| Event | Status | Outcome | Downstream behavior |
| --- | --- | --- | --- |
| Normal simulated step finishes | `completed` | `passed` | Dependencies satisfied |
| Approval accepted | `completed` | `approved` | Dependencies satisfied |
| Check passes | `completed` | `passed` | Passing branch selected |
| Check exhausts repairs | `completed` | `failed` | Failing branch selected |
| `f ID` execution error | `failed` | No completed result | Dependents remain blocked |
| Branch is not selected | `skipped` | Empty | Ordinary dependents skip; joins can converge |

The `repeat_check` node alternates between `check` and `repair` phases.
Negative results start repairs until the budget is exhausted. Each repair
increments the count and prepares another check. History records these events.

An operational failure is not a negative check result. The workflow cannot
select a branch from an operation that never completed.

## Skipping and joins

A completed source with a mismatched outcome skips its conditional branch.
Skipping propagates through ordinary dependencies. Thus an ordinary node that
depends on both mutually exclusive branches is skipped too.

Use `join` for convergence. It waits until every input is completed or skipped,
and becomes ready when at least one input completed. If every input skips, the
join skips. An execution failure blocks the join even if another input completed.

```text
                 ┌─ approved ─┐
check outcome ───┤            ├─ join ─ summary
                 └─ escalated ┘
```

Joins do not merge files or carry output values. They also do not inspect check
outcomes: a completed negative check satisfies a join input. Join branch endpoints
that encode the decision you want, rather than interpreting a join as success.

Generic loops, arbitrary condition expressions, live graph edits, and user-defined
executors are not implemented.

## Structured output

An agent with `output_schema` enters status `running` and phase `output` when
advanced. It waits for `o ID JSON`, which validates the supplied value before
storing it in the node's `Output` field and completing the agent. Invalid output
leaves it waiting. Automatic mode leaves that agent pending and advances other
ready work.

The prototype validates manually supplied JSON. It does not make a model request.
Output values are displayed and persisted with the run. Output references can supply fields to later agents, and scalar equality
conditions can select branches. The authoring Python script still exits before
these runtime values exist.

## State and recovery

Normal runs persist their immutable graph, node states, outputs, resolved inputs,
and repair history. Resume restores the saved run without rerunning Python.
Completed results remain completed and are not executed again within that run.

`r` creates a new run from the graph. `q` exits and preserves the current run.
A JSON-only export does not create run state.

This proves durable simulation state. Real side effects, workspace ownership,
and reconciliation for external operations still need production integration.
See [workflow storage](/explanation/storage/) for transaction and cleanup details.

## WASI boundary

The host supplies the runtime directory, embedded `harness.py`, and selected
script. It does not mount the repository, forward host credentials, or inherit
the host environment. It supplies `PYTHONHOME` explicitly.

Interpreter execution has a 30-second context deadline and a 256 MiB Wasm
linear-memory limit. These are not total process limits. In particular, captured
stdout uses host memory. This is a feasibility sandbox, not a completed security assessment.

Arbitrary Python interpreter checkpointing is unsupported. Native package
compatibility needs separate validation. Worktrunk is metadata in the simulator;
the REPL supplies a real pluggable worktree backend.
