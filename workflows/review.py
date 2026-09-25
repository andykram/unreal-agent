"""Live example: review the committed checkout in an isolated worktree."""

from harness import OutputModel, Workflow


class Review(OutputModel):
    summary: str
    findings: list[str]
    checks_run: list[str]
    unverified: list[str]


flow = Workflow("Repository review")
workspace = flow.worktree("review-workspace")
review = flow.agent(
    "review",
    workspace=workspace,
    prompt=(
        "Review this repository's current committed code. Read repository instructions "
        "and inspect the source with read-only tools. Do not modify files or install "
        "dependencies. Report concrete findings with file paths. List only checks "
        "you actually performed in checks_run and clearly identify unverified claims."
    ),
    output=Review,
)
flow.approval("acknowledge-findings", after=review)
flow.export()
