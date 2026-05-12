# Run Store Events Reference

## Purpose

Provide the v1 append-only event log and event payload contract for durable run
state under `.orc/runs`.

## Related Docs

- [run-store.md](run-store.md)
- [run-store-layout.md](run-store-layout.md)
- [run-store-status-artifacts.md](run-store-status-artifacts.md)
- [run-store-operations.md](run-store-operations.md)

## Events

`events.jsonl` is append-only JSON Lines. Each line is one event, and every
line, including the final line, must end with `\n`; a missing trailing newline
is treated as incomplete state.
The file follows the [filesystem safety](run-store-layout.md#filesystem-safety)
rules.

Required event fields:

```json
{
  "schema_version": 1,
  "sequence": 1,
  "time": "2026-05-02T14:30:22Z",
  "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
  "type": "run.created",
  "payload": {
    "workflow": "implementation",
    "task_slug": "main-997",
    "workflow_state_entry": {
      "workflow": "implementation",
      "state": "plan",
      "count": 1
    }
  }
}
```

Ordering is defined by the monotonically increasing `sequence` field. Timestamps are metadata.

Latest-state changes are recorded as `status.updated` events and then materialized into `status.json`.

### Caller Events

Callers may append custom events with non-reserved event types. Reserved event
types are `run.created`, `status.updated`, `artifact.written`,
`attempt.started`, `attempt.prompted`, `attempt.logged`, `attempt.process_started`,
`attempt.finished`, `attempt.recovered`, `attempt.reported`, `attempt.warning`,
`report.ignored`, `run.continued`, `workflow.loop_soft_cap`,
`workflow.loop_hard_cap`, `workflow.loop_hard_cap_override`,
`workflow.step_skipped`, and `config_snapshot_refreshed`; those are written
only through the dedicated store APIs.

For caller events, callers provide:

- `type`
- optional `payload`
- optional `time`

The store assigns or overwrites:

- `schema_version`
- `sequence`
- `run_id`
- `time` when omitted

An empty payload is stored as `{}`. Caller events advance `status.json`
`updated_at` and `last_sequence`; they do not change `state` or artifact
references.

### V1 Event Types

`run.created` is written once when a run directory is created. Explicit run ids
reserve the final run directory atomically; creation fails if that path already
exists, including an empty directory.

```json
{
  "workflow": "implementation",
  "task_slug": "main-997",
  "workflow_state_entry": {
    "workflow": "implementation",
    "state": "plan",
    "count": 1
  }
}
```

`status.updated` is written by the status update API before the latest state is materialized into `status.json`.

```json
{
  "state": "ready_for_human",
  "workflow_state_entry": {
    "workflow": "implementation",
    "state": "ready_for_human",
    "count": 1,
    "previous_state": "code",
    "trigger_status": "done",
    "trigger_result": "ready"
  }
}
```

The `workflow_state_entry` field is present when the status update is an
accepted workflow transition into a terminal or human-handoff state. Terminal
states are counted for auditability; loop cap enforcement applies only to
worker-selecting transitions.

`artifact.written` is written when the store persists a standalone artifact.
Markdown report details accepted with `orc report` are the exception: their
artifact reference is embedded in the `attempt.reported` payload instead of a
separate `artifact.written` event.

```json
{
  "artifact": "<artifact reference>"
}
```

The artifact reference shape is defined in
[Artifacts](run-store-status-artifacts.md#artifacts).

`attempt.started` is written when a worker or deterministic command/script
launch creates a `starting` attempt.
The attempt remains the run's `active_attempt` while launch preparation
continues.

```json
{
  "attempt": {
    "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
    "step_id": "plan",
    "agent_id": "planner",
    "attempt_id": "20260504T120000Z-plan-a1b2c3",
    "state": "starting",
    "timeout": "30m0s",
    "report_exit_grace": "30s",
    "started_at": "2026-05-04T12:00:00Z"
  }
}
```

For command and script steps, `agent_id` is the system actor id for the
deterministic kind (`command` or `script`). These attempts still use the same
attempt id, start, active process, terminal report, retry lineage, and workflow
routing fields as agent attempts.

When `attempt.started` consumes a previously terminal outcome, routing fields
are added beside the `attempt` object:

- `consume_attempt_id`: required when a start consumes the latest terminal
  outcome, so routing cannot be skipped.
- `retry_lineage`: retry-only metadata with updated retry counts for the
  replacement attempt's step execution lineage.
- `supersede_reason`: retry-only `status/result` text stored on the consumed
  attempt when retry lineage is present.
- `workflow_state_entry`: next-step routing metadata when the accepted decision
  enters a new worker-selecting workflow state. Agent execution retries omit
  this field because retry counts are separate from workflow loop counts.

```json
{
  "consume_attempt_id": "20260504T120000Z-plan-a1b2c3",
  "retry_lineage": {
    "step_id": "plan",
    "counts": {
      "failed/missing_report": 1
    }
  },
  "supersede_reason": "failed/missing_report"
}
```

Workflow state entries are counted by state name. The initial workflow start
state is recorded at run creation with count `1`. Later counts increment only
when routing is accepted into durable run state: a selected worker state in
`attempt.started`, a terminal/human state in `status.updated`, or an audited
skip transition in `workflow.step_skipped`. Failed report validation and
`report.ignored` events do not increment these counters.

`workflow.step_skipped` is written by the internal trusted skip service when a
human decision bypasses the currently selected skippable step. It records the
system-owned accepted outcome `done/skipped`, applies the configured transition
in the same locked mutation, clears retry lineage, and does not create an
active attempt, terminal attempt, worker report, or `status.attempts` entry.
When the skipped step was selected by routing a previous terminal attempt
outcome, `consume_attempt_id` records that outcome so replay and future
workflow evaluation do not consume it again.

```json
{
  "step_id": "review",
  "status": "done",
  "result": "skipped",
  "reason": "not worth another review",
  "source": "human",
  "consume_attempt_id": "20260504T120000Z-plan-a1b2c3",
  "state": "running",
  "workflow_state_entry": {
    "workflow": "implementation",
    "state": "redundancy-review",
    "count": 1,
    "previous_state": "review",
    "trigger_status": "done",
    "trigger_result": "skipped"
  }
}
```

When the configured `done/skipped` transition targets a terminal run state,
`state` is that terminal state and the workflow state entry records that
terminal state. The append-only event remains the source of truth; `status.json`
materializes `skipped_steps` from these events.

`config_snapshot_refreshed` is written by explicit config refresh after the
new version directory is committed and `config/current.json` points to it. It
does not add an artifact reference; config snapshots live under `config/`.

```json
{
  "old_version": 1,
  "old_version_dir": "000001",
  "new_version": 2,
  "new_version_dir": "000002",
  "manifest_hash_algorithm": "sha256",
  "manifest_hash": "<sha256 of manifest.json bytes>",
  "source": "cli"
}
```

`workflow.loop_soft_cap` is written once per workflow state when a
worker-selecting transition reaches prospective count `soft + 1`. The launcher
still starts the worker.

```json
{
  "cap": {
    "workflow": "implementation",
    "state": "code",
    "count": 3,
    "soft": 2,
    "hard": 4,
    "previous_state": "test",
    "trigger_status": "done",
    "trigger_result": "passed"
  }
}
```

`workflow.loop_hard_cap` is written instead of starting a worker when a
worker-selecting transition would reach prospective count `hard + 1`. The
target state's persisted count is not incremented by this event, and the run
state is materialized as `blocked_for_human`.

```json
{
  "state": "blocked_for_human",
  "cap": {
    "workflow": "implementation",
    "blocked_target_state": "code",
    "current_count": 4,
    "prospective_count": 5,
    "soft": 2,
    "hard": 4,
    "previous_state": "test",
    "trigger_status": "done",
    "trigger_result": "passed",
    "reason": "loop_hard_cap_reached"
  }
}
```

`workflow.loop_hard_cap_override` is written only by an explicit human-reviewed
continuation command. It clears the active hard-cap block, returns the run to
`running`, and materializes `pending_hard_cap_override` for exactly the blocked
target state and prospective count. The next matching `attempt.started` event
includes the consumed override and clears the pending override while recording
the normal workflow state entry.

```json
{
  "state": "running",
  "override": {
    "workflow": "implementation",
    "target_state": "code",
    "count_before_override": 4,
    "count_after_override": 5,
    "soft": 2,
    "hard": 4,
    "human_action": "allow_loop_cap",
    "reason": "loop_hard_cap_reached"
  }
}
```

`run.continued` is written by `orc run continue <run-id> --resolve-block
--reason <text>` after a human resolves a non-loop `blocked_for_human` blocker
outside Orc. The reason is trimmed before validation and persistence. Replay
requires the previous materialized state to be `blocked_for_human`, no active
attempt, no active workflow-loop hard-cap block, and the latest attempt to be a
terminal routing outcome matching the resolved fields. The latest workflow-loop
entry must also be the transition into `blocked_for_human` with trigger
status/result matching that attempt; a manually stale blocked state without that
routing evidence is not resumable.

The event returns the run to `running` and materializes a `continued` marker
with mode `resolve_block`. That marker makes workflow evaluation select the
resolved step without re-consuming the old blocked terminal outcome. The next
matching `attempt.started` event records the normal workflow-loop entry and
count for selecting that step again, then clears the marker. This continuation
does not rewrite prior attempts or reports, create a worker attempt, create or
consume a loop-cap override, reset retry lineage, or reset workflow-loop
counters.

```json
{
  "mode": "resolve_block",
  "previous_state": "blocked_for_human",
  "new_state": "running",
  "reason": "fixed workflow config and reran checks",
  "resolved_attempt_id": "20260507T023810Z-code-0b0dbb",
  "resolved_step_id": "code",
  "resolved_status": "blocked",
  "resolved_result": "blocked"
}
```

Retry starts derive supersession from `consume_attempt_id` plus `retry_lineage`.

`attempt.prompted` links the rendered prompt artifact to the current attempt.
The link is one-time and only valid while the current attempt is `starting`.

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "prompt_ref": "<artifact reference>"
}
```

`attempt.logged` links the durable log artifact to the current attempt before
the worker process starts. The link is one-time and only valid while the current
attempt is `starting`.

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "log_ref": "<artifact reference>"
}
```

`attempt.process_started` records worker process metadata and transitions the
attempt state from `starting` to `active`. The process-start event requires the
current `starting` attempt to already have both `prompt_ref` and `log_ref`.
`process_start_time` is the launcher-read process identity from procfs and must
be a non-empty decimal string.

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "pid": 12345,
  "process_start_time": "123456789"
}
```

`attempt.finished` terminalizes the active attempt with a
launcher-synthesized outcome. Terminal state/status/result tuples and
pre-process restrictions are defined in
[Attempt Lifecycle Preconditions](run-store-operations.md#attempt-lifecycle-preconditions).

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "state": "missing_report",
  "status": "failed",
  "result": "missing_report",
  "exit_code": 0,
  "exit_state": "exited",
  "log_ref": "<artifact reference>"
}
```

`attempt.recovered` terminalizes an active attempt that cannot be verified after
launcher restart, or an expired active attempt whose process identity is still
live. V1 records unverifiable attempts as `failed/process_error` with
`exit_state=unknown`. Expired live attempts are recorded as `failed/timeout`
with `state=timed_out` and `exit_state=timeout`.

`attempt.warning` records process facts that do not change the authoritative
attempt outcome. The launcher uses warning events when a valid reported attempt
is followed by a nonzero worker exit or when a still-running worker is
terminated after `report_exit_grace`.

```json
{
  "warning": {
    "attempt_id": "20260504T120000Z-plan-a1b2c3",
    "kind": "post_report_process_exit",
    "exit_code": 1,
    "exit_state": "exited",
    "message": "worker exited nonzero after valid report; report remains authoritative",
    "time": "2026-05-04T12:00:05Z"
  }
}
```

`attempt.reported` terminalizes the run's current `active_attempt` with a
structured report. Agent reports are accepted through `orc report` after the
attempt reaches `active`. Orc-authored command/script reports may also
terminalize a `starting` attempt for process setup failures that occur before
process metadata can be recorded.

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "state": "reported",
  "exit_code": 0,
  "exit_state": "exited",
  "log_ref": "<artifact reference>",
  "report": {
    "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
    "step_id": "plan",
    "agent_id": "planner",
    "attempt_id": "20260504T120000Z-plan-a1b2c3",
    "status": "done",
    "result": "ready",
    "summary": "Plan is ready.",
    "changed_paths": ["README.md"],
    "commands": ["go test ./internal/cli"],
    "tests": ["go test ./internal/cli"],
    "risks": ["none"],
    "followups": [
      {
        "title": "Document report summaries"
      }
    ],
    "report_ref": "<artifact reference>"
  }
}
```

Valid reports use `state=reported` and preserve the workflow `status/result`
pair. Current-attempt reports that fail schema or allowed-pair validation use
`state=invalid_report`, `status=failed`, and `result=invalid_report`.
For CLI JSON input, `--json-file` is mutually exclusive with report field flags;
when the JSON payload identifies the current active attempt, that mix is
schema-invalid report input. Unknown JSON fields, nested unknown JSON fields,
and trailing JSON values are schema-invalid. Markdown report files must be
readable, regular, non-symlink files; failures for the current active attempt are
recorded as invalid reports instead of leaving the attempt active.

`report.ignored` records malformed, stale, wrong-step, wrong-agent, or
wrong-attempt reports that provide enough identity to locate a run but do not
prove they target the current active attempt. Reports without `run_id` cannot be
recorded as run-local events because the store cannot identify the owning run.
Ignored reports do not change active attempt state or consume retry policy.

```json
{
  "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "old-attempt",
  "reason": "report does not target current active attempt",
  "errors": ["report attempt_id does not match active attempt"]
}
```
