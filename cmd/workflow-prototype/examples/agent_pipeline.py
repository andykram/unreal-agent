"""Real Pydantic schemas and data passing through a simulated agent pipeline.

No model is called. Supply structured results with `o ID JSON`. References
resolve only after source completion. Each mapper gets its own worktree.
"""
from typing import Literal
from pydantic import BaseModel, Field
from harness import Agent, Workflow


class Evidence(BaseModel):
    topic: str
    sources: list[str]
    finding: str
    confidence: float = Field(ge=0, le=1)

    class Config:
        extra = "forbid"


class Brief(BaseModel):
    title: str
    evidence: list[Evidence]
    open_questions: list[str]

    class Config:
        extra = "forbid"


class Critique(BaseModel):
    gaps: list[str]
    recommendation: Literal["revise", "publish"]

    class Config:
        extra = "forbid"


class Publication(BaseModel):
    title: str
    body: str
    approved: bool

    class Config:
        extra = "forbid"


flow = Workflow("evidence-to-publication")
control = flow.worktree("editorial")
research = Agent("Research the assigned item. Return evidence with uncertainty.",
                 output=Evidence, skills=("diagnose",))
synthesizer = Agent("Synthesize the results list into a brief without inventing sources.",
                    output=Brief, skills=("documentation",))
critic = Agent("Critique the previous brief. Identify unsupported conclusions.",
                output=Critique)
editor = Agent("Use the previous critique to write a publication or request more work.",
                output=Publication, skills=("documentation",))

# Build-time map: independent inputs and workspaces; the list size is known now.
topics = ["correctness", "performance", "operability"]
workspaces = [flow.worktree("research-" + topic) for topic in topics]
research_steps = flow.map_agents("research", topics, research, workspaces=workspaces)

# Fan-in: the Go executor assembles validated mapper outputs under inputs.results.
brief = flow.reduce_agent("brief", research_steps, synthesizer, workspace=control,
                          inputs={"audience": "maintainers"})

# Sequential composition: each stage receives the preceding result as previous.
# Explicitly carry the brief through context when the editor also needs it.
pipeline = flow.chain("publication", [("critique", critic), ("edit", editor)],
                      workspace=control, inputs={"previous": flow.ref(brief)},
                      context={"brief": flow.ref(brief)})

# Individual fields and whole objects can be passed together. Dependencies are
# inferred from refs, including refs nested inside lists and dictionaries.
publish = flow.agent("publish", workspace=control,
                      prompt="Record the approved publication handoff.",
                      inputs={"title": pipeline.output.field("title"),
                              "document": pipeline.output,
                              "original_brief": flow.ref(brief)},
                      when=pipeline.output.field("approved").equals(True))
followup = flow.agent("followup", workspace=control,
                       prompt="Record unresolved publication work.",
                       inputs={"draft": pipeline.output},
                       when=pipeline.output.field("approved").equals(False))
flow.join("finished", after=[publish, followup])
flow.export()
