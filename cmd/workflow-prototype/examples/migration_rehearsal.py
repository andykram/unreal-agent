"""Throwaway: can staged rehearsals stop progression and still reach an audit?

No database, filesystem command, or worktree operation actually runs.
All stages use one rehearsal workspace, with forward progression gated by pass.
"""
from harness import Workflow

flow = Workflow("staged-migration-rehearsal")
workspace = flow.worktree("migration-workspace", base="current")
inventory = flow.agent("inventory", workspace=workspace,
                       skills=["cockroachdb-sql"],
                       prompt="Inventory CockroachDB schema consumers and define rollback boundaries.")
snapshot = flow.command("snapshot", ["./scripts/db", "snapshot-scratch"],
                        workspace=workspace, after=inventory)
restore = flow.command("restore-check", ["./scripts/db", "verify-restore"],
                       workspace=workspace, after=snapshot)
permission = flow.approval("approve-rehearsal", after=restore)


def stage(name, previous):
    prepare = flow.agent(name + "-prepare", workspace=workspace, after=previous,
                         prompt="Prepare the " + name + " stage and its inverse operation.")
    plan = flow.command(name + "-explain", ["./scripts/db", "explain", name],
                        workspace=workspace, after=prepare)
    rehearsal = flow.repeat_check(name + "-rehearse",
                                   ["./scripts/db", "rehearse", name],
                                   workspace=workspace, after=plan,
                                   repair_prompt="Repair the " + name + " migration plan.",
                                   repair_skills=["cockroachdb-sql", "diagnose"],
                                   max_repairs=2)
    # Conditional checks exercise when on repeat_check. A failed rehearsal
    # never starts canary work and instead produces a preflight stop report.
    canary = flow.repeat_check(name + "-canary",
                               ["./scripts/db", "canary", name],
                               workspace=workspace, after=rehearsal,
                               when={"step": rehearsal, "outcome": "passed"},
                               repair_prompt="Reduce batch size without widening the canary.",
                               max_repairs=1)
    stop = flow.agent(name + "-preflight-stop", workspace=workspace, after=rehearsal,
                      when={"step": rehearsal, "outcome": "failed"},
                      prompt="Record the rejected rehearsal and preserve diagnostic evidence.")
    rollback = flow.command(name + "-rollback", ["./scripts/db", "rollback", name],
                            workspace=workspace, after=canary,
                            when={"step": canary, "outcome": "failed"})
    rollback_check = flow.command(name + "-rollback-check",
                                  ["./scripts/db", "verify-rollback", name],
                                  workspace=workspace, after=rollback)
    rollback_report = flow.agent(name + "-rollback-report", workspace=workspace,
                                 after=rollback_check,
                                 prompt="Record the reverted canary and why progression stopped.")
    approve = flow.approval(name + "-approve", after=canary,
                            when={"step": canary, "outcome": "passed"})
    widen = flow.command(name + "-widen", ["./scripts/db", "widen-rehearsal", name],
                         workspace=workspace, after=approve)
    advance = flow.agent(name + "-advance", workspace=workspace, after=widen,
                         prompt="Record the stage's accepted result for the next rehearsal.")
    settled = flow.join(name + "-settled", after=[stop, rollback_report, advance])
    # A settled failure is sufficient for auditing, never for forward progress.
    return advance, settled


previous = permission
settled_stages = []
for name in ("expand", "backfill", "contract"):
    previous, settled = stage(name, previous)
    settled_stages.append(settled)

# If an early stage stops, later stages are skipped. At least the stopped
# stage's settled node completes, so this join can still schedule the audit.
end = flow.join("rehearsal-settled", after=settled_stages)
audit = flow.agent("audit", workspace=workspace, after=end,
                   skills=["documentation"],
                   prompt="Audit the completed stages, stopping point, and rollback evidence.")
flow.approval("archive-audit", after=audit)
flow.export()
