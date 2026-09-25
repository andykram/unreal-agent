---
name: workflow-library
description: Extend the repository's Python WASI workflow DSL, structured dataflow, skill invocation builders, and annotated developer examples. Use for workflow-library API or simulator changes, not ordinary agent tasks.
---

# Workflow library

Shared implementation lives in `harness/workflow` in the root Go module. Python
authors a graph and exits. `cmd/workflow-prototype` remains a separate simulator
client. The REPL `/workflow` runs live commands, child agents, and worktrees through
[its executor](../../../cmd/internal/repl/workflow_executor.go). Validate the root
module when changing shared code, and the simulator module when changing its client.

## Follow the contract across layers

For API changes, inspect the affected paths before editing:

- [harness.py](../../../harness/workflow/harness.py): builders and dependency inference.
- [graph.go](../../../harness/workflow/graph.go) and [dataflow.go](../../../harness/workflow/dataflow.go): validation, transitions, references, conditions.
- [generated stubs](../../../cmd/workflow-prototype/typing/generate_stubs.py): editor API must match runtime signatures.
- [Python reference](../../../cmd/workflow-prototype/docs/src/content/docs/reference/python-api.md): public contract.

Keep old graphs and stored runs readable when extending fields. A new builder
alone does not create executor behavior. Check serialization, validation, runtime
resolution, examples, and stubs together. Do not introduce a second workflow truth
inside Python or the viewer.

## Dataflow invariants

- `Agent.bind` adds its workspace dependency. Legacy `flow.agent` adds the
  workspace only when `after` is empty; preserve that distinction.
- `chain` passes `previous`; `context` persists through every stage. Carry source
  material explicitly when a later stage needs more than its predecessor's result.
- `map_agents` expands a known collection during authoring. Runtime-sized fan-out
  requires a new execution mechanism, not a Python loop around an unresolved ref.
- `reduce_agent` receives actual outputs as `inputs.results`. Nested references
  infer dependencies. Missing optional fields or array indexes fail at resolution;
  do not invent values. Ambiguous multi-model union paths are rejected.
- Ordinary nodes skip when any dependency skips. `join` waits for all inputs to
  complete or skip, requires one completed input, and skips when all skip.
  Execution failure blocks a direct join input. Convergence is not authorization.
- Exhausted checks are completed with outcome `failed`; execution errors have
  status `failed`. Preserve this distinction in branches, approval gates, and tests.

## Real schema and skill boundaries

[output_model.py](../../../harness/workflow/output_model.py) wraps real
Pydantic. The bundled pure Python release is currently 1.10.26; inspect
[dependency installer](../../../cmd/workflow-prototype/install-python-deps.sh)
and [Go runtime installer](../../../harness/workflow/runtime_install.go) before
changing versions. Keep their pinned packages aligned. Python 3.14 support is limited. Pydantic v2 needs
`pydantic-core`; a package working on the host does not establish WASI support.

Go [structured validation](../../../harness/workflow/structured.go) uses
exported JSON Schema, including local references and constraints. It does not run
Python custom validators, insert defaults, or perform Pydantic coercion. External
schema references are rejected. Keep provider schema compatibility separate from
local validation and simulated output acceptance.

[Skill builders](../../../cmd/workflow-prototype/typing/generate_skills.py) infer
arguments from explicit source conventions and record evidence/confidence.
Keep prose-only skills on raw-string fallback. Builders return exactly
`{name, arguments}` descriptors with quoted argument text; they do not execute
skills. Catalog-only stubs constrain names but infer no argument contract.
Regenerate both runtime and `.pyi` outputs after changing inference. Read the
[typing guide](../../../cmd/workflow-prototype/typing/README.md) for fixtures.

## Additive prompts and execution evidence

`system_prompt_append` applies to agent steps and reusable `Agent` bindings;
`repair_system_prompt_append` applies to repeat-check repair agents. Preserve the
configured base prompt, workspace instructions, skill preamble, and mode restrictions.
Validate field types and applicable step kinds in Python and Go. Keep generated
stubs and the live executor aligned; successful graph export alone does not prove
that an instruction reaches the model.

Inspect [runtime prompt assembly](../../../cmd/internal/repl/app.go),
[executor integration](../../../cmd/internal/repl/workflow_executor.go), and
[prompt snapshots](../../../cmd/internal/repl/workflow_prompt.go). Prompt inspection
uses captured system and expanded user text for that attempt. Do not reconstruct a
historical prompt from today's files and label it as executed. Legacy runs may lack
snapshots. Verify provider-bound content and restored evidence with the
[HTTP fixture](../../../cmd/internal/repl/workflow_agent_approval_test.go).

## Examples and documentation

Use [delivery_pipeline.py](../../../cmd/workflow-prototype/examples/delivery_pipeline.py)
for map/chain/reduce composition and [its input tape](../../../cmd/workflow-prototype/examples/delivery_pipeline.inputs.txt)
for a coherent simulated success path. Submitted conclusions are not proven facts.

The [complex-examples page](../../../cmd/workflow-prototype/docs/src/content/docs/guides/complex-examples.mdx)
imports runnable source directly. Update annotation markers when source changes;
missing markers deliberately fail the build. Do not replace imports with copies.

## Validate the changed boundary

For shared code and live integration, run focused root-module checks:

```sh
CGO_ENABLED=0 go test ./harness/workflow ./cmd/internal/repl
CGO_ENABLED=0 go vet ./harness/workflow ./cmd/internal/repl
```

Run these client checks from `cmd/workflow-prototype` when relevant:

```sh
CGO_ENABLED=0 go fmt ./...
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
./run.sh -script examples/delivery_pipeline.py -json
workflow_state_dir=$(mktemp -d)
./run.sh -script examples/delivery_pipeline.py -state-dir "$workflow_state_dir" < examples/delivery_pipeline.inputs.txt
ASTRO_TELEMETRY_DISABLED=1 npm run build --prefix docs
```

The root module's `go test ./...` does not cover the simulator module. WASI export
checks authoring and validation; the tape additionally checks simulated dataflow.
For branch changes, exercise rejection and blocked-operation paths too. For
persistence or execution-key changes, read [storage semantics](../../../cmd/workflow-prototype/docs/src/content/docs/explanation/storage.md)
and verify resume/crash behavior. Stable keys require external adapters to honor
them; stored keys alone do not establish exactly-once effects.
