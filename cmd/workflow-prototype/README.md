# Python WASI workflow prototype

Throwaway experiment: can real CPython author a workflow graph inside a Go
application built with `CGO_ENABLED=0`, and can Go visualize its dependencies?

From the cli-repl worktree:

```sh
make workflow-prototype
```

Requires Go, curl, unzip, and shasum. The first run downloads a checksum-pinned
CPython 3.14.7 WASI build into `$XDG_CACHE_HOME/unreal-agent/workflow-prototype`
(or `~/.cache`). No locally installed Python or C compiler is needed.
Real Pydantic 1.10.26 and typing-extensions are installed as checksum-pinned
pure-Python wheels in the WASI runtime. The Go dependencies are isolated in this directory's module.

Edit `example.py` to experiment. It uses a loop to generate concurrent checks.

```sh
cd cmd/workflow-prototype
./run.sh -script example.py -json  # validated graph, no viewer
./run.sh -auto                    # simulate until approval
./run.sh                          # interactive viewer
```

The viewer displays every step, dependency, specification, and state.
Enter `n` to complete the next ready batch, `a` to approve ready approvals,
`f test` to fail a ready step, `r` to reset, or `q` to leave.
Execution is simulated. Commands, agents, worktrees, and integration are not run.
An approval named `integrate` records simulated consent only.

## Result

Verified on macOS arm64 with Go 1.27.1, wazero 1.12.0, and CPython 3.14.7.
The interpreter reported `sys.platform == 'wasi'`; binary metadata confirmed
`CGO_ENABLED=0`. Before Pydantic, initial Wasm compilation and graph export took about 1.7 seconds.
Current Pydantic-backed exports take roughly 3–5 seconds on this host.
The host reads the selected script, provides the embedded `harness.py` module,
and reads one JSON document from guest stdout. Print diagnostics to stderr.

Go rejects unsupported graph versions, unknown step kinds, duplicate IDs,
missing dependencies, and cycles. Per-kind specification validation is deferred.
Python gets the runtime directory, SDK modules, selected script, and an optional
explicitly supplied generated skill module. It does not receive the repository,
credentials, shell, or inherited environment. Execution has a 30-second timeout
and a 256 MiB Wasm linear-memory limit. These are not total process resource limits.
This is a feasibility sandbox, not a production security assessment.

The experiment supports Python authoring a static graph. It does not establish
arbitrary Python checkpoint/resume, dynamic graph updates, package compatibility,
Linux execution, or production harness integration. No Wasm compilation cache
is implemented. Worktrunk is only graph metadata here.

Runtime artifact: https://github.com/brettcannon/cpython-wasi-build/releases/tag/v3.14.7
This is an unofficial build. The archive includes CPython's LICENSE.

Next decision: keep graph construction in Python and execution in Go, or explore
an imperative Python API that requires replay/checkpoint semantics. Delete or
absorb this prototype after making that decision.

## Runtime branches and bounded repair

Question: can Python construct a static graph whose decisions use runtime
results, without keeping the Python interpreter alive?

```sh
make workflow-prototype-branches
```

`branching.py` describes a check/repair node with two allowed repairs. Go owns
its state. Run `n` three times to reach the first check. Then use:

- `p verify`: the check passes. Integration approval becomes ready.
- `b verify`: the check returns a negative result. A repair becomes ready.
- `n`: simulate that repair and prepare the next check.
- `f verify`: simulate an execution error. Dependent steps stay blocked.
- `a`: approve a ready approval. It does not integrate code.

A maximum of two repairs means three checks. Exhaustion completes the node
with outcome `failed` and selects escalation. This is different from an
execution error, which sets status `failed` and blocks both branches.
Pass/fail inputs outside a waiting check are ignored. History retains each
check and repair. Reset starts a new durable run and preserves the previous run.

Conditions support `passed` or `failed` from a `repeat_check` node, and scalar
equality on structured result fields via `flow.ref(step, "field").equals(value)`.
The Python API adds that node as a dependency automatically. Unselected branches
and ordinary descendants become `skipped`. Use `flow.join(id, after=[...])` to
converge alternatives: it waits for all direct inputs to complete or skip and
requires at least one completed input. All-skipped inputs skip the join; a direct
execution failure blocks it. A join is not a success gate. Branch destinations
in the small branching example remain terminal alternatives. `-auto` supplies passing check results and stops at approval.

Verified with actual WASI execution: first-check success, repair then success,
two-repair exhaustion, and operational failure during repair. The checks used
independent processes in parallel. `go vet` passed. These are smoke observations,
not durable recovery or real command execution evidence.

Conclusion: static graph construction can express these runtime decisions.
A narrow `repeat_check` primitive is the only loop implemented. Its introduction
is a first-use pattern confined to the prototype. General loops remain unsupported. The explicit join now supports convergence
without changing ordinary dependency behavior.

The Go simulator now persists the immutable graph and execution state in SQLite,
including results, attempts, phases, outcomes, and approvals. Resume with
`./run.sh -resume RUN_ID` restores the last checkpoint without Python authoring.
External execution and workspace ownership reconciliation remain unimplemented.
See the [storage contract](docs/src/content/docs/explanation/storage.md) for
retention, automatic keys, executor isolation, and recovery guarantees.

## Developer documentation

The [Astro Starlight site](docs/README.md) includes a quickstart, Python API
reference, branching tutorial, graph schema, and runtime semantics.

## Complex examples

See [the example collection](examples/README.md) for larger graphs with isolated
workspace descriptions, conditional checks, bounded repairs, approvals, and
branch convergence. Every operation is still simulated.

## Skills and structured results

Agents accept `skills=["diagnose"]`. Repair nodes accept
`repair_skills=["diagnose"]`. These are name references, not paths or embedded
skill contents. The prototype validates their shape, but does not consult the
live registry or execute skills. See [catalog-specific type stubs](typing/README.md).

Use real `pydantic.BaseModel` classes with `output=YourModel`. `OutputModel` is
now a thin compatibility adapter over Pydantic, not a separate validator.
Pydantic 1.10.26 runs in WASI using its pure-Python wheel. V2's pydantic-core has
no published WASI wheel. V1 has limited Python 3.14 support; the exercised models
work, but this is not a blanket package-compatibility claim.

Go validates the exported JSON Schema using jsonschema/v6, including local
references and constraints. Pydantic custom validators, coercion, and default
insertion run only when explicitly parsing with Pydantic in Python. The Go
simulator does not run Python parsing on submitted outputs. External schema
references are rejected.
```sh
./run.sh -script examples/structured_review.py
```

Enter `n` twice, then `o review JSON` with a result matching the exported schema.
Invalid output leaves the agent waiting. Valid output is durably checkpointed and
unblocks its approval. Automatic mode waits at structured output rather than
fabricating data. `flow.ref(step, "field")` passes result fields through nested
agent inputs and automatically adds dependencies. Missing optional values fail
the consuming step explicitly instead of inventing a value.

Separately, the real harness now accepts `llm.Model.OutputFormat` with a name,
JSON Schema map, and explicit strict boolean. Current provider adapters send
that request through Responses `text.format`. Provider support varies; errors
surface without a fallback. Responses still contain message text. Check stop
reasons and validate data before consumption. No live provider compatibility
was established by the wire tests. The prototype does not call these adapters.

## Agent composition

`Agent` is a reusable prompt/output/skill definition. Bind it into independent
steps, compose `(name, Agent)` stages with `flow.chain`, fan out a known list with
`flow.map_agents`, then assemble validated results with `flow.reduce_agent`.
Chains pass `previous` to each following stage; optional `context` is carried to
every stage. Map expansion happens while authoring, not from runtime list sizes.

```sh
./run.sh -script examples/agent_pipeline.py
./run.sh -script examples/delivery_pipeline.py < examples/delivery_pipeline.inputs.txt
```

The 28-node delivery tape exercises plans, per-service chains, mapped checks,
reduction, a result-field condition, approval, and audit. Simulated agent results
are user-supplied. No evaluator establishes whether their conclusions are true.

## Inferred skill parameters

```sh
python3 typing/generate_skills.py typing/fixtures/*/SKILL.md --output-dir /tmp/workflow-skills
./run.sh -script examples/skill_parameters.py -skills-module /tmp/workflow-skills/generated_skills.py
```

The developer generator needs host Python. The runtime still does not. Generated
callables return skill names with argument strings; matching `.pyi` files provide
inferred parameter hints. Read metadata.json evidence/confidence before relying
on inference. Prose-only skills fall back to string arguments. See typing/README.md.
