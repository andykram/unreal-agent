# Go validation

After changing Go code, run `go fmt ./...` and `go vet ./...` in every affected Go module before handing off the work. Resolve reported issues and report any checks that could not run.

The root module commands do not cover the separate `cmd/workflow-prototype` module. Run its checks from that directory, with `CGO_ENABLED=0` to preserve its no-cgo requirement.

# Engineering skills

For changes in these areas, read the matching repository skill. Load additional skills only when the change crosses their boundaries.

- [Harness engineering](.agents/skills/harness-engineering/SKILL.md): coordinator, persisted sessions, tools, providers, and model requests.
- [Workflow library](.agents/skills/workflow-library/SKILL.md): Python/WASI API, graph dataflow, structured outputs, generated skill types, and developer docs.
- [Workflow durability](.agents/skills/workflow-durability/SKILL.md): checkpoints, recovery, retention, memoization, and executor key isolation.
- [REPL engineering](.agents/skills/repl-engineering/SKILL.md): terminal rendering, input, sessions, workflow threads and approval UI, configuration, and model catalogs.
