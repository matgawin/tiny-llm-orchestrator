# Workflow Engine

## Purpose

Document the deterministic workflow transition contract implemented by
`internal/workflow`.

## Audience

Contributors wiring runtime surfaces to workflow routing.

## Read This When

- You need to evaluate the next workflow action for a run.
- You are changing report, launcher, or run inspection behavior.
- You need to distinguish workflow decisions from run-store persistence.

## Related Docs

- [configuration.md](configuration.md)
- [run-store.md](run-store.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)
- [../features/worker-launching.md](../features/worker-launching.md)

## Ownership

`internal/workflow` owns deterministic routing for validated workflow
definitions. The engine accepts in-memory run state and returns the next
workflow decision.

## Engine Inputs

The engine evaluates a validated `config.Workflow` and a `RunState`.

`RunState` includes:

- `Status`: current run status.
- `SelectedStep`: currently selected workflow step, if one is selected.
- `ActiveAttempt`: whether one worker attempt is active.
- `Outcome`: terminal worker outcome for the selected step, if one exists.
- `Retry`: retry counts consumed per `status/result` pair in the current step
  execution lineage.

Run statuses are:

- `running`
- `ready_for_human`
- `blocked_for_human`
- `cancelled`

Worker report statuses are separate from run statuses:

- `done`
- `blocked`
- `failed`

An outcome is valid only when its `status/result` pair is declared in the
selected step's `allowed_results`. Synthesized failures such as
`failed/timeout`, `failed/missing_report`, `failed/invalid_report`,
`failed/process_error`, and `failed/error` follow the same rule: they are valid
only when declared by the workflow step.

`allowed_results` is broader than worker-authored report input. `orc report`
rejects reserved synthesized/system-owned outcomes such as `failed/timeout`,
`failed/missing_report`, `failed/invalid_report`, `failed/process_error`, and
`failed/error`; it also rejects the system-owned skip outcome `done/skipped`.
Those outcomes enter the workflow engine only from the launcher, report
validation, skip service, or other trusted system paths.

Workflow-declared skip routing uses the normal outcome model. A step is
skippable only when config declares `skippable: true`,
`allowed_results.done: [..., skipped]`, and an explicit `on.done/skipped`
transition. Once a trusted system path supplies `done/skipped`, the workflow
engine evaluates that pair exactly like any other declared outcome pair. Skipped
review is not implicitly approved; it follows the configured `done/skipped`
transition, which may or may not target the same state as approval.

The default repo-local and scaffolded workflows use skip routing only for
explicit human-judgment bypasses: skipped reviews advance to the next configured
review or human handoff, and skipped remediation after reviewer changes advances
to the next review or human handoff. Planning and verification command steps
are not skippable by default.

## Decisions

The engine returns one of these decision kinds:

- `select_step`: a launchable workflow step is selected with no active attempt.
- `retry_step`: retry the same step before applying the configured transition.
- `wait_active_attempt`: the run is already waiting on an active attempt.
- `terminal`: the run is non-runnable because it reached a terminal status.

For a new `running` run with no selected step, the engine selects the
workflow's `start` step.

For `ready_for_human`, `blocked_for_human`, and `cancelled`, the engine returns
a terminal decision and does not select a step.

For sequential v1 workflows, a run cannot have both a launchable selected step
and an active attempt. The engine rejects invalid in-memory states that violate
that invariant.

## Retry Semantics

The engine evaluates retry policy before applying the step's `on` transition.

If retries remain for the outcome's `status/result` pair, the engine returns
`retry_step` for the same step and increments that pair's count in the current
retry lineage. Counts for other pairs in the same step lineage are preserved.
The caller is responsible for creating or persisting any replacement attempt.

If no retry is configured, or retries are exhausted, the engine applies the
configured `on` transition.

Retry counters are keyed by `status/result` pair and scoped to a step execution
lineage. Applying an `on` transition is normal routing and resets all retry
counts for that lineage, even when the transition target is the same step. This
keeps retry decisions distinct from configured workflow loops.

Runtime loop counters are persisted by the run store when routing decisions are
accepted into run state. The workflow engine only returns `select_step`,
`retry_step`, or terminal decisions; callers decide which accepted decisions
record workflow state entries. In v1, `retry_step` is an agent execution retry
and does not increment workflow loop counters.

## Out Of Scope

Adjacent packages and commands own persistence and runtime effects. The
workflow engine does not:

- write run-store events or status files
- create attempt ids
- supersede attempts
- validate report payload schemas beyond declared `status/result` routing pairs
- observe process exits, timeouts, or logs
- render CLI output
