---
title: Workflow storage
description: Understand saved graphs, run checkpoints, memoization, and automatic retention.
---

## One immutable graph per run

A normal workflow invocation authors and validates a graph, then starts a saved
run. Its graph does not change. Resume loads that graph and its latest committed
state without rerunning Python. Editing the original script affects future runs.

The store records node status, outcomes, structured outputs, resolved inputs,
check phases, repair counts, and history. Accepted state transitions are committed
before the viewer reports their saved state. Invalid input does not complete a node.

Completed node results are reused within the resumed run. This is per-run
memoization, not a cache shared between different workflows or runs. Reset starts
a new run from the same graph and preserves the old one.

## Transaction boundary

The shared workflow store uses SQLite through the pure Go `modernc` driver. Builds remain
cgo-free. Transactions use WAL mode and FULL synchronous commits. Updates compare
the saved revision with the revision loaded by the process, then advance it.
A stale process cannot silently overwrite a newer checkpoint.

This revision check is not a distributed execution lease. Persistence does not
provide exactly-once execution of external tools or agents. The standalone simulator does not perform external work. Live REPL execution
commits dispatch intent first and stops on unresolved dispatched work after a
crash. It does not automatically retry ambiguous external operations.

A crash can discard an uncommitted transition. Resume restores the last committed
checkpoint. Python itself is not checkpointed: it already finished authoring the
saved graph before simulation began.

## Automatic idempotency keys

The harness assigns started nodes an `IdempotencyKey` derived from the run ID
and step ID before checkpointing. Pending and skipped nodes have no keys. It remains stable when the run resumes. Starting a new run produces
new keys, even when its graph has the same step IDs.

An ordinary node's `AttemptKey` is its logical idempotency key. A repeated check
or repair has a distinct attempt key incorporating its phase and attempt number:
check 1, repair 1, check 2, and so on. A terminal check keeps its final attempt key.
The node retains its `AttemptKeys` history, so a resumed attempt keeps its identity
while a later check or repair receives a different identity.

Internal keys belong to checkpoint bookkeeping. Do not send them directly to an
external executor. The shared `ExecutorKey` boundary derives an opaque
`ext-v1-` key from the executor name, durable attempt key, and operation name.
The same operation on retry receives the same external key. Different executors,
operations, attempts, and runs receive separate keys. Operation names are local
to the attempt; callers do not need to make them globally unique.

`ExecutorKeyOptions.UnsafeRawKey` is the explicit escape hatch for intentional
key sharing. It passes that value through unchanged, bypassing external namespace
isolation. It never changes the internal checkpoint identity. Keep this override
out of ordinary executor inputs. Even the override requires an existing attempt
and explicit executor and operation names.

Keys identify work; they do not execute or deduplicate an external operation by
themselves. There are no live adapters yet. When added, dispatch must commit the
attempt first, derive its external key through this boundary, and pass only that
key to the adapter. Adapters must honor it and reconcile interrupted requests.
The runner does not claim exactly-once effects.

## Retention and disk use

The default retention interval is 168 hours from completion. Cleanup runs at
startup, then after a changed-state checkpoint when at least one minute passed
since cleanup. It excludes the current process's active run and removes only
older runs whose nodes are all completed or skipped. Each pass removes at most
100 runs.

Runs with failed, blocked, or awaiting work are retained. A completed negative
check still has status completed; its outcome alone does not prevent cleanup
when the whole graph has settled.

The database uses incremental vacuum and passive WAL checkpointing after cleanup.
These reduce reclaimable storage but do not impose a hard disk quota. Long-lived
unfinished runs and large saved outputs can continue to consume disk space.

## Scope

This database is shared by the workflow simulator and live REPL workflow runner.
A saved execution mode prevents opening simulator runs as live runs, or vice versa.
The harness's existing local-file session store remains separate and stores child
agent sessions. Cross-run result caching and distributed scheduling are not provided.

The live runner refuses unresolved dispatched work with `ErrReconciliationRequired`.
The REPL recovery panel inspects evidence and offers verified-result, failure,
and retry decisions. State changes require a reason and explicit confirmation;
retry also requires the exact step-specific confirmation phrase. Decisions are
audited. Completed results can be reused; unknown external outcomes are never
automatically converted into success or retried.

## Execution receipts and evidence

The live executor records operation receipts separately from SQLite checkpoints.
A matching terminal receipt can preserve a result across the gap between external
completion and saving the workflow node. Matching includes operation identity,
inputs, and executor configuration. Pending receipts do not establish completion.

Read-only recovery probes may suggest a saved terminal result or verified clean
worktree at its pinned base. The suggestion still requires explicit acceptance.
A mapped child-session ID is a pointer to evidence, not proof of agent completion.
Evidence probes never replay commands or create worktrees.

Retry preserves the existing attempt's identity and inputs. It does not increment
a repair phase or create a new logical operation. Ensure the prior executor has
stopped before retrying; revision checks alone do not stop an old external process.
The run remains paused after reconciliation until resumed by the user.

After run cleanup, a separate receipt pass deletes at most 100 terminal receipts
older than seven days, and only after confirming their run is absent. Active runs,
existing runs, failed or unresolved receipts, malformed records, and legacy receipts
without run identity are preserved. SQLite vacuum does not remove receipt files.
This retention policy does not impose a hard disk quota. See
[recovery controls](/guides/live-workflows/#reconcile-an-interrupted-step).

See [CLI reference](/reference/cli/#workflow-run-storage) for storage paths and
[resume tutorial](/tutorials/resume-workflow/) for a concrete walkthrough.
