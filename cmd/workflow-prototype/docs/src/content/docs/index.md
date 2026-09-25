---
title: Workflow Python API
description: Author Python workflow graphs, simulate their behavior, and inspect durable simulated execution.
---

Write Python to describe a workflow. CPython runs inside WASI, exports a JSON
graph, and exits. The standalone viewer simulates execution. The shared Go engine
also exposes an executor interface for applications to implement.

:::caution[Simulation boundary]
The `run.sh` tutorials simulate agents, commands, worktrees, repairs, and approvals.
Those simulations do not invoke models, execute commands, or create worktrees.
The API and graph format are experimental.
:::

```python
from harness import Workflow

flow = Workflow("first-workflow")
workspace = flow.worktree("feature")
implement = flow.agent("implement", workspace=workspace,
                       prompt="Implement the requested feature")
flow.command("test", ["go", "test", "./..."],
             workspace=workspace, after=implement)
flow.export()
```

## Start here

- [Build your first workflow](/tutorials/quickstart/) to run the example through WASI.
- [Explore branches and repairs](/tutorials/branches-and-repairs/) to choose outcomes in the viewer.
- [Explore complex workflows](/guides/complex-examples/) for releases, migrations, and investigations.
- [Generate skill-name types](/guides/skill-types/) from a catalog snapshot for editor tooling.
- [Explore structured agent output](/tutorials/structured-output/) to validate a simulated JSON review.
- [Resume a workflow](/tutorials/resume-workflow/) to recover saved outputs and pending work.
- [Python API](/reference/python-api/) lists every method and its exact signature.
- [Graph format](/reference/graph-format/) describes the Python-to-Go contract.
- [Execution model](/explanation/execution-model/) explains dependencies, outcomes, and limits.

The shared implementation lives in `harness/workflow`. The standalone simulator
lives in `cmd/workflow-prototype`. Concrete execution adapters are not included.
The API remains experimental.
