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
provide exactly-once execution of external tools or agents. The standalone simulator does not perform external work. The generic `Next` API
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
themselves. Concrete adapters are supplied by applications. The generic `Next` API commits
the attempt first and derives its external key through this boundary. Pass only
that external key to the adapter. Adapters must honor it and reconcile interrupted requests.
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

## Generic execution boundary

The shared `Next` API commits dispatch intent before calling an application-supplied
executor. A recovered dispatched operation without a committed result returns
`ErrReconciliationRequired`; it is not automatically retried. `Reconcile` records
an explicit decision with a reason and audit history. Callers must verify external
evidence and establish that the previous executor stopped before retrying.

Saved execution mode separates simulator and live runs. No concrete command,
agent, worktree, receipt store, or recovery UI adapter is included at this layer.
The harness's local-file session store remains separate. Cross-run result caching,
distributed scheduling, and exactly-once external effects are not provided.

See [CLI reference](/reference/cli/#workflow-run-storage) for storage paths and
[resume tutorial](/tutorials/resume-workflow/) for a concrete simulator walkthrough.
