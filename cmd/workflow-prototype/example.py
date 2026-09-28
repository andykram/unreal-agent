from harness import Workflow

flow = Workflow("implement-feature")
workspace = flow.worktree("feature", base="current")
implement = flow.agent("implement", workspace=workspace,
                       prompt="Implement the requested feature")
checks = [flow.command(name, argv, workspace=workspace, after=implement)
          for name, argv in [("test", ["go", "test", "./..."]),
                             ("vet", ["go", "vet", "./..."])]]
review = flow.agent("review", workspace=workspace,
                    prompt="Review the changes", after=checks)
flow.approval("integrate", after=review)
flow.export()
