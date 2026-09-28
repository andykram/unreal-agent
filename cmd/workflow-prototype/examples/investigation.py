"""Throwaway: can parallel investigations use nested routes and synthesize?

Prompts describe evidence handling. The prototype does not transfer artifacts
between worktrees, run agents, or evaluate conclusions semantically.
"""
from harness import Workflow

flow = Workflow("multi-strategy-investigation")
control = flow.worktree("incident-control", base="current")
scope = flow.agent("scope", workspace=control,
                   prompt="Scope the latency incident, time window, and falsifiable hypotheses.")
baseline = flow.command("baseline", ["./scripts/incident", "baseline"],
                        workspace=control, after=scope)


def investigate(name, primary, fallback):
    workspace = flow.worktree(name + "-workspace", base="current")
    collect = flow.agent(name + "-collect", workspace=workspace,
                         after=[workspace, baseline],
                         skills=["reviewing-cluster-health"] if name == "database" else ["diagnose"],
                         prompt="Collect independent evidence using " + primary + ".")
    probe = flow.repeat_check(name + "-probe", ["./scripts/incident", "probe", name],
                              workspace=workspace, after=collect,
                              repair_prompt="Correct the experiment without changing its hypothesis.",
                              max_repairs=1)
    interpret = flow.agent(name + "-interpret", workspace=workspace, after=probe,
                            when={"step": probe, "outcome": "passed"},
                            prompt="Explain the successful probe and list confounding factors.")
    # This check exists only within the successful primary route. Its outcome
    # adds a second branch depth before reconvergence.
    validate = flow.repeat_check(name + "-validate",
                                 ["./scripts/incident", "counterexample", name],
                                 workspace=workspace, after=interpret,
                                 when={"step": probe, "outcome": "passed"},
                                 repair_prompt="Improve the control experiment and rerun it.",
                                 repair_skills=["diagnose"],
                                 max_repairs=2)
    supported = flow.agent(name + "-supported", workspace=workspace, after=validate,
                           when={"step": validate, "outcome": "passed"},
                           prompt="Record a supported hypothesis with its remaining uncertainty.")
    disputed = flow.agent(name + "-disputed", workspace=workspace, after=validate,
                          when={"step": validate, "outcome": "failed"},
                          prompt="Record counterevidence and mark this hypothesis disputed.")
    fallback_collect = flow.agent(name + "-fallback", workspace=workspace, after=probe,
                                   when={"step": probe, "outcome": "failed"},
                                   prompt="The primary probe exhausted. Use " + fallback + ".")
    fallback_check = flow.repeat_check(name + "-fallback-check",
                                       ["./scripts/incident", "fallback", name],
                                       workspace=workspace, after=fallback_collect,
                                       repair_prompt="Improve the fallback evidence collection.",
                                       max_repairs=1)
    recovered = flow.agent(name + "-recovered", workspace=workspace, after=fallback_check,
                           when={"step": fallback_check, "outcome": "passed"},
                           prompt="Record fallback findings and their weaker evidence limits.")
    unresolved = flow.agent(name + "-unresolved", workspace=workspace, after=fallback_check,
                            when={"step": fallback_check, "outcome": "failed"},
                            prompt="Record an inconclusive strategy and propose missing evidence.")
    settled = flow.join(name + "-settled", after=[supported, disputed, recovered, unresolved])
    return flow.agent(name + "-summary", workspace=workspace, after=settled,
                       prompt="Summarize this route without turning uncertainty into a claim.")


strategies = [
    ("traces", "distributed traces", "sampled request logs"),
    ("database", "query plans and lock graphs", "offline workload replay"),
    ("runtime", "allocation profiles", "controlled concurrency experiments"),
]
summaries = [investigate(*strategy) for strategy in strategies]
synthesis = flow.agent("synthesis", workspace=control, after=summaries,
                       skills=["documentation"],
                       prompt="Compare independent findings, disagreements, and untested causes.")
challenge = flow.repeat_check("challenge", ["./scripts/incident", "challenge-conclusion"],
                              workspace=control, after=synthesis,
                              repair_prompt="Revise the conclusion to account for counterevidence.",
                              max_repairs=2)
publish = flow.approval("publish-findings", after=challenge,
                        when={"step": challenge, "outcome": "passed"})
gaps = flow.agent("evidence-gaps", workspace=control, after=challenge,
                  when={"step": challenge, "outcome": "failed"},
                  prompt="List unresolved contradictions and commission follow-up experiments.")
end = flow.join("investigation-settled", after=[publish, gaps])
flow.agent("handoff", workspace=control, after=end,
            prompt="Record whether findings were approved or further evidence was requested.")
flow.export()
