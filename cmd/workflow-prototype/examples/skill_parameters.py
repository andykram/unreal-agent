"""Use callable skill parameters inferred from the bundled SKILL.md fixtures.

Generate with typing/generate_skills.py, then pass -skills-module /path/to/
generated_skills.py. These fixture skills describe metadata, not installed tools.
"""
from harness import Workflow
from generated_skills import audit_bundle, explore_design, review_change

flow = Workflow("parameterized-skills")
workspace = flow.worktree("skill-review")
review = flow.agent("review", workspace=workspace,
                    prompt="Review the change and inspect its evidence bundle.",
                    skills=[review_change(change=42, format="concise"),
                            audit_bundle(bundle="evidence bundle", budget=200)])
flow.agent("design", workspace=workspace, after=review,
            prompt="Suggest a design using the review.",
            skills=[explore_design("compact", "keyboard accessible")])
flow.export()
