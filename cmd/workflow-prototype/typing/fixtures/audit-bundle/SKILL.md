---
name: audit-bundle
description: Inspect a bundle and report large dependencies within a size budget.
---

# Audit a bundle

## Usage

```text
/audit-bundle <bundle> [--budget <budget>]
```

## Parameters

| Name | Type | Required | Description |
| --- | --- | --- | --- |
| bundle | path | yes | Bundle to inspect. |
| budget | integer | no | Maximum size in bytes. |

Use existing build output. Do not run or import the bundle.
