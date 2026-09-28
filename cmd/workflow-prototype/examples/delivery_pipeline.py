"""Throwaway: a 28-node delivery pipeline with real structured dataflow.

Run from cmd/workflow-prototype:
  ./run.sh -script examples/delivery_pipeline.py
  ./run.sh -script examples/delivery_pipeline.py < examples/delivery_pipeline.inputs.txt

Four static services receive individual plans. Each serial implementation/review
chain carries the charter, plan, and service name through its context input.
Mapped verifier agents consume the chain outputs. A reducer consumes all four
verification results and produces readiness, which selects approval or escalation.

All work is simulated. Inputs really resolve from submitted JSON outputs, but
files, agents, skill execution, deployments, and worktree creation remain simulated.
Readiness is an agent-supplied decision, not a computed all-services-pass predicate.
The success tape supplies coherent results; it does not prove the agent's reasoning.

To explore escalation, replace the assess-readiness output in the tape with
ready=false and advance. The release-manifest branch skips, escalation completes,
and the reporting join still permits the final audit. Reporting never authorizes
release. A malformed structured response leaves that agent waiting for valid JSON.
"""
from pydantic import BaseModel, Field
from harness import Agent, Workflow


class Contract(BaseModel):
    class Config:
        extra = "forbid"


class Charter(Contract):
    objective: str = Field(min_length=1)
    compatibility_rules: list[str]
    rollback_required: bool


class ServicePlan(Contract):
    service: str
    change: str = Field(min_length=1)
    files: list[str]
    rollback: str = Field(min_length=1)


class Implementation(Contract):
    service: str
    summary: str
    changed_files: list[str]
    checks: list[str]


class Review(Contract):
    service: str
    approved: bool
    findings: list[str]
    rollback_reviewed: bool


class Verification(Contract):
    service: str
    passed: bool
    evidence: list[str]


class Readiness(Contract):
    ready: bool
    checked_services: int = Field(ge=0, le=4)
    summary: str = Field(min_length=1)
    unresolved: list[str]


flow = Workflow("four-service-delivery-pipeline")
control = flow.worktree("delivery-control")
charter = Agent(
    prompt="Define a compatible delivery objective and rollback constraints.",
    output=Charter,
).bind(flow, "charter", workspace=control,
       inputs={"request": "Introduce request deduplication across API, worker, billing, and gateway."})

planner = Agent(
    prompt="Plan this service's change using the charter. Identify files and a reversible rollout.",
    output=ServicePlan,
)
implementer = Agent(
    prompt="Implement context.plan within context.charter. Return changed files and check evidence.",
    output=Implementation,
    skills=("diagnose",),
)
reviewer = Agent(
    prompt="Review previous implementation against context.plan and context.charter. Inspect rollback safety.",
    output=Review,
    skills=("diagnose",),
)
verifier = Agent(
    prompt="Independently verify item.review against item.plan. Report concrete evidence and a pass decision.",
    output=Verification,
    skills=("diagnose",),
)
reducer = Agent(
    prompt="Assess all results against the charter. Set ready only if all four services passed with no unresolved issues.",
    output=Readiness,
    skills=("documentation",),
)

items, workspaces = [], []
for service in ("api", "worker", "billing", "gateway"):
    workspace = flow.worktree(service + "-workspace")
    plan = planner.bind(flow, service + "-plan", workspace=workspace,
                        inputs={"service": service, "charter": flow.ref(charter)})
    pipeline = flow.chain(
        service,
        [("implement", implementer), ("review", reviewer)],
        workspace=workspace,
        inputs={"plan": flow.ref(plan)},
        context={"charter": flow.ref(charter), "plan": flow.ref(plan), "service": service},
    )
    items.append({"service": service, "plan": flow.ref(plan), "review": pipeline.output})
    workspaces.append(workspace)

# Map is a static authoring expansion. These IDs are verify-0 through verify-3.
# Each verifier follows its own service review in the same isolated workspace.
checks = flow.map_agents("verify", items, verifier, workspaces=workspaces)
readiness = flow.reduce_agent("assess-readiness", checks, reducer,
                              workspace=control, inputs={"charter": flow.ref(charter)})
approval = flow.approval("approve-delivery", after=readiness,
                         when=flow.ref(readiness, "ready").equals(True))
manifest = flow.agent("release-manifest", workspace=control, after=approval,
                      inputs={"readiness": flow.ref(readiness),
                              "verification": [flow.ref(check) for check in checks]},
                      skills=["documentation"],
                      prompt="Record the approved release manifest and rollback requirements. Do not deploy.")
escalation = flow.agent("escalate-delivery", workspace=control, after=readiness,
                        when=flow.ref(readiness, "ready").equals(False),
                        inputs={"readiness": flow.ref(readiness)},
                        prompt="Explain unresolved readiness issues and request corrective work. Do not release.")
settled = flow.join("delivery-settled", after=[manifest, escalation])
flow.agent("delivery-audit", workspace=control, after=settled,
            inputs={"readiness": flow.ref(readiness), "charter": flow.ref(charter)},
            skills=["documentation"],
            prompt="Record whether delivery was approved or escalated, using the explicit readiness decision.")
flow.export()
