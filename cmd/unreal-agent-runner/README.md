# unreal-agent-runner

Run an AI agent from a prompt or JSON request. It writes events to stdout as
JSONL and exits when the task finishes.

Install with Go 1.27+:

```sh
go install github.com/unreallabsai/unreal-agent/cmd/unreal-agent-runner@latest
```

Set an OpenAI API key and run a prompt in the current directory:

```sh
export OPENAI_API_KEY="..."
unreal-agent-runner -p 'Inspect this project and explain how to run its tests.'
```

Or run from source at the repository root:

```sh
go run ./cmd/unreal-agent-runner -p 'Inspect this project and explain how to run its tests.'
```

Choose a workspace and save the output:

```sh
unreal-agent-runner -workspace ./my-project -p 'Summarize this project.' > run.jsonl
```

Sessions: `${XDG_STATE_HOME:-$HOME/.local/state}/unreal-agent/sessions`
(override with `-session-directory`).

You can also pass a JSON request as an argument or through stdin:

```sh
unreal-agent-runner '{"prompt":"Summarize this project."}'
unreal-agent-runner < request.json
```

OpenAI is the default provider. Set `UNREAL_HARNESS_LLM_PROVIDER` to `openai`,
`openai-codex`, `openrouter`, `fireworks`, or `ollama`, and
`UNREAL_HARNESS_LLM_MODEL` to choose a model.

Run `unreal-agent-runner -h` for options and the JSON request fields.

## Docker

The `unrea1labs/unreal-agent` image supports Linux on AMD64 and ARM64. Run it
with a project mounted as the workspace:

```sh
docker run --rm -i --user "$(id -u):$(id -g)" \
  -e OPENAI_API_KEY -v "$PWD:/workspace" \
  -v unreal-agent-state:/state \
  unrea1labs/unreal-agent:latest -p 'Summarize this project.'
```

Each release also publishes its Git tag (for example, `v0.1.0`) for version pinning.

## Compact skill discovery

Set `"compact_skills": true` in a JSON request to omit skill file paths from the
initial discovery index. This saves context, especially when many skills have
long paths. The default is `false`, which keeps paths in the index.

```sh
unreal-agent-runner '{"prompt":"Review this project.","compact_skills":true}'
```

Skills still load by registered name in both modes. The `SkillUse` result includes
the loaded file path so relative references can be resolved. Names, descriptions,
and loaded skill contents are unchanged. Set the flag again when resuming a
session; it is request configuration, not a persisted session setting.

Go callers can use `contextbuilder.NewBuilderWithConfig` with
`contextbuilder.Config{CompactSkills: true}`. `NewBuilder` keeps locations by default.
