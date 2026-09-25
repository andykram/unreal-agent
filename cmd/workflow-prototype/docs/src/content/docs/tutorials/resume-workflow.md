---
title: Resume a workflow
description: Save a structured result, stop the viewer, and continue the same run.
---

This tutorial saves a structured result and resumes the same workflow without
submitting that result again. Complete the [quickstart](/tutorials/quickstart/) first.

## 1. Start an isolated run

From `cmd/workflow-prototype`:

```sh
workflow_state_dir=$(mktemp -d)
./run.sh -script examples/structured_review.py -state-dir "$workflow_state_dir"
```

Copy the `Workflow run` ID or the printed `Resume` command. Keep this shell and its storage directory
for the resume command.

## 2. Save a result

Enter `n` twice. Then submit this one-line result:

```text
o review {"verdict":"approve","summary":"No blocking findings","findings":[],"confidence":0.9,"tests_reviewed":true}
```

Wait for the viewer to show the completed review and pending acknowledgment.
That displayed transition has a saved checkpoint.

## 3. Stop and resume

Enter `q`. Replace `RUN_ID` with the copied ID:

```sh
./run.sh -resume RUN_ID -state-dir "$workflow_state_dir"
```

The review remains complete, with its structured output restored. The
`acknowledge-review` approval remains pending. Python authoring is not rerun, and
the completed review does not need another output submission. Its logical and
attempt keys remain the same. The runner also skips Python runtime setup on resume.

Enter `a` to complete the approval, then `q` to leave the completed run saved.

## 4. Check interruption recovery

Resume the same run again. Confirm it is still completed. Interrupt the viewer
with Ctrl+C, then resume using the same command. The last committed state remains
available even though the viewer did not exit through `q`.

For an abrupt-process crash check, terminate only this viewer process after its
checkpoint is visible, then resume it. This exercises process recovery. It does
not establish recovery from disk loss or exactly-once external operations.

## 5. Start a separate experiment

In the resumed viewer, enter `r`. The graph starts in a new run with a new ID.
The old run still exists and can be resumed independently. The new run has new
node idempotency keys. Changes to the source
script do not alter either run's saved graph.

The default retention policy eventually removes inactive completed runs. Waiting,
blocked, and failed runs are preserved. Keep the temporary directory while testing;
removing it removes these isolated run records.

See [storage semantics](/explanation/storage/) for checkpoint boundaries and cleanup.
