# Run Inspection

## Purpose

Define the read-only run inspection behavior exposed by `orc run status` and
`orc run next`.

## Audience

Contributors changing orchestration inspection, prompt rendering inputs, or
summary-context inputs.

## Read This When

- You are changing `orc run status` or `orc run next`.
- You need to know what run inspection can report before worker launch and
  report persistence are implemented.
- You are wiring later commands that consume the workflow-selected next step.

## Related Docs

- [run-start.md](run-start.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../reference/workflow-engine.md](../reference/workflow-engine.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)

## Command Shape

```bash
orc run status <run-id>
orc run next <run-id>
```

Both commands are read-only. `orc run next` evaluates the current persisted run
state through the workflow engine and prints the selected action without
launching a worker, creating an attempt, writing an event, or mutating
artifacts.

Unknown run ids fail with a clear not-found error.

## Current State Sources

Inspection reads the run through the Run Store and loads the configured
workflow named by `status.json`.

In the current v1 slice, run start records durable status and task artifacts.
Inspection marks the following details as unavailable until later slices record
durable sources for them:

- active attempts
- structured report outcomes
- retry lineage

For a newly started `running` run with no selected step persisted, `run next`
uses the workflow engine's start-step behavior and reports that start step as
the selected inspect-only action.

For `ready_for_human` and `blocked_for_human`, inspection identifies:

- the run directory for current summary context
- the `summaries/` directory for final summary artifacts
- a terminal `run next` decision that explicitly states no worker should launch

When persisted report artifacts exist, terminal inspection output includes
their paths so the orchestrator can tell the human where the relevant context
lives.
