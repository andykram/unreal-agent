---
title: Python API
description: Exact signatures, dependency rules, and return values for the Workflow class.
---

```python
from harness import Workflow
```

The shared host embeds `harness.py` and makes it importable inside the WASI interpreter.
It is not an installed Python package. The signatures below omit `self`.

## Shared arguments and return values

| Argument | Expected value | Meaning |
| --- | --- | --- |
| `id` | Nonempty string, unique in the graph | Stable step reference |
| `after` | Step ID string or iterable of IDs | Dependencies, with completion rules defined by the step kind |
| `workspace` | Worktree step ID | Workspace metadata, not a host path |
| `when` | `None` or `{"step": id, "outcome": "passed" or "failed"}` | Runtime check-outcome condition; also accepts `OutputRef.equals(...)` |

All step-creation methods append a step and return its `id`. They do not execute
work. Only `agent` supplies a workspace dependency when `after` is empty.
Every provided `when` adds its source step to `needs` if absent.

The Python builder performs little validation. Go validates the exported graph.
Most per-kind specification fields, including workspace references, are not validated.

## `OutputModel`

```python
from harness import OutputModel
from typing import Literal

class Finding(OutputModel):
    severity: Literal["warning", "blocking"]
    summary: str

class Review(OutputModel):
    accepted: bool
    findings: list[Finding]
    followup: str | None
```

`OutputModel` subclasses real Pydantic `BaseModel`. The WASI bundle uses the
pure Python distribution of Pydantic 1.10.26. The wrapper adds v2-style method
names and forbids extra fields by default. `agent(output=...)` also accepts a
Pydantic `BaseModel` subclass directly.

| Method | Result |
| --- | --- |
| `Review.model_json_schema()` | Pydantic-generated JSON Schema, with nullable fields preserved |
| `Review.model_validate_json(text)` | Pydantic-validated `Review` instance, or an error |
| `review.model_dump()` | Dictionary of the instance's field values |

Defaults, constraints, nested models, and validators follow the installed
Pydantic version. A nullable field without an explicit required marker can be
optional under Pydantic v1. Use `Field(...)` when it must be present.

The Go viewer validates exported JSON Schema. It does not execute Python custom
validators or perform Pydantic coercion. Provider support for schema features is
also separate from local validation.

## `Workflow`

```python
Workflow(name)
```

Creates an empty workflow. `name` must be a nonempty string for host validation.
The instance exposes `name` and a mutable `steps` list. Prefer the builder methods
over changing that list directly.

## `step`

```python
flow.step(id, kind, *, after=(), when=None, **spec)
```

Appends a generic step. Keyword arguments in `spec` become the step's `spec`
object. `after` defaults to no dependencies. A string becomes a one-element list.

Go accepts only `worktree`, `agent`, `command`, `approval`, `repeat_check`, and
`join` kinds. This method does not register new operation types. The standalone viewer simulates
these kinds; applications supply concrete execution adapters.

```python
flow.step("conditional-test", "command", after="verify",
          when={"step": "verify", "outcome": "passed"},
          workspace="feature", argv=["go", "test", "./..."])
```

Use `step` to write supported kinds directly. The same Go validation rules apply.
For conditional commands, prefer the `command` helper below.

## `worktree`

```python
flow.worktree(id, base="current")
```

Creates a root `worktree` description with `spec.base` and
`spec.backend == "worktrunk"`. The backend cannot be changed through this method.
The simulator treats `base` and the backend as metadata. A concrete executor
must define commit resolution and worktree provisioning.

```python
workspace = flow.worktree("feature", base="current")
```

## `agent`

```python
flow.agent(id, *, workspace, prompt, after=(), when=None, skills=(), output=None, inputs=None, system_prompt_append="")
```

Describes an agent with the given `workspace` and prompt string.
`system_prompt_append` records additive system instructions in the graph. An
agent executor must append them while preserving its configured base instructions
and restrictions. The simulator does not assemble or send model prompts.
Reusable `Agent(..., system_prompt_append="...")` definitions preserve the same
addition across bindings, chains, maps, and reductions. Both APIs require a string.

```python
flow.agent("review", workspace=workspace, prompt="Review the change",
           system_prompt_append="Report reproducible defects with file and line evidence.")
```

When `after` is empty or otherwise false, dependencies default to `[workspace]`.
When `after` is nonempty, it replaces that default. A `when` source is then added.

```python
implement = flow.agent("implement", workspace=workspace,
                       prompt="Implement the requested feature")
```

`skills` accepts names such as `["diagnose"]` or descriptors with exactly `name`
and `arguments` string fields. It defaults to empty. The builder rejects a single
string and malformed references, then removes duplicates while preserving order.
Go validates the serialized shape. See [skill tooling](/guides/skill-types/).

`output` optionally accepts a Pydantic `BaseModel` subclass for a structured response.
See [OutputModel](#outputmodel) below.

These are references only during authoring and simulation. Live agent execution
passes the skill requests into a child harness session, where registered skill
names can be resolved and loaded. This method has no model, effort, access,
or timeout parameters.

`inputs` optionally maps names to literal values or output references. References
add their source steps to dependencies. Use `flow.ref(step, *path)` to create them.

## Reusable agents and pipelines

```python
from harness import Agent

reviewer = Agent(prompt="Review the input", output=Review, skills=("diagnose",))
step = reviewer.bind(flow, "review", workspace=workspace, inputs={"change": "example"})
```

`Agent.bind` creates a fresh step and always includes its workspace dependency.
The shared definition does not share execution state between bindings.

| API | Behavior |
| --- | --- |
| `flow.ref(step, *path)` | Reference a whole output or fields/indexes within it |
| `reference.field(*path)` | Extend an output path |
| `reference.equals(value)` | Build a runtime condition comparing a scalar JSON value |
| `flow.chain(prefix, stages, *, workspace, inputs=None, context=None, after=())` | Bind `(name, Agent)` stages in sequence; each later stage receives the previous output as `inputs.previous`; shared `context` is preserved as `inputs.context` |
| `flow.map_agents(prefix, items, agent, *, workspaces, after=())` | Bind one agent per item/workspace pair with `inputs.item` |
| `flow.reduce_agent(id, steps, agent, *, workspace, inputs=None, after=())` | Bind an agent with all source outputs in `inputs.results` |

A chain returns `Pipeline` with `steps`, `last`, and `output`. Maps return step
IDs. Chains and maps must be nonempty; map item/workspace counts must match.
Reduction requires at least one source and reserves the `results` input name.

References use field names or nonnegative array indexes. `$output` is reserved
in serialized specifications; create references through the builder. Schema paths
are checked before simulation, including typed dictionary keys and tuple indexes.
Ambiguous multi-model union paths are rejected. Missing optional values and out-of-range indexes
fail when resolved at runtime. Mapping expands a static Python collection during
authoring; it cannot dynamically map over a future agent output.

## `command`

```python
flow.command(id, argv, *, workspace, after, when=None)
```

Describes an argument vector, such as `["go", "test", "./..."]`.
`workspace` and `after` are required. The workspace dependency is **not** added
automatically. `when` optionally selects a branch from a completed check outcome.

```python
test = flow.command("test", ["go", "test", "./..."],
                    workspace=workspace, after=implement)
```

## `repeat_check`

```python
flow.repeat_check(id, argv, *, workspace, repair_prompt,
                  max_repairs=2, after, when=None, repair_skills=(),
                  repair_system_prompt_append="")
```

Describes a check with a bounded check/repair cycle. `workspace`, `repair_prompt`,
and `after` are required. The workspace dependency is not added automatically.
`repair_system_prompt_append` adds system instructions only to the repair agent;
it does not affect the check command. It defaults to an empty string.

`max_repairs` defaults to `2`. It must be a Python `int` from `0` to `10`,
inclusive. Booleans and floats are rejected with `ValueError`, even if numerically
equivalent. Go also validates this field. The maximum check count is
`max_repairs + 1`.

```python
verify = flow.repeat_check("verify", ["go", "test", "./..."],
                           workspace=workspace, after=implement,
                           repair_prompt="Fix the failing checks",
                           max_repairs=2)
```

`repair_skills` names skills intended for the repair agent. It has the same
skill-reference validation and metadata-only behavior as `agent.skills`.

`when` optionally starts this loop only for a selected upstream check outcome.
See [runtime semantics](/explanation/execution-model/) for outcome handling.

## `approval`

```python
flow.approval(id, *, after, when=None)
```

Describes a step that waits for explicit approval in the simulator or live runner. `after` is required.
No prompt text or integration strategy is accepted.

```python
flow.approval("integrate", after=verify,
              when={"step": verify, "outcome": "passed"})
```

An approval called `integrate` does not merge anything. The name is only an ID.

## `join`

```python
flow.join(id, *, after)
```

Converges alternative branches. `after` is required and must contain at least
one dependency. A join cannot have a `when` condition, including through `step`.

The join becomes ready when every input is `completed` or `skipped`, with at
least one completed input. If all inputs skip, the join skips. An execution
failure blocks the join. It does not race inputs or choose the first finisher.

```python
settled = flow.join("review-settled", after=["approve", "escalate"])
flow.agent("summary", workspace=workspace, after=settled,
           prompt="Summarize the selected review path")
```

A join does not merge worktrees, collect output values, or require passing check
outcomes. A completed check with outcome `failed` still counts as completed.
Use conditional branch endpoints when later work requires a particular outcome.

## `export`

```python
flow.export()
```

Prints one JSON document to stdout and returns `None`. Includes graph version `1`,
name, Python version, platform, and steps. Call once at the end of the script.
This method does not validate the graph, write a file, or execute steps.

```python
import sys
print("Exporting workflow", file=sys.stderr)
flow.export()
```

Read the [graph format](/reference/graph-format/) for the serialized contract.
