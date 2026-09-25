---
title: Graph format
description: Version 1 JSON fields, accepted kinds, and validation rules.
---

Python exports one graph. Go decodes it and validates it before displaying or
returning it. `-json` returns the validated graph, not an execution-state snapshot.

## Top-level fields

| Field | JSON type | Meaning |
| --- | --- | --- |
| `version` | Integer | Must be `1` |
| `name` | String | Nonempty workflow name |
| `python` | String | `sys.version` from the authoring interpreter |
| `platform` | String | `sys.platform`, normally `wasi` |
| `steps` | Array of step objects | At least one step |

`python` and `platform` are metadata. The validator does not require particular values.

## Step fields

| Field | JSON type | Meaning |
| --- | --- | --- |
| `id` | String | Nonempty, unique identifier |
| `kind` | String | One of the six kinds below |
| `needs` | Array of strings | Existing step IDs, with no dependency cycles |
| `spec` | Object | Operation description |
| `when` | Object, optional | Check outcome or structured-output scalar comparison |

| Kind | Fields written to `spec` by its convenience method |
| --- | --- |
| `worktree` | `base`, `backend` |
| `agent` | `workspace`, `prompt`, `skills`, optional `output_schema` and `inputs` |
| `command` | `workspace`, `argv` |
| `repeat_check` | `workspace`, `argv`, `repair_prompt`, `max_repairs`, `repair_skills` |
| `approval` | Empty object |
| `join` | Empty object |

`repeat_check.max_repairs` is validated. If present, `skills` and `repair_skills`
must be arrays of nonempty string names or `{name, arguments}` descriptors. The validator does not resolve those
names against a registry. An `output_schema` is accepted only on agent steps and
must compile as a self-contained JSON Schema. External schema references are rejected. Other specification
fields are not validated per kind.
Acceptance by the prototype does not imply suitability for a real executor.

## Conditions

```json
{
  "id": "integrate",
  "kind": "approval",
  "needs": ["verify"],
  "spec": {},
  "when": {"step": "verify", "outcome": "passed"}
}
```

For outcome conditions, the source must be a `repeat_check` step and must appear in `needs`.
The outcome must be `passed` or `failed`. Python adds the dependency automatically.
Conditions are evaluated by Go after the source completes. A `join` cannot have
a condition.

Structured output conditions instead use a source, path, and scalar value:

```json
{"step": "review", "path": ["approved"], "equals": true}
```

The source must have an output schema. Its referenced path must exist in that
schema. The source dependency is added by the Python builder.

## Output references

Agent `spec.inputs` can contain nested references:

```json
{"review": {"$output": {"step": "review", "path": []}}}
```

An empty path selects the whole result. String segments select object fields;
nonnegative integers select array elements. References add dependencies and are
resolved from stored outputs when their consumer advances. Literal dictionaries
cannot use the reserved `$output` key through the Python builder.

## Validation failures

Go rejects unsupported versions, empty names, empty graphs, empty or duplicate
step IDs, unknown kinds, missing dependencies, and dependency cycles.
It also rejects invalid conditions, joins with no dependencies or a condition,
and `max_repairs` values outside integer `0..10`.

Unknown JSON fields are not rejected by the decoder. Extra fields do not add
executor behavior. This format is a prototype contract, not a strict general schema.

## Execution state

Runtime state is held separately in Go, keyed by step ID:

```text
Status   string
Outcome  string
Phase    string
Repairs  int
History  []string
Output   any
IdempotencyKey string
AttemptKey    string
AttemptKeys   []string
```

Keys are assigned by the harness for a particular run, not by Python authoring.
This state is not included by `export()` or `-json`. Normal runs persist it with the
immutable graph. See [storage semantics](/explanation/storage/).
See [execution semantics](/explanation/execution-model/) for how these values change.
