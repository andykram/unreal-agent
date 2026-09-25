---
title: Troubleshoot a workflow
description: Resolve graph export errors, invalid dependencies, blocked branches, and runner setup issues.
---

Start by exporting the script without the viewer:

```sh
./run.sh -script branching.py -json
```

Use this command from `cmd/workflow-prototype`. A successful result confirms
Python execution, JSON decoding, and graph validation.

## Fix export errors

| Symptom | Action |
| --- | --- |
| `workflow must export one JSON document` | Call `flow.export()` once. Remove other stdout prints. Send diagnostics to stderr. |
| `CPython WASI` error | Read the Python traceback on stderr. Check imports, method arguments, and interpreter limits. |
| Missing script | Use an absolute `-script` path, or a path relative to the prototype directory. |
| Missing `harness` during local Python execution | Use `./run.sh`. The WASI host provides the module automatically. |

```python
import sys
print("Building graph", file=sys.stderr)
```

The selected script and embedded SDK modules are mounted at `/workflow`.
Sibling Python files are not copied automatically. Pydantic is bundled with the
runtime; other local third-party packages are not copied automatically.

## Fix validation errors

| Error | Action |
| --- | --- |
| Named version 1 graph required | Set a nonempty workflow name and add at least one step. |
| Empty or duplicate ID | Assign a unique, nonempty string to every step. |
| Missing dependency | Use an ID returned by a step-creation method. |
| Dependency cycle | Remove the circular edge. Use `repeat_check` for the supported repair loop. |
| Unknown kind | Use one of the six supported kinds. Generic `step()` does not add executors. |
| Invalid condition | Use a check outcome or a scalar output comparison, with its source dependency. |
| Invalid join | Supply at least one dependency and remove any condition. |
| Invalid `max_repairs` | Supply an integer from `0` through `10`, not a boolean or float. |

## Explain stalled or skipped steps

- A waiting check needs `p ID` or `b ID`. Interactive `n` alone does not resolve it.
- Approval needs `a`. Automatic mode never approves.
- An execution failure leaves dependents blocked. Reset with `r` to try another simulation.
- A mismatched branch skips. Ordinary steps depending on that branch also skip.
- Use `join` to converge exclusive branches. It waits for all inputs to complete or skip.
- A join skips if every input skips. An execution failure blocks a join.

## Check runner prerequisites

Use Go 1.27 or newer. Ensure `curl`, `unzip`, and `shasum` are available for the
first download. A checksum mismatch stops setup. Do not bypass verification.

If the Go build reports a cache permission error, configure writable `GOCACHE`
and `GOMODCACHE` paths before running the script. Runtime downloads use
`XDG_CACHE_HOME` separately.

Repeated startup compilation is expected because no Wasm compilation cache is
implemented. See [CLI reference](/reference/cli/) for paths and flags.
