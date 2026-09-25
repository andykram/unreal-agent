# Continuous integration

CI runs for every pull request, including stacked PRs whose base is another feature branch. Pushes to `main` and manual dispatches also run CI. A newer run for the same pull request cancels older work. Feature branches use the pull request trigger to avoid duplicate push and PR runs.

Required validation includes:

- Linux and macOS: root race tests, `go vet`, a failing `gofmt` check, runner builds, and a no-cgo build.
- The standalone workflow module, when present: no-cgo vet, tests, build, and golangci-lint v2. Root formatting covers its Go sources too.
- golangci-lint v2 for root Go packages and Actionlint for workflow definitions.
- All five current fuzz targets, with 30 seconds of mutation per target. Normal Go tests also execute saved seeds. Failed fuzz corpora are retained as artifacts for seven days.
- Existing Harbor unit tests, Ruff lint and formatting, bundle smoke tests, and Docker build/smoke checks.
- Workflow authoring, when present: Python runtime/error lint, native unit tests, generated skill types, and a complex Python/Pydantic graph compiled through WASI.
- The Starlight docs build, when present. Publishing is configured with the docs feature.

The workflow module and docs checks activate when their files enter the stack. This lets the CI foundation PR run before those features exist. Add new fuzz targets to the CI matrix when introducing them.

Run `make test check build` locally. Use the versions pinned in `ci.yml` for golangci-lint and Actionlint. Run the nested module commands from `cmd/workflow-prototype` with `CGO_ENABLED=0`. CI never requires model credentials or sends live model requests. Clipboard/display smoke checks still require suitable hosts.

Go lint uses `.golangci.yml` with govet, Staticcheck, unused-code, and ineffective-assignment checks. Run `golangci-lint run ./...` in each Go module. The nested module inherits the root configuration and runs with `CGO_ENABLED=0`.
