# Run Store Reference

## Purpose

Provide the v1 on-disk contract for durable run state under `.orc/runs`.

## Audience

Contributors who need exact file names, event fields, status fields, and artifact paths for run persistence.

## Read This When

- You are changing `internal/runstore`.
- You are wiring future CLI commands to persisted run state.
- You need to inspect a run directory by hand.

## Related Docs

- [configuration.md](configuration.md)
- [../architecture/system-overview.md](../architecture/system-overview.md)

## Slug Normalization

Generated slug segments are lowercase ASCII. Runs of non-letter/non-digit
characters collapse to dash separators, leading and trailing dashes are trimmed,
and each slug segment is capped at 48 characters. Callers may provide richer
display names separately where the API supports them.

## Run ID Contract

Generated run IDs use:

```text
<utc-timestamp>-<workflow-slug>-<task-slug>-<short-random>
```

Example:

```text
20260502T143022Z-implementation-main-997-a1b2c3
```

Workflow is required and must contain at least one ASCII letter or digit for
generated run IDs. Empty or unsluggable task slugs fall back to `task`. The
random suffix is six lowercase hexadecimal characters.

Explicit caller-provided run IDs are allowed for tests, debugging, and imports.

Explicit run IDs must be filesystem-safe names using only letters, digits, dot,
underscore, and dash. Path separators, empty IDs, `.`, and `..` are rejected.

Generated ID collisions are retried by generating a new suffix. Explicit ID collisions fail.

## Directory Layout

Project initialization owns the `.gitignore` rule that keeps `.orc/runs/`
ignored; `internal/runstore` owns the runtime files under that directory.

Each created run starts with this layout:

```text
.orc/runs/<run-id>/
  .lock
  events.jsonl
  status.json
  task/
  reports/
  prompts/
  logs/
  snapshots/
  summaries/
  followups.md
```

Task files are created when their artifacts are persisted:

```text
task/context.md
task/snapshot.json
```

Task artifact contents are caller-owned.

## Filesystem Safety

`.orc`, `.orc/runs`, and each run directory must be real directories, not
symlinks. Bootstrap files and artifact files must be regular files, not
directories, symlinks, devices, sockets, or FIFOs.

## Events

`events.jsonl` is append-only JSON Lines. Each line is one event, and every
line, including the final line, must end with `\n`; a missing trailing newline
is treated as incomplete state.
The file follows the [filesystem safety](#filesystem-safety) rules.

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
    "task_slug": "main-997"
  }
}
```

Ordering is defined by the monotonically increasing `sequence` field. Timestamps are metadata.

Latest-state changes are recorded as `status.updated` events and then materialized into `status.json`.

### Caller Events

Callers may append custom events with non-reserved event types. Reserved event
types are `run.created`, `status.updated`, `artifact.written`,
`attempt.started`, `attempt.prompted`, `attempt.logged`, `attempt.process_started`,
`attempt.finished`, `attempt.recovered`, `attempt.reported`, and
`report.ignored`; those are written only through the dedicated store APIs.

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
  "task_slug": "main-997"
}
```

`status.updated` is written by the status update API before the latest state is materialized into `status.json`.

```json
{
  "state": "ready_for_human"
}
```

`artifact.written` is written when the store persists a standalone artifact.
Markdown report details accepted with `orc report` are the exception: their
artifact reference is embedded in the `attempt.reported` payload instead of a
separate `artifact.written` event.

```json
{
  "artifact": "<artifact reference>"
}
```

The artifact reference shape is defined in [Artifacts](#artifacts).

`attempt.started` is written when a worker launch creates a `starting` attempt.
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
[Attempt Lifecycle Preconditions](#attempt-lifecycle-preconditions).

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

`attempt.reported` terminalizes the run's current `active_attempt` with a
structured worker report accepted through `orc report`. The attempt must already
be in attempt state `active`; `starting` attempts are not reportable.

```json
{
  "attempt_id": "20260504T120000Z-plan-a1b2c3",
  "state": "reported",
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
  "attempts": []
}
```

In v1, `state` is caller-validated. The store requires status updates and
`status.updated` event payloads to provide a non-empty state string, but
workflow/report layers own the allowed-state policy.

When a worker attempt is starting or active, `status.json` includes
`active_attempt` and an `attempts` history. Terminal attempts remain in
`attempts`; terminalizing an attempt removes `active_attempt`. The history
entry below is abbreviated; entries use the same attempt object shape as
`active_attempt`.

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
  ]
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

Later retry slices may add superseded states.

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

Artifact files follow the [filesystem safety](#filesystem-safety) rules.
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

`followup` appends new content by rewriting `followups.md` before the
`artifact.written` event is appended.

## V1 Operational Rules

### Locking

V1 serializes status-backed writes per run inside the Run Store. Higher-level
orchestration still owns domain coordination, such as deciding whether a
concurrent command should be allowed to mutate the same run.
Run creation also takes a runs-directory publication lock while reserving and
publishing the final run directory, so public readers do not observe a
half-published run layout.
Public read APIs acquire the same per-run lock before replaying state or
reading artifacts, so inspection and reload paths observe a stable committed
snapshot rather than an in-progress event append.

V1 writes `schema_version: 1` and does not implement schema migrations. The
only metadata backfill is `.lock` creation for legacy run directories that
predate per-run locking; public reads and writes may create that lock file
before accessing run state.

V1 treats persisted content as caller-owned. Redaction and size limits belong to
callers or future policy layers, not the run-store package.

### Commit Order and Failure Semantics

`AppendEvent`:

- Order: append event, then refresh `status.json`.
- Ambiguous append: returns the candidate event so callers can reload before
  retrying.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed event.

`UpdateStatus`:

- Order: append `status.updated`, then refresh `status.json`.
- Non-running state updates are rejected while an attempt is active.
- Ambiguous append: returns the candidate status and event so callers can
  reload before retrying.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed status and event.

`WriteArtifact`:

- Order: commit the artifact file, append `artifact.written`, then refresh
  `status.json`.
- Streaming log artifacts are the exception to final-content semantics:
  `artifact.written` reserves the log destination before process start, and the
  launcher opens the recorded artifact through the run store and appends
  stdout/stderr to that file while the worker runs.
- Ambiguous append: returns the candidate artifact reference and keeps the
  artifact because the event may be durable.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed artifact reference.

If a `WriteArtifact` event append definitely fails before a line can be
appended, the store attempts to roll back the artifact file. Retrying after a
successful event append or status materialization failure is not idempotent
unless the caller adds its own idempotency key in a future event contract.

Attempt lifecycle APIs follow the same status-backed write failure contract:
ambiguous append and status refresh failures return the candidate attempt/event
when the event may have committed, and callers should reload before retrying.
`RecordAttemptReport` with report content is the exception to pure
append-then-status ordering: it commits the report artifact before appending
`attempt.reported`, as described in the artifact section above.

### API Families

- `AppendEvent`, `UpdateStatus`, `WriteArtifact`, `StartAttempt`,
  `StartAttemptContext`, `RecordAttemptPrompt`, `RecordAttemptLog`,
  `RecordAttemptProcess`, `RecordAttemptProcessContext`, `FinishAttempt`,
  `RecoverAttempt`, `RecordAttemptReport`, and `RecordIgnoredReport` take a
  run-level lock, append their event, then refresh `status.json`, except for the
  report-content commit-order case described above.
- `Load`, `ReadArtifact`, and `OpenArtifactAppend` also take the run-level lock.
  For legacy runs that predate `.lock`, these APIs create the missing lock file
  as metadata-only backfill before replaying state, reading artifact content, or
  opening a recorded artifact for append.

## Attempt Lifecycle Preconditions

- `StartAttemptContext` returns the context error without appending an
  `attempt.started` event if cancellation wins before the attempt commits,
  including while waiting for the same-process run lock.
- `StartAttempt` only accepts runs whose latest state is `running`; replay
  rejects `attempt.started` events after a terminal or human-waiting state.
- `StartAttempt` rejects attempt ids already present in attempt history, even
  when the previous attempt is terminal.
- `FinishAttempt` and `RecoverAttempt` only accept terminal attempt states:
  `missing_report`, `process_error`, or `timed_out`.
- Terminal attempts must use the v1 launcher outcome tuple for their state:
  `failed/missing_report`, `failed/process_error`, or `failed/timeout`.
- `RecordAttemptReport` accepts report terminal states `reported` and
  `invalid_report` for the current `active` attempt. `invalid_report` must use
  `failed/invalid_report`. Callers cannot supply `report_ref`; report refs are
  assigned only when `RecordAttemptReport` stages report content.
- `missing_report` and `timed_out` terminal states require prior
  `attempt.process_started`; pre-process terminalization is limited to
  `process_error`.
- Retrying attempt writes is not idempotent unless the caller first reloads and
  observes the current active attempt or terminal attempt history.
- Replay rejects `attempt.started` while a pending launcher-synthesized outcome
  remains unconsumed.

## Log Append API

- `OpenArtifactAppend` only opens recorded `log` artifacts and rejects
  symlinks, directories, and other non-regular files before returning an append
  handle.
- It only opens the current active attempt's linked log; terminal or unrelated
  attempt logs are immutable through this API.

Malformed or incomplete event state is not repaired during load. Load errors
name the broken file or artifact path.
