# Run Store Status and Artifacts Reference

## Purpose

Provide the v1 latest status and artifact reference contract for durable run
state under `.orc/runs`.

## Related Docs

- [run-store.md](run-store.md)
- [run-store-layout.md](run-store-layout.md)
- [run-store-events.md](run-store-events.md)
- [run-store-operations.md](run-store-operations.md)

## Latest Status

`status.json` is the materialized fast-read state for a run. The append-only
event log is the source of truth; when `status.json` lags behind the event log,
loaders replay events and return reconstructed latest state. `status.json` is
still required bootstrap metadata: it must exist and contain valid schema,
`run_id`, workflow, and timestamp fields before replay can proceed.
Stale materialized fields, including extra artifact references, may be ignored
during replay because events are authoritative.

Store-written status files contain:

```json
{
  "schema_version": 1,
  "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
  "workflow": "implementation",
  "state": "running",
  "created_at": "2026-05-02T14:30:22Z",
  "updated_at": "2026-05-02T14:30:22Z",
  "last_sequence": 1,
  "artifacts": [],
  "attempts": [],
  "warnings": [],
  "workflow_loop": {
    "counts": {
      "plan": 1
    },
    "entries": [
      {
        "workflow": "implementation",
        "state": "plan",
        "count": 1
      }
    ]
  }
}
```

In v1, `state` is caller-validated. The store requires status updates and
`status.updated` event payloads to provide a non-empty state string, but
workflow/report layers own the allowed-state policy.

`status.json` materializes current attempt state and attempt history:

- Starting or active workers appear in `active_attempt` and `attempts`.
- Terminal attempts remain in `attempts`; terminalizing an attempt removes
  `active_attempt`.
- Retry launches materialize the `attempt.started` routing metadata on latest
  status.
- Process warnings are materialized in `warnings`.
- Workflow loop state is materialized in `workflow_loop`. `counts` stores the
  latest count per workflow state, `entries` preserves the accepted run path,
  `repeated_states` lists states whose count has reached at least `2`,
  `soft_cap_warnings` stores advisory threshold hits, and `hard_cap_block`
  stores the active hard-cap human-decision stop when present.
  `pending_hard_cap_override` stores the one-shot human-reviewed continuation
  created by `orc run continue --allow-loop-cap` until the next matching
  `attempt.started` consumes it.
- Non-loop human-block continuation state is materialized in `continued` with
  mode `resolve_block` until the next `attempt.started` clears it. This marker
  records the human reason and resolved attempt fields needed to retry the same
  blocked step after process restart without losing workflow-loop accounting
  for the retry.
- Audited step skips are materialized in `skipped_steps`. Each entry is
  reconstructed from `workflow.step_skipped` and includes `step_id`, `status`,
  `result`, `reason`, `event_sequence`, `timestamp`, and optional `source`.
  Skips do not add entries to `attempts`; the paired workflow transition is
  visible through `workflow_loop.entries`.
- When `workflow.step_skipped` consumes the latest terminal attempt outcome,
  that existing attempt is materialized with `consumed_by_event` set to the
  skip event sequence.

The history entry below is abbreviated; entries use the same attempt object
shape as `active_attempt`.

```json
{
  "active_attempt": {
    "run_id": "20260502T143022Z-implementation-main-997-a1b2c3",
    "step_id": "plan",
    "agent_id": "planner",
    "attempt_id": "20260504T120000Z-plan-a1b2c3",
    "state": "active",
    "pid": 12345,
    "process_start_time": "123456789",
    "timeout": "30m0s",
    "report_exit_grace": "30s",
    "prompt_ref": {
      "kind": "prompt",
      "path": "prompts/000002-plan.md",
      "name": "plan",
      "event_sequence": 2
    },
    "log_ref": {
      "kind": "log",
      "path": "logs/000003-plan.log",
      "name": "plan",
      "event_sequence": 3
    },
    "started_at": "2026-05-04T12:00:00Z"
  },
  "attempts": [
    {
      "attempt_id": "20260504T120000Z-plan-a1b2c3",
      "state": "active",
      "prompt_ref": {
        "path": "prompts/000002-plan.md"
      },
      "log_ref": {
        "path": "logs/000003-plan.log"
      },
      "started_at": "2026-05-04T12:00:00Z"
    }
  ],
  "retry_lineage": {
    "step_id": "plan",
    "counts": {
      "failed/missing_report": 1
    }
  }
}
```

Attempt states currently materialized by the launcher are:

- `starting`
- `active`
- `missing_report`
- `process_error`
- `timed_out`

Report persistence also materializes:

- `reported`
- `invalid_report`

Retries do not replace terminal attempt states with a `superseded` state; see
the `attempt.started` contract in
[run-store-events.md](run-store-events.md#v1-event-types) for retry routing
metadata.

## Artifacts

Artifact references are relative to the run directory and must stay under it:

```json
{
  "kind": "report",
  "path": "reports/000004-plan.md",
  "name": "plan",
  "event_sequence": 4
}
```

`name` is optional metadata. Repeatable artifact filename slugs use the shared
slug normalization rules. Empty or unsluggable names fall back to the artifact
kind.

Artifact files follow the [filesystem safety](run-store-layout.md#filesystem-safety) rules.
Artifact paths are clean slash-separated relative paths with no parent segments.
Artifact parent directories must be real directories under the run directory,
not symlinks. Each artifact kind must use its documented path namespace and
extension.

Supported artifact kinds map to paths as follows:

| Kind | Path |
| --- | --- |
| `task_context` | `task/context.md` |
| `task_snapshot` | `task/snapshot.json` |
| `report` | `reports/<six-digit-sequence>-<name>.md` |
| `prompt` | `prompts/<six-digit-sequence>-<name>.md` |
| `log` | `logs/<six-digit-sequence>-<name>.log` |
| `snapshot` | `snapshots/<six-digit-sequence>-<name>.json` |
| `summary` | `summaries/<six-digit-sequence>-<name>.md` |
| `followup` | `followups.md` |

`task_context` and `task_snapshot` are singleton artifacts: each fixed path may
be written once per run. Repeatable artifacts use six-digit sequence-prefixed
filenames, such as `000004`.

For runs created through `orc run start`, the run-start layer owns
`task/context.md` and `task/snapshot.json` contents. The Run Store owns only
their paths, singleton behavior, and event references. See
[../features/run-start.md](../features/run-start.md#task-snapshot-schema) for
the task snapshot schema.

Final handoff summaries are repeatable `summary` artifacts. Their artifact
references in `status.json` are the durable record that human-review summaries
exist. State-guarded artifact writes check the run state while holding the run
lock; `record-summary` uses that to require `ready_for_human` at commit time.

VCS pre-run and post-run summaries are ordinary `snapshot` artifacts named
`vcs-pre-run` and `vcs-post-run`, for example
`snapshots/000004-vcs-pre-run.json`. The VCS inspector owns their JSON schema;
the Run Store owns only artifact path allocation and event references. See
[../features/run-start.md](../features/run-start.md#vcs-snapshot-schema) for
the snapshot fields.

`report` artifacts are usually written by `attempt.reported` when `orc report`
copies Markdown details, so the report attempt event owns both the terminal
attempt state and the report artifact reference.

The file is committed before the event append. Definite append failures roll the
file back, but a process or host crash between file commit and event append can
leave an unreferenced artifact file for later cleanup tooling. Retrying the same
report detail is tolerated when the expected report artifact path already exists
with identical content; different existing content remains an error.

`RecordAttemptReport` rejects caller-supplied `report_ref` values, so report refs
are added only when the store stages report content for that event.

`followup` appends new content by rewriting `followups.md`. Follow-up entries
are formatted by the typed Run Store follow-up API rather than by callers.

Report-sourced entries use this Markdown shape:

```md
## <title>

Source: report
Step: <step-id>
Agent: <agent-id>
Attempt: <attempt-id>
Recorded-At: <RFC3339 timestamp>

<details>
```

Orchestrator-sourced entries omit step, agent, and attempt metadata:

```md
## <title>

Source: orchestrator
Recorded-At: <RFC3339 timestamp>

<details>
```

The details block is omitted when no details are provided.

Orchestrator-sourced appends are recorded with the existing `artifact.written`
event for `kind=followup`. Report-sourced appends are staged and committed by
`RecordAttemptReport`; the resulting `attempt.reported` payload carries
`followup_refs` so the accepted report and its follow-up artifact share one
store-owned success boundary. V1 does not emit a separate `followup.recorded`
event.
