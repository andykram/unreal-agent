# Complex workflow experiments

These standalone examples ask whether one static graph can express useful
parallel work, nested decisions, bounded repairs, and branch convergence.
Python constructs each graph once. Go simulates the execution afterward.

Run these commands from `cmd/workflow-prototype`:

```sh
./run.sh -script examples/service_release.py
./run.sh -script examples/migration_rehearsal.py
./run.sh -script examples/investigation.py
```

Add `-json` to validate and inspect a graph without running the viewer.
Add `-auto` to simulate passing checks until approvals stop progress.
Interactive state is in memory. Starting another process or entering `r`
starts a new simulation, with no recovery of previous decisions.

| Example | Nodes | Main topology |
| --- | ---: | --- |
| `service_release.py` | 57 | Four independent worktrees, parallel checks, three terminal routes per service, aggregate report |
| `migration_rehearsal.py` | 44 | Three sequential stages, nested canary decisions, rollback and early stop, final audit |
| `investigation.py` | 48 | Three independent strategies, nested validation, fallback experiments, synthesis and challenge |

## Simulator controls

Use `n` until the desired node is waiting for a check result or approval.

| Input | Effect |
| --- | --- |
| `n` | Execute the next ready batch, or finish ready repair actions |
| `p ID` | Pass a waiting check |
| `b ID` | Return a negative check result, scheduling repair or exhausting the budget |
| `a` | Approve **all** ready approvals |
| `f ID` | Cause an execution error in a ready or running node |
| `r` | Clear simulation state |
| `q` | Exit |

After `b ID` schedules a repair, use `n` before providing the next check result.
A node with `max_repairs=2` allows three check results. Exhaustion completes
the node with outcome `failed`. It is different from `f ID`, which sets its
execution status to `failed`. Invalid check inputs are ignored.

## Four-service release

Each service has its own workspace. Implementation waits for the shared contract
baseline. Lint and compatibility checks run independently. Verification may
repair twice before choosing release review or rejection.

The passed route requires approval before the simulated rollout. Observation
then selects a healthy report or rollback. The exhausted verification route
cannot reach the rollout. Each service join waits for its route endpoints.
The aggregate report requires all four service joins to complete.

### Walkthroughs

1. **All healthy:** pass `api-verify`, `worker-verify`, `billing-verify`, and
   `gateway-verify`. Advance reviews, then approve. Pass each corresponding
   `*-observe`. Advance to `release-report` and approve `archive-release-report`.
   All four `*-healthy` endpoints complete. Rollback and rejection endpoints skip.
2. **One rejected service:** exhaust `billing-verify` with three `b` results,
   advancing repairs between them. Pass the other checks and approvals.
   `billing-rejected` completes. `billing-approve`, `billing-rollout`, and
   observation descendants skip. The aggregate report still becomes available.
3. **One rollback:** pass all verification checks. After approvals and rollouts,
   exhaust `api-observe` with two `b` results. Pass the other observations.
   `api-rollback` completes instead of `api-healthy`. Reporting still converges.
4. **Operational error:** use `f api-rollout` when it is ready. That route cannot
   produce a successful observation endpoint. Its join and aggregate report do
   not complete. This is an interruption, not a business rejection route.

Representative skill metadata: verification repairs reference `diagnose`, and
the aggregate report references `documentation`.

## Staged migration rehearsal

The workflow models a scratch CockroachDB migration. Snapshot and restore checks
precede approval. Stages then run in order: expand, backfill, contract.

Each stage has a plan rehearsal, a conditional canary check, and an approval
before widening. Exhausted rehearsal creates a preflight stop report.
Exhausted canary triggers rollback, rollback verification, and a rollback report.
Only the `*-advance` endpoint starts the next stage. The stage's reporting join
does not authorize progression.

### Walkthroughs

1. **All stages accepted:** approve `approve-rehearsal`. For each stage, pass
   `*-rehearse`, pass `*-canary`, approve, and advance. After `contract-advance`,
   the final audit becomes ready. Approve `archive-audit` to finish.
2. **Middle-stage rollback:** finish expand. Pass `backfill-rehearse`, then
   exhaust `backfill-canary` with two `b` results. Advance rollback and its
   verification. The backfill rollback report completes. Contract skips,
   and `audit` still becomes ready.
3. **Early preflight stop:** exhaust `expand-rehearse` with three `b` results.
   Canary work, backfill, and contract skip. The preflight stop report allows
   `rehearsal-settled` and then `audit` to complete.
4. **Rollback itself fails:** on the rollback route, use `f backfill-rollback-check`.
   The workflow cannot claim verified rollback. The affected stage's join does
   not complete, and the final audit remains blocked.

Representative skill metadata: schema inventory references `cockroachdb-sql`,
rehearsal repair references `cockroachdb-sql` and `diagnose`, and audit references
`documentation`.

## Multi-strategy investigation

Trace, database, and runtime strategies use separate workspaces. Each strategy
probes a hypothesis. Success leads to a second bounded validation experiment,
whose result chooses supported or disputed findings. Exhausted primary probing
selects a fallback experiment, whose result chooses recovered or unresolved
findings. Each strategy joins its four mutually exclusive route endpoints.

Synthesis waits for all three strategy summaries. A final bounded challenge
selects publication approval or an evidence-gap report. Both routes converge
into handoff. A handoff means the investigation reached an explicit disposition,
not that a hypothesis was proven.

### Walkthroughs

1. **All supported:** pass every `*-probe` and `*-validate`. Pass `challenge`,
   approve `publish-findings`, and advance to `handoff`.
2. **Mixed evidence:** pass `traces-probe`, then exhaust `traces-validate` with
   three `b` results. Exhaust `database-probe` with two `b` results, then pass
   `database-fallback-check`. Let runtime pass both checks. Synthesis still
   runs, with disputed, recovered, and supported route endpoints completed.
3. **Inconclusive investigation:** exhaust all primary probes and fallback
   checks. All three `*-unresolved` endpoints complete, so synthesis can run.
   Exhaust `challenge` with three `b` results. `evidence-gaps` then reaches
   handoff, while publication approval skips.
4. **Operational interruption:** use `f runtime-collect` when ready. Runtime
   cannot produce a summary, so synthesis does not run. Execution errors are
   not silently converted into inconclusive findings.

Representative skill metadata: database evidence collection references
`reviewing-cluster-health`, other collection references `diagnose`, and synthesis
references `documentation`.

## What these examples establish and what they do not

- `join` is an all-settled convergence point, not a first-winner race or quorum.
  Every direct dependency must complete or skip, and at least one must complete.
  All-skipped joins skip. An execution-failed direct dependency blocks a join.
- Ordinary dependencies require completed parents. An unselected branch and
  its descendants skip. A join is not a global ancestor-failure detector.
- A completed check with a negative outcome is still completed. Success-only
  actions depend on the selected passed route, not a reporting join.
- Worktrees, agents, commands, approvals, and skill loading do not execute real
  operations. Command paths are illustrative and need no corresponding files.
- `skills` and `repair_skills` contain registered skill names as metadata.
  This simulator does not resolve names, read skill files, or inject their text.
- Prompts describe intended summaries and evidence. There is no artifact
  transport or automatic transfer of outputs between workspaces.
- No generic predicates, dynamic subgraphs, approval rejection, quorum rules,
  durable recovery, or real integrations are implied by these examples.

These are throwaway topology experiments. Keep the lessons, then absorb or
remove the examples when the production execution model is chosen.

## Structured review

`structured_review.py` is a focused three-node companion example. It declares a
nested `Review` model, attaches it to an agent, and waits for manual structured
output before approval. Run `./run.sh -script examples/structured_review.py`.
Enter `n` twice, then submit a matching value:

```text
o review {"verdict":"approve","summary":"Reviewed","findings":[],"confidence":0.9,"tests_reviewed":true}
a
```

Invalid JSON results keep the agent waiting. This validates data shape, not the
truth of the review. The approval acknowledges any valid review, including a
changes-requested verdict; it does not authorize integration. Other agents can consume fields through `flow.ref(review, "verdict")`.
Scalar equality conditions can branch on those fields; this small example does
not add a verdict-based decision.

## Agent pipelines

`agent_pipeline.py` has 13 nodes: three research outputs feed a reducer, followed
by a critique/edit chain. The editor receives shared brief context as well as
the previous critique. A boolean output field selects publication or follow-up.

`delivery_pipeline.py` has 28 nodes: a charter feeds four service plans and
implementation/review chains. Mapped verifiers pass results to a readiness agent.
A supplied readiness boolean selects approval or escalation, followed by audit.
The graph enforces dependencies and data shape, not truth of readiness claims.

```sh
./run.sh -script examples/agent_pipeline.py
./run.sh -script examples/delivery_pipeline.py < examples/delivery_pipeline.inputs.txt
```

Every `o ID JSON` line in the delivery tape is a simulated agent response.
Edit the final readiness to false to exercise escalation. Map expansion uses a
list known during authoring; it does not spawn agents from a runtime-sized list.

## Parameterized skills

Generate typed builders from the bundled skill fixtures, then mount their module:

```sh
python3 typing/generate_skills.py typing/fixtures/*/SKILL.md --output-dir /tmp/workflow-example-skills
./run.sh -script examples/skill_parameters.py -skills-module /tmp/workflow-example-skills/generated_skills.py
```

The fixtures describe hypothetical skills, not installed registry entries.
The builders produce name/argument metadata. Nothing executes these skills.
