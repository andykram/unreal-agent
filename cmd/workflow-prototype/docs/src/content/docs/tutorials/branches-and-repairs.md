---
title: Explore branches and repairs
description: Simulate successful checks, repairs, exhaustion, and execution errors.
---

This tutorial uses the existing `branching.py` example. Complete the
[quickstart](/tutorials/quickstart/) first to install the runtime.

## 1. Read the graph

```python
from harness import Workflow

flow = Workflow("bounded-repair")
workspace = flow.worktree("feature", base="current")
implement = flow.agent("implement", workspace=workspace,
                       prompt="Implement the requested feature")
verify = flow.repeat_check("verify", ["go", "test", "./..."],
                           workspace=workspace, after=implement,
                           repair_prompt="Fix the failing checks",
                           max_repairs=2)
flow.approval("integrate", after=verify,
              when={"step": verify, "outcome": "passed"})
flow.agent("escalate", workspace=workspace, after=verify,
           prompt="Explain the remaining failures and request help",
           when={"step": verify, "outcome": "failed"})
flow.export()
```

Two repairs allow at most three checks. Both branch conditions refer to the
`verify` outcome. The unselected branch becomes skipped.

## 2. Repair, then pass

From `cmd/workflow-prototype`:

```sh
./run.sh -script branching.py
```

Enter `n` three times. The first check is now waiting for a result.

1. Enter `b verify`. The first check fails and a repair becomes ready.
2. Enter `n`. Repair 1 completes and check 2 becomes ready.
3. Enter `p verify`. The node completes with outcome `passed`.
4. Observe that `integrate` is ready and `escalate` is skipped.
5. Enter `a` to record simulated consent.

The history lists both checks and the repair. No code was changed or integrated.

## 3. Exhaust the repair budget

Enter `r`, then enter `n` three times to reach check 1 again.

```text
b verify
n
b verify
n
b verify
```

The third negative result exhausts the budget. `verify` has status `completed`
and outcome `failed`. `integrate` is skipped. Enter `n` to simulate escalation.

## 4. Compare an execution error

Enter `r`, then `n` three times. Enter `f verify`.

Now `verify` has status `failed`. Both dependent branches remain blocked because
their dependency did not complete. This differs from a completed negative check.

## 5. Converge the alternatives

To extend the example, add these lines before `flow.export()`:

```python
settled = flow.join("decision-settled", after=["integrate", "escalate"])
flow.agent("summary", workspace=workspace, after=settled,
           prompt="Summarize the selected path and remaining work")
```

Restart the script. Pass `verify`, then approve `integrate` with `a`.
Enter `n` to complete the join, then `n` again for the summary.
The skipped escalation branch does not prevent convergence.

On the exhausted-repair path, complete escalation with `n`, then advance the join
and summary. An execution error still blocks convergence.

:::note[Explicit convergence]
Ordinary nodes still skip if any dependency skips. Use `join` between mutually
exclusive branches and their shared continuation.
:::

Enter `q` to exit. The saved run can be resumed; `r` starts a separate run. See the
[execution model](/explanation/execution-model/) for details.
