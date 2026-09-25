---
title: Build your first workflow
description: Export a graph from Python inside WASI and advance its simulated steps.
---

You will create a worktree description, an agent description, and two parallel
checks. The viewer will show their dependencies and simulated states.

## Prerequisites

- A checkout containing `cmd/workflow-prototype`.
- Go 1.27 or newer, `curl`, `unzip`, and `shasum`.
- Network access for the initial Go dependency and CPython downloads.

You do not need a local Python installation or a C compiler. `run.sh` builds Go
with `CGO_ENABLED=0` and downloads a checksum-pinned CPython 3.14.7 WASI archive.

## 1. Create a script

From the repository or worktree root:

```sh
cd cmd/workflow-prototype
```

Save this as `first_workflow.py` in that directory:

```python
from harness import Workflow

flow = Workflow("first-workflow")
workspace = flow.worktree("feature", base="current")
implement = flow.agent("implement", workspace=workspace,
                       prompt="Implement the requested feature")

checks = []
for name, argv in [("test", ["go", "test", "./..."]),
                   ("vet", ["go", "vet", "./..."])]:
    checks.append(flow.command(name, argv, workspace=workspace,
                               after=implement))

flow.approval("integrate", after=checks)
flow.export()
```

Each method returns the step ID. The `after` arguments connect those IDs into a graph.

## 2. Export the graph

```sh
./run.sh -script first_workflow.py -json
```

The output is a validated JSON document with five steps. The `test` and `vet`
steps both depend on `implement`. The interpreter reports `wasi` as its platform.

The script must write exactly one JSON document to stdout. Use stderr for diagnostics.

## 3. Advance the viewer

```sh
./run.sh -script first_workflow.py
```

Enter each command followed by Enter:

1. `n` completes the simulated worktree step.
2. `n` completes the simulated agent step.
3. `n` completes both checks in one ready batch.
4. `a` completes the ready approval.
5. `q` exits.

All five steps now show completion. The two checks demonstrate dependency-level
parallelism. No commands or agents actually ran, and approval did not merge code.

Continue with [branches and repairs](/tutorials/branches-and-repairs/), or look up
the [Python methods](/reference/python-api/).
