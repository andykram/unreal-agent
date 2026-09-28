"""Throwaway: author a nested structured agent output schema inside WASI.

This exports schema metadata. The viewer accepts manually supplied JSON.
It does not invoke a provider. Other agents can consume fields with flow.ref.
"""
from typing import Literal
from harness import OutputModel, Workflow


class Finding(OutputModel):
    severity: Literal["low", "medium", "high"]
    file: str
    line: int
    explanation: str
    suggested_fix: str | None


class Review(OutputModel):
    verdict: Literal["approve", "changes_requested"]
    summary: str
    findings: list[Finding]
    confidence: float
    tests_reviewed: bool


flow = Workflow("structured-review")
workspace = flow.worktree("review-workspace", base="current")
review = flow.agent("review", workspace=workspace, skills=["diagnose"],
                    prompt="Review the proposed changes. Return a Review with concrete findings.",
                    output=Review)
flow.approval("acknowledge-review", after=review)
flow.export()
