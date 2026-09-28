# Skill-aware Python types and callable descriptors

## Infer calls from SKILL.md

```sh
python3 typing/generate_skills.py typing/fixtures/*/SKILL.md --output-dir /tmp/skill-api
```

This reads only explicitly supplied files and generates `generated_skills.py`,
`generated_skills.pyi`, and `metadata.json`. The Python module uses only the
standard library and does not execute skill instructions. It creates descriptors:

```python
from generated_skills import review_change, audit_bundle, explore_design

review_change(change=42, format="markdown")
# {"name": "review-change", "arguments": "42 markdown"}
audit_bundle(bundle="dist/app bundle.js", budget=100000)
# {"name": "audit-bundle", "arguments": "'dist/app bundle.js' --budget 100000"}
explore_design("mobile onboarding", "with offline support")
```

Pass these descriptors in the workflow's `skills` or `repair_skills` list. The
host must make the generated module available as `generated_skills.py` in WASI.
The fixture names are demonstration skills, not installed registry entries.
Generating a descriptor does not install or execute its skill.

Inference reads these conventions, in order:

1. `arguments:` names in a plain string or simple YAML list.
2. `argument-hint:` with named placeholders.
3. One standalone `/skill-name <parameter> [--option <value>]` usage pattern.
4. Indexed `$ARGUMENTS[N]` or `$N`, generating `arg0`, `arg1`, and so on.
5. An explicit `Parameters` or `Arguments` table with `Name`, `Type`, and
   `Required` columns, using row order when no invocation pattern exists.

Tables refine primitive types (`string`/`path`, `integer`, `number`, `boolean`)
and requiredness. Otherwise parameters are strings. Indexed positions and hint
names are required by default, a reviewable heuristic: brackets in Claude's
autocomplete hints do not establish optionality. Brackets in documented CLI
usage mean optional values. Boolean values serialize as `true` or `false`, not
bare flags. Ambiguous prose and `$ARGUMENTS` alone generate `*arguments: str`.

Every generated callable uses keyword parameters to make inferred names visible.
`SKILL_METADATA` and `metadata.json` retain description, parameter order, source,
SHA256, evidence, and confidence. These are inferred contracts, never guaranteed
prose semantics. Review them before relying on enforcement. Omitted positional
arguments cannot precede supplied positional arguments.

Names become Python identifiers; duplicate names, sanitized collisions, and
reserved helper names fail deterministically. Arguments are shell-quoted text,
never commands. Source text, dynamic context commands, scripts, and imports from
skill documentation are never evaluated. Arbitrary free prose inference would
need a separate reviewed extraction process, potentially model-assisted.

The frontmatter reader supports common scalar fields, block descriptions, and
simple `arguments` lists. It is not a complete YAML parser; advanced YAML should
be normalized before use. Output files are created exclusively in a fresh
directory. No tracked SDK source is overwritten.

The [Agent Skills specification](https://agentskills.io/specification) defines no
universal parameter schema. `argument-hint`, `arguments`, and positional
substitution are [Claude Code conventions](https://code.claude.com/docs/en/skills).

## Catalog-only SDK stubs

This developer tool generates the complete `harness.pyi` API from an explicit
JSON catalog snapshot. It requires host Python 3.11+, not a production Go or WASI
dependency. It does not fetch or discover skills.

```sh
python3 typing/generate_stubs.py typing/catalog.example.json --output /tmp/stubs/harness.pyi
```

Run from `cmd/workflow-prototype`. The example catalog is a fixture containing
existing skill names, not the live harness registry. A future registry exporter
can supply the same list of `{ "name": "...", "description": "..." }` objects.

`SkillName` is a sorted `Literal[...]` union. An empty catalog emits `Never`, so
empty skill lists remain valid while no skill name is accepted. Descriptions
are validated but never interpreted as argument schemas in this catalog-only
mode. Use SKILL.md inference above for callable parameter hints. Duplicate names fail.

```python
flow.agent("review", workspace="feature", prompt="Review the interface",
           skills=["frontend-design"])
flow.repeat_check("verify", ["go", "test", "./..."], workspace="feature",
                  after="review", repair_prompt="Diagnose failing tests",
                  repair_skills=["diagnose"])
```

Place the generated stub adjacent to a copied SDK's `harness.py`, or configure
your checker's stub search path (for example Pyright's `stubPath` or mypy's
`MYPYPATH`) to its directory. Generated output is editor/checker metadata;
Python execution does not load `.pyi` files. Runtime registry validation remains
necessary, especially after skills change. The generic `step(..., **spec)` API
is intentionally untyped for custom specification fields.

The output path is mandatory and existing files are never overwritten. Generate
into a new temporary directory, review the result, and explicitly replace any
previous local stub. Nothing automatically changes tracked SDK sources.
