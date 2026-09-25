---
name: review-change
description: Review a numbered change and produce findings in the requested format.
argument-hint: "[change] [format]"
---

# Review a change

Inspect the diff for $0 and report findings using $1 when supplied.
Prioritize correctness, regression risks, and missing verification.

## Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| change | integer | yes | Change number in the repository. |
| format | string | no | Desired report format. |
