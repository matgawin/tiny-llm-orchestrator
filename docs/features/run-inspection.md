# Run Inspection

## Purpose

Define the read-only run inspection behavior exposed by `orc run status` and
`orc run next`.

## Audience

Contributors changing orchestration inspection, prompt rendering inputs, or
summary-context inputs.

## Read This When

- You are changing `orc run status` or `orc run next`.
- You need to know how run inspection reports selected, retrying, blocked, or
  terminal workflow decisions.
- You are wiring later commands that consume the workflow-selected next step.

## Related Docs

- [run-start.md](run-start.md)
- [summary-context.md](summary-context.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../reference/workflow-engine.md](../reference/workflow-engine.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)

## Command Shape

```bash
orc run show <run-id>
orc run status <run-id>
orc run next <run-id>
```

These commands are read-only with respect to run domain state. `orc run show`
is the human-facing status view and currently shares output with
`orc run status`. `orc run next`
evaluates the current persisted run state through the workflow engine and
prints the selected action without launching a worker, creating an attempt,
writing an event, or mutating artifacts. Loading a legacy run may still perform
the Run Store's metadata-only `.lock` backfill before reading state.

Unknown run ids fail with a clear not-found error.

## Current State Sources

Inspection reads the run through the Run Store and loads the configured
workflow named by `status.json`.

Run start records durable status and task artifacts. Worker launch and
`orc report` update durable run status; inspection renders those persisted
facts without mutating the run.

For a newly started `running` run with no selected step persisted, `run next`
uses the workflow engine's start-step behavior and reports that start step as
the selected inspect-only action.

For a `running` run with an active attempt, `run status` prints the active
attempt id and `run next` reports `wait_active_attempt` instead of launchable
step selection.

For a `running` run whose latest attempt ended with a valid worker report,
launcher-synthesized failure, or current-attempt invalid report, inspection
evaluates the persisted `status/result` pair through the workflow engine and
renders the selected next step, retry step, or terminal state. When an outcome
evaluates to `retry_step`, inspection shows the retrying outcome, retry count,
and retry source attempt the next launch would consume. When retries are
exhausted, inspection shows the latest outcome, attempt id, exhausted pair, and
configured terminal transition.

For selected next-step decisions, `orc run next` also previews workflow
loop-cap effects using the same effective caps and persisted counters as worker
launch. Soft-cap previews print a warning for the prospective `soft + 1`
entry. Hard-cap previews identify the blocked target state, prospective count,
current count, hard cap, and `loop_hard_cap_reached` reason. These previews are
read-only: they do not write cap events, increment counters, or move the run to
human handoff.

`orc run show`/`status` include workflow loop-cap status by workflow state:
current entry count, soft and hard thresholds, whether the soft threshold has
been reached, whether a hard cap is currently blocking, and the blocked target
state with prospective count when a hard-cap human decision is active. If a
human-reviewed loop-cap override is pending, the status output shows the
pending override action and the one allowed count-after value.

`orc run show`/`status` also include audited `skipped_steps` materialized from
`workflow.step_skipped` events. Each skipped step shows step id, `done/skipped`,
reason, event sequence, timestamp, and source when recorded. Skipped steps are
not worker attempts and do not appear in `status.attempts`.

For `ready_for_human` and `blocked_for_human`, inspection identifies:

- the run directory for current summary context
- the `summaries/` directory for final summary artifacts
- a terminal `run next` decision that explicitly states no worker should launch

When persisted report artifacts exist, terminal inspection output includes
their paths so the orchestrator can tell the human where the relevant context
lives.
