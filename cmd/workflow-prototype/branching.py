"""Throwaway: can a static graph express runtime branches and bounded repairs?"""
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
