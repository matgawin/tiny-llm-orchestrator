# Run Inspection

## Purpose

Define the read-only run inspection behavior exposed by `orc run status` and
`orc run next`.

## Audience

Contributors changing orchestration inspection, prompt rendering inputs, or
summary-context inputs.

## Read This When

- You are changing `orc run status` or `orc run next`.
- You need to know what run inspection can report before report persistence and
  retry routing are implemented.
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

Both commands are read-only with respect to run domain state. `orc run next`
evaluates the current persisted run state through the workflow engine and
prints the selected action without launching a worker, creating an attempt,
writing an event, or mutating artifacts. Loading a legacy run may still perform
the Run Store's metadata-only `.lock` backfill before reading state.

Unknown run ids fail with a clear not-found error.

## Current State Sources

Inspection reads the run through the Run Store and loads the configured
workflow named by `status.json`.

In the current v1 slice, run start records durable status and task artifacts,
and worker launch records active attempts. Structured report outcomes and retry
lineage are not yet rendered by inspection because later slices own their
durable sources.

For a newly started `running` run with no selected step persisted, `run next`
uses the workflow engine's start-step behavior and reports that start step as
the selected inspect-only action.

For a `running` run with an active attempt, `run status` prints the active
attempt id and `run next` reports `wait_active_attempt` instead of launchable
step selection.

For a `running` run whose latest attempt ended with a launcher-synthesized
failure before report/retry routing exists, `run next` reports
`pending_worker_outcome` and states that no worker should launch. This prevents
manual relaunch without retry accounting.

For `ready_for_human` and `blocked_for_human`, inspection identifies:

- the run directory for current summary context
- the `summaries/` directory for final summary artifacts
- a terminal `run next` decision that explicitly states no worker should launch

When persisted report artifacts exist, terminal inspection output includes
their paths so the orchestrator can tell the human where the relevant context
lives.
