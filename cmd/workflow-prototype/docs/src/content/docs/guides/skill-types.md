---
title: Generate skill-name types
description: Create Python editor stubs from an explicit skill catalog snapshot.
---

Agent steps can reference skills by name. Repair loops have a separate list for
the repair agent:

```python
implement = flow.agent("implement", workspace=workspace,
                       prompt="Investigate and correct the bug",
                       skills=["diagnose"])
flow.repeat_check("verify", ["go", "test", "./..."],
                  workspace=workspace, after=implement,
                  repair_prompt="Repair remaining failures",
                  repair_skills=["diagnose"])
```

The prototype serializes these names. It does not resolve a registry or load
skill contents. Runtime integration still needs those operations.

## Generate an editor snapshot

From `cmd/workflow-prototype`, use Python 3.11 or newer:

```sh
python3 typing/generate_stubs.py typing/catalog.example.json \
  --output /tmp/workflow-stubs/harness.pyi
```

The output path must be new. The generator creates parent directories and
refuses to overwrite an existing file. Generate a fresh snapshot when catalog
entries change, then explicitly replace your previous editor stub.

The sample catalog is a fixture, not an export from the active skill registry.
A real snapshot uses this shape:

```json
[
  {"name": "diagnose", "description": "Investigate bugs and performance regressions"},
  {"name": "documentation", "description": "Write developer documentation"}
]
```

Entries require unique, nonempty names and string descriptions. The generator
sorts the names and emits the full `Workflow` API plus a literal type:

```python
SkillName = Literal["diagnose", "documentation"]
```

`agent.skills` and `repeat_check.repair_skills` use `Sequence[SkillName]` in the
stub. It also includes `OutputModel` and the `agent.output` parameter.
An empty catalog emits `Never` for skill names. Configure your Python
editor or type checker to discover the generated `harness.pyi` as its module stub.

## Scope of the types

The stub exposes skill names for editor completion and type checking. The catalog tool does not infer arguments from descriptions. A separate tool
can inspect explicit argument conventions in skill source files.

Catalog changes require regeneration. Static checks against a stale snapshot
cannot guarantee that a skill exists at runtime. The harness still needs to
validate references against the catalog used by that run.

See the [Python API](/reference/python-api/) for runtime validation rules.


## Infer invocation builders from skill source

Use the separate source-aware generator for skills with documented arguments:

```sh
python3 typing/generate_skills.py \
  typing/fixtures/review-change/SKILL.md \
  typing/fixtures/audit-bundle/SKILL.md \
  --output-dir /tmp/workflow-skill-builders
```

The output directory must be new. It contains `generated_skills.py`, its `.pyi`
stub, and `metadata.json`. The runtime module uses only the standard library.

```python
from generated_skills import review_change, audit_bundle

review = review_change(change=42, format="markdown")
audit = audit_bundle(bundle="dist/app.js", budget=1000)
```

Mount the generated module explicitly when a workflow imports it:

```sh
./run.sh -script /path/to/workflow.py \
  -skills-module /tmp/workflow-skill-builders/generated_skills.py
```

Builders return `{ "name": "...", "arguments": "..." }` descriptors accepted by
`agent.skills` and `repeat_check.repair_skills`. Arguments are shell-quoted text;
the generator never executes a skill or a command from its documentation.

Inference reads explicit argument frontmatter, placeholder hints, full invocation
examples, positional argument references, and parameter tables. Tables can refine
primitive types. Without a usable argument contract, a builder accepts raw string
arguments. It does not invent types from arbitrary descriptive prose.

Review `metadata.json` for source paths, content hashes, inferred parameters,
evidence, and confidence. Regenerate and inspect changes when skill sources change.


## Run the parameterized example

From `cmd/workflow-prototype`, generate all three fixture builders into a fresh
temporary directory, then mount the generated runtime module:

```sh
skill_builders_dir=$(mktemp -d)/builders
python3 typing/generate_skills.py \
  typing/fixtures/review-change/SKILL.md \
  typing/fixtures/audit-bundle/SKILL.md \
  typing/fixtures/explore-design/SKILL.md \
  --output-dir "$skill_builders_dir"
./run.sh -script examples/skill_parameters.py \
  -skills-module "$skill_builders_dir/generated_skills.py" -json
```

The graph contains invocation descriptors from `review_change`, `audit_bundle`,
and `explore_design`. These fixtures are not live installed skills. Export proves
that the generated module loads in WASI and produces valid graph metadata; it
does not execute any skill. See the
[annotated source](/guides/complex-examples/#parameterized-skill-references).
