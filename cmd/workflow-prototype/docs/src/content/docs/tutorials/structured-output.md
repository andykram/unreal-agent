---
title: Explore structured agent output
description: Generate a response schema and validate a simulated review result.
---

This tutorial uses `examples/structured_review.py`. Complete the
[quickstart](/tutorials/quickstart/) first to install the WASI runtime.

## 1. Read the output model

The example defines nested output classes with required fields:

```python
from typing import Literal
from harness import OutputModel

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
```

`OutputModel` wraps real Pydantic. The WASI runtime bundles its pure Python v1
distribution. Nullable fields without an explicit required marker can be omitted
under this version; use Pydantic `Field(...)` when presence is mandatory.

## 2. Attach the model to an agent

```python
review = flow.agent("review", workspace=workspace,
                    skills=["diagnose"],
                    prompt="Review the proposed changes. Return concrete findings.",
                    output=Review)
flow.approval("acknowledge-review", after=review)
```

`output` takes the model class. The builder writes its schema into
`spec.output_schema`. Export it from `cmd/workflow-prototype`:

```sh
./run.sh -script examples/structured_review.py -json
```

The schema records required properties, forbids extra properties, and includes the
nested finding schema. No model request occurs during export.

## 3. Supply a simulated result

```sh
./run.sh -script examples/structured_review.py
```

Enter `n` twice. The workspace completes, then `review` waits for structured output.
Submit a JSON object on one line:

```text
o review {"verdict":"approve","summary":"No blocking findings","findings":[],"confidence":0.9,"tests_reviewed":true}
```

The viewer validates the JSON, displays the stored result, and completes `review`.
Enter `a` to acknowledge it. This approval is unconditional: the graph does not
branch on `verdict`. Other workflows can use output references and scalar conditions.

## 4. Reject an invalid result

Reset with `r`, then enter `n` twice. Try a missing-field object:

```text
o review {"verdict":"approve"}
```

Validation fails and the node keeps waiting. Submit the valid object from step 3
to continue. Extra fields, incorrect types, and unknown enum values are invalid too.

Automatic mode leaves agents waiting for output while advancing other ready work.
It does not invent a result.
Results persist with the run. `r` creates a new run and preserves the old result;
`q` exits without discarding saved state.

## 5. Validate JSON in Python

The same model also works during Python execution:

```python
review = Review.model_validate_json(
    '{"verdict":"approve","summary":"No blocking findings",'
    '"findings":[],"confidence":0.9,"tests_reviewed":true}'
)
assert review.verdict == "approve"
assert review.model_dump()["tests_reviewed"] is True
schema = Review.model_json_schema()
```

Authoring finishes before simulated execution. This Python example validates a
known JSON string; it does not receive the viewer's later result.

See [OutputModel reference](/reference/python-api/#outputmodel) for supported
annotations and restrictions. The workflow prototype does not call a provider,
execute agents, or integrate with the production harness executor.
