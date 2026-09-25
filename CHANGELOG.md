# Changelog

User-visible changes are listed under Unreleased until they are included in a tagged release.
Each pull request adds its entry here. PR links provide stable history across rebases and squash merges.

## Unreleased

### CI and review foundation ([#10](https://github.com/andykram/unreal-agent/pull/10))

- Add Linux and macOS validation, golangci-lint v2, formatting and vet checks, race tests, fuzzing, and builds without cgo.
- Check Harbor, Docker, Python/WASI workflows, generated skill types, and developer docs when those components are present.
- Configure CodeRabbit reviews for stacked pull requests.

### Session fork recovery ([#2](https://github.com/andykram/unreal-agent/pull/2))

- Preserve completed tool results when forking and restoring sessions, so resumed model requests retain matching tool calls and outputs.

### Skill loading ([#3](https://github.com/andykram/unreal-agent/pull/3))

- Add opt-in `compact_skills` discovery to save context by omitting skill paths from the index. Paths remain visible by default. Skills load by name and return their location for relative references.

### Provider connections ([#4](https://github.com/andykram/unreal-agent/pull/4))

- Add the `ollama-cloud` provider with API-key authentication while retaining local Ollama support.
- Add authenticated Codex model-catalog requests using the configured subscription endpoint and credentials.

### Structured model outputs ([#5](https://github.com/andykram/unreal-agent/pull/5))

- Add JSON Schema output formats to model requests and encode native structured outputs for Responses API providers.
