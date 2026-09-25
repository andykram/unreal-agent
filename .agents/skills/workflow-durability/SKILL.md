---
name: workflow-durability
description: Extend or debug workflow checkpoints, crash recovery, memoization, retention, and executor idempotency in Unreal Agent. Use when changing persisted workflow state or adding execution adapters.
---

# Workflow durability

Work from the repository checkout containing this skill. Verify the current implementation before treating these contracts as facts about a newer revision.

## Establish the boundary

The shared engine and SQLite store live in `harness/workflow`. The separate
`cmd/workflow-prototype` module simulates work. Applications can implement the
generic executor interface; concrete adapters are not included. Workflow storage
does not replace the harness's local-file session store. Resume restores the saved graph and execution
mode without rerunning Python; live and simulator runs cannot cross modes.

Read the relevant implementation before changing it:

- [execution.go](../../../harness/workflow/execution.go): live intent commits, dispatch, result persistence, approval, and interrupted-operation rejection.
- [simulator main](../../../cmd/workflow-prototype/main.go): simulation checkpoint ordering and cleanup callers.
- [persistence.go](../../../harness/workflow/persistence.go): transactions, revisions, schema ownership, retention.
- [execution_keys.go](../../../harness/workflow/execution_keys.go): internal identity and the external key boundary.
- [storage contract](../../../cmd/workflow-prototype/docs/src/content/docs/explanation/storage.md): current guarantees and limits.

For Python graph changes, use [workflow-library](../workflow-library/SKILL.md).

## Preserve recovery semantics

- Keep the graph immutable within a run. A fresh run receives a new ID and preserves the previous run. Editing the authoring script must not silently change a resumed run.
- Save the settled state before acknowledging a transition. A failed write or stale revision must stop that process before acknowledgement. Do not overwrite a newer writer's state by retrying with its revision and stale contents.
- Preserve outputs, resolved inputs, check phase, repair count, attempt history, and execution keys. Persisted generic JSON uses `UseNumber`; float64 would corrupt integers above 2^53.
- A completed result can be reused within its run. Cross-run memoization needs an explicit identity contract covering relevant code, prompts, model settings, skills, inputs, and workspace state. Do not infer that contract from a step name.
- Keep old records readable when adding fields. Changing key derivation, graph interpretation, or schema versions needs a migration or an explicit incompatibility path. Do not silently reinterpret unfinished work.

## Isolate external identities

Internal `IdempotencyKey` and `AttemptKey` identify checkpoint work. External adapters receive an opaque key from `executorKey`, scoped by executor, attempt, and operation. Ordinary caller operation names must stay inside that namespace.

- Retry the same operation with the same identity. Give different operations stable distinct names within an attempt.
- Before integrating a live adapter, bind the saved identity to its request payload. Reject changed arguments under an existing key. A transport retry must not create a new logical operation.
- Deliberate repeat-check phases use different keys: check 1, repair 1, check 2. Resume preserves the current attempt and repair budget.
- `UnsafeRawKey` is the explicit isolation escape hatch. Keep it out of ordinary untrusted executor inputs; it must never replace internal checkpoint identity.
- Commit the attempt before dispatch. On recovery, reconcile external work before resubmitting it. An external success followed by a local crash is the critical ambiguity.
- The key helper checks identity presence, not proof of commit. Live `Next`
  commits before dispatch. An unresolved dispatched node returns
  `ErrReconciliationRequired`. Callers must verify evidence before using `Reconcile`, which requires a reason
  and preserves an audit history. Retry preserves keys and inputs. Preserve this
  fail-closed automatic boundary.
- Verify the remote service's deduplication lifetime and lookup behavior. If a result is ambiguous and that protection has expired or is absent, reconcile or stop instead of blindly retrying. Local cleanup must preserve information needed for that reconciliation.

Keys and revision checks alone do not provide exactly-once effects or execution leases. State precisely what a newly integrated executor actually honors.

## Keep storage portable and bounded

Preserve `CGO_ENABLED=0`. Current persistence uses `modernc.org/sqlite`, WAL, FULL synchronous commits, immediate transactions, and per-connection busy timeout. Check driver behavior before changing these settings.

Cleanup must exclude every open run, including the active resume target, and retain failed, blocked, and waiting runs. Only fully completed/skipped runs become retention candidates. Re-saving completion must not extend its original retention timestamp. Bound deletion and vacuum work; do not call retention a hard disk quota. Enable incremental vacuum before creating tables.

Keep database ownership/version checks and private file permissions. Report snapshot costs as proportional to accumulated state size until measured. Do not generalize startup or rendering timings into database performance claims.

## Verify the changed contract

Run focused `CGO_ENABLED=0 go test ./harness/workflow` and
vet from the root. Format changed Go files. Also run cgo-disabled build/vet in
`cmd/workflow-prototype` when changing its client. Root checks do not cover that
nested module.

Choose the relevant recovery scenario instead of only testing graceful exit:

| Change | Evidence to collect |
| --- | --- |
| Checkpoint/resume | Accept output, wait for acknowledgement, send SIGKILL, resume with an unavailable Python runtime. Compare saved output, resolved inputs, keys, and revision. |
| Transaction/write ordering | Interrupt before commit and after commit; force write failure. Only committed state may be acknowledged or restored. |
| Writer coordination | Two readers of one revision compete to save. One succeeds; the other fails without overwriting. |
| Repeat checks | Kill during repair and next check. Resume must neither duplicate repair nor consume another repair budget slot. |
| Key isolation | Retry stability, separate executors/operations/runs/attempts, internal/external separation, explicit raw override, missing identity rejection. |
| Retention | Expired completed runs are removed in bounded batches; exclusions and unfinished runs survive. |
| Serialization | Round-trip an integer such as 9007199254740993 through outputs and downstream inputs. |

Use isolated temporary state for destructive recovery experiments. Use committed shared-engine tests and isolated fixtures; do not depend on another
developer's `/tmp` artifacts. Add adapter recovery regression coverage alongside integrations.

Update the storage explanation and resume tutorial when guarantees change. Separate simulated transitions, process-crash recovery, power-loss guarantees, and live external effects in the handoff.
