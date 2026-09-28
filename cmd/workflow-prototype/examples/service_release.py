"""Throwaway: can independent service routes converge without unsafe releases?

Everything is simulated. The SDK has no artifact transfer or deployment API.
Agent prompts describe intended work, not enforcement of that work.
"""
from harness import Workflow

flow = Workflow("four-service-release")
control = flow.worktree("release-control", base="current")
plan = flow.agent("release-plan", workspace=control,
                  prompt="Plan compatible API, worker, billing, and gateway changes.")
contract = flow.command("contract-baseline", ["./scripts/contracts", "baseline"],
                        workspace=control, after=plan)


def service_route(service):
    workspace = flow.worktree(service + "-workspace", base="current")
    implement = flow.agent(service + "-implement", workspace=workspace,
                           after=[workspace, contract],
                           prompt="Implement the planned " + service + " changes.")
    # Ordinary dependencies enforce that both checks complete successfully.
    lint = flow.command(service + "-lint", ["./scripts/lint", service],
                        workspace=workspace, after=implement)
    compatibility = flow.command(service + "-compatibility",
                                 ["./scripts/contracts", "check", service],
                                 workspace=workspace, after=implement)
    verify = flow.repeat_check(service + "-verify", ["./scripts/test", service],
                               workspace=workspace, after=[lint, compatibility],
                               repair_prompt="Fix " + service + " regressions.",
                               repair_skills=["diagnose"],
                               max_repairs=2)
    # An exhausted check completes with outcome failed. Only its passed route
    # can reach approval and the simulated rollout.
    review = flow.agent(service + "-review", workspace=workspace, after=verify,
                        when={"step": verify, "outcome": "passed"},
                        prompt="Review correctness, compatibility, and rollback.")
    approve = flow.approval(service + "-approve", after=review)
    rollout = flow.command(service + "-rollout", ["./scripts/rollout", service],
                           workspace=workspace, after=approve)
    observe = flow.repeat_check(service + "-observe",
                                ["./scripts/observe", service],
                                workspace=workspace, after=rollout,
                                repair_prompt="Adjust reversible rollout settings.",
                                max_repairs=1)
    healthy = flow.agent(service + "-healthy", workspace=workspace, after=observe,
                         when={"step": observe, "outcome": "passed"},
                         prompt="Record the healthy rollout and its evidence.")
    rollback = flow.command(service + "-rollback", ["./scripts/rollback", service],
                            workspace=workspace, after=observe,
                            when={"step": observe, "outcome": "failed"})
    rejected = flow.agent(service + "-rejected", workspace=workspace, after=verify,
                          when={"step": verify, "outcome": "failed"},
                          prompt="Explain exhausted verification. Do not release.")
    # Join only route endpoints. Do not include verify itself: its negative
    # outcome still has completed status, which is not release authorization.
    settled = flow.join(service + "-settled", after=[healthy, rollback, rejected])
    return settled


settled = [service_route(service) for service in ("api", "worker", "billing", "gateway")]
report = flow.agent("release-report", workspace=control, after=settled,
                    skills=["documentation"],
                    prompt="Summarize released, rejected, and rolled-back services separately.")
flow.approval("archive-release-report", after=report)
flow.export()
