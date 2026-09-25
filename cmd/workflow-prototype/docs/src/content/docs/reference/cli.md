---
title: CLI and viewer
description: Runner flags, runtime cache, and interactive simulation controls.
---

Run commands from `cmd/workflow-prototype`:

```sh
./run.sh -script example.py -json
./run.sh -script branching.py
./run.sh -script branching.py -auto
```

## Flags

| Flag | Default | Behavior |
| --- | --- | --- |
| `-script` | `example.py` | Python authoring script to read |
| `-skills-module` | Empty | Optional path to a generated Python skill module, mounted as `generated_skills.py` |
| `-json` | `false` | Print a validated graph without creating a run; with `-resume`, export the saved graph |
| `-resume` | Empty | Resume a saved run ID without rerunning Python authoring |
| `-state-dir` | XDG state directory | Directory containing the workflow run database |
| `-retention` | `168h` | Positive duration from completion before an inactive run is eligible for cleanup |
| `-auto` | `false` | Advance ready nonapproval work and pass waiting checks automatically |
| `-python` | Empty in the binary | Runtime directory containing `python.wasm` and `lib`; supplied by `run.sh` |

`run.sh` changes into its own directory before running the binary. Relative
`-script` paths are relative to `cmd/workflow-prototype`, regardless of the caller's
directory. Use an absolute path for scripts elsewhere.

`-auto` exits when no further work advances. Approvals and agents awaiting structured output remain pending. It does
not approve or integrate changes.

## Interactive commands

Each command requires Enter. These are line-oriented viewer commands.

| Command | Behavior |
| --- | --- |
| `n` | Advance one snapshot of ready nonapproval steps and running repairs |
| `a` | Complete ready approvals with outcome `approved` |
| `o ID JSON` | Validate JSON for an agent waiting for structured output; store it and complete the node |
| `p ID` | Supply a passing result to a waiting check |
| `b ID` | Supply a negative result to a waiting check |
| `f ID` | Mark a ready or running step as an execution failure |
| `r` | Start a new run from the same graph; preserve the previous run |
| `q` | Exit while keeping the run available for resume |

`p` and `b` are ignored unless the named `repeat_check` is running in its check
phase. `n` does not supply a check result in interactive mode. Unknown commands
have no effect.

## Workflow run storage

Normal viewer and automatic runs are durable by default. State lives under
`$XDG_STATE_HOME/unreal-agent/workflows`, or
`~/.local/state/unreal-agent/workflows` when `XDG_STATE_HOME` is unset.
The database filename is `workflows.sqlite`. Use `-state-dir` to select an isolated directory.

Resume by saved run ID:

```sh
./run.sh -resume RUN_ID
```

The saved graph is immutable. Resume restores it rather than reloading a changed
Python script. Resume rejects explicit `-script` or `-skills-module` flags.
For a custom storage directory, pass the same `-state-dir` on resume. The viewer
prints the run ID, checkpoint revision, and a ready-to-use resume command.
`run.sh` skips Python runtime downloads and dependency installation for resume.
It still builds the Go executable, which requires the Go toolchain and dependencies.

Cleanup removes only inactive runs whose nodes all completed or skipped and
whose retention period elapsed. Failed, blocked, or waiting runs are preserved.
The current process’s active run is excluded. Retention is not a disk-space quota.

See [resume a workflow](/tutorials/resume-workflow/) for a recovery walkthrough and
[storage semantics](/explanation/storage/) for durability boundaries.

## Runtime cache

The runner uses `$XDG_CACHE_HOME/unreal-agent/workflow-prototype`, or
`~/.cache/unreal-agent/workflow-prototype` when `XDG_CACHE_HOME` is unset.
It downloads and verifies the CPython archive when `python-3.14.7/python.wasm`
is absent. It also installs checksum-pinned pure Python Pydantic and its typing
dependency into the runtime. It rebuilds the Go prototype on every invocation.

The runtime archive is an unofficial CPython WASI build. The runner pins its
URL and SHA-256. The archive contains the CPython license. There is no compiled
Wasm-module cache, so each process compiles the interpreter again.

## Repository targets

From the repository or worktree root:

```sh
make workflow-prototype           # example.py
make workflow-prototype-branches  # branching.py
```
