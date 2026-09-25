---
name: harness-engineering
description: Implement or debug Unreal Agent harness coordination, durable sessions, tool operations, forks, and model-provider contracts. Use for changes under harness/ or their REPL runtime integration.
---

# Harness engineering

Preserve stored sessions, pending inputs, and unfinished operations when changing
the harness. Diagnose the persisted transition and its consumer before changing
the UI or provider request.

## Preserve the durable boundary

- Tool translation produces inert operation specifications. `Context.Submit`
  records intent without I/O. Dispatch belongs to the coordinator after the tool
  status and operation snapshots are stored. Follow the
  [tool contract](../../../harness/tool/tool.go) and
  [coordinator loop](../../../harness/coordinator/loop.go).
- Keep turn persistence before model dispatch. Keep operation persistence before
  dependent dispatch or acknowledgement. A write error must stop progress at that
  boundary. Do not continue using mutated local state as though it were durable.
- The local session store synchronizes writes before notifying observers.
  Preserve that order when adding visible progress or acknowledgements. Read
  [store publication](../../../harness/sessionstore/localfile/store.go).
- Human answers do not release the model gate when a form closes. The canonical,
  persisted tool status releases it. Check
  [question acknowledgement](../../../cmd/internal/repl/questions.go),
  [question tests](../../../cmd/internal/repl/questions_test.go), and
  [model-gate tests](../../../harness/coordinator/model_gate_test.go).
- Keep gate checks on the coordinator loop. A blocked model request must not
  prevent hard-stop processing.

## Recovery and forks

- Preserve the context builder's submitted prefix and staged suffix. A late tool
  result must remain available after an input write fails. Use
  [submission recovery tests](../../../harness/coordinator/submission_test.go)
  before changing commit, delivery, or steering order.
- Resume unfinished operations from stored snapshots. A stored terminal operation
  supplies its result without rerunning its external action. Check
  [recovery tests](../../../harness/coordinator/recovery_test.go).
- A conversation fork inherits context, not the parent's running operations.
  Close inherited unresolved tool calls with explicit unavailable-result messages.
  Never claim they succeeded or omit their result entries. Check
  [fork behavior](../../../harness/coordinator/fork_test.go) and
  [store fork boundaries](../../../harness/sessionstore/localfile/store_test.go).
- Preserve old record decoding and incomplete-tail recovery when changing storage.
  Read [store codecs](../../../harness/sessionstore/localfile/codec.go) and their
  adjacent tests. New optional fields need legacy omission and roundtrip coverage.
- Stored IDs and idempotency metadata alone do not guarantee exactly-once external
  effects. State the actual executor or provider deduplication boundary.

## Provider-neutral requests

- Put shared request semantics in [llm.Model/Request](../../../harness/llm/model.go).
  Current provider clients share the
  [Responses adapter](../../../harness/llm/responsesapi/request.go).
  Verify the selected client's additional constraints before claiming support.
- Use existing generated wire types. Keep nil options absent from legacy requests.
  Reject invalid requested features explicitly. Do not silently drop settings or
  retry a schema request as unrestricted text.
- `Model.OutputFormat` carries a named JSON Schema and an explicit `Strict` flag.
  `ContextBuilder.SetModel` preserves it. Responses sends it as `text.format`.
  Native enforcement depends on provider and model support.
- Structured output remains message text. Inspect `Response.Stop` for refusal or
  truncation before consuming it. The production adapter does not perform local
  JSON Schema validation. See
  [structured-output wire tests](../../../harness/llm/responsesapi/structured_output_test.go).
- Restore model configuration when reconstructing a builder. Serializing a model
  successfully does not mean the session store persists that configuration.
- Skill loading resolves registered names to paths. Do not ask models to invent
  filesystem locations. Follow [SkillUse](../../../harness/tool/skill_use.go).

## Keep prototype claims separate

The graph, WASI compiler, SQLite storage, and live runner now live in
[harness/workflow](../../../harness/workflow). The REPL executor binds real
commands, child harness agents, and a pluggable Worktrunk backend. The separate
`cmd/workflow-prototype` client still simulates operations. Keep simulator evidence,
shared-engine tests, and real provider/worktree execution claims distinct.

Live dispatch intent is committed before adapter calls. Interrupted dispatched
work stops for reconciliation, without automatic retry. Child agent sessions retain
the existing harness tool/question machinery. Plan mode rejects live execution.

## Validate the changed boundary

From the repository root, choose the relevant focused tests:

```sh
GOCACHE=/tmp/unreal-agent-cli-gocache GOMODCACHE=/tmp/unreal-agent-cli-modcache \
  go test -race ./harness/coordinator ./harness/contextbuilder ./harness/sessionstore/...

GOCACHE=/tmp/unreal-agent-cli-gocache GOMODCACHE=/tmp/unreal-agent-cli-modcache \
  go test -race ./harness/llm/... ./harness/tool/...

go fmt ./...
go vet ./...
```

Repository instructions require formatting and vet in every changed Go module.
Root checks do not cover the prototype. Run its checks separately with
`CGO_ENABLED=0` when changing it.

For durability changes, test failure before persistence, recovery after
persistence, and replay without duplicate work. For provider changes, inspect
serialized requests and refusal/error paths. Report local tests, live provider
requests, and platform smoke checks separately. HTTP fixtures do not establish
live authentication, model compatibility, or successful external side effects.
