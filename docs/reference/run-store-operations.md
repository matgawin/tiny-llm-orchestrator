# Run Store Operations Reference

## Purpose

Provide the v1 operational rules, attempt lifecycle preconditions, and log
append API contract for durable run state under `.orc/runs`.

## Related Docs

- [run-store.md](run-store.md)
- [run-store-layout.md](run-store-layout.md)
- [run-store-events.md](run-store-events.md)
- [run-store-status-artifacts.md](run-store-status-artifacts.md)

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
Config snapshot writes also take the per-run lock. Snapshot version content is
written before `config/current.json`, so readers never observe a new current
version that points at partially written `resolved.json` or `manifest.json`
content.

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

`WriteInitialConfigSnapshot`:

- Order: write `config/000001/resolved.json`, write
  `config/000001/manifest.json`, then atomically replace
  `config/current.json`.
- The initial snapshot files are not run artifacts and do not append events.
- `current.json` is a regular file with `schema_version`, numeric `version`,
  and six-digit `version_dir`; symlinks are rejected by the same filesystem
  safety checks as other run-store paths.

`RefreshConfigSnapshot`:

- Order: validate the locked run state, write the next
  `config/<version>/resolved.json`, write `config/<version>/manifest.json`,
  atomically replace `config/current.json`, append
  `config_snapshot_refreshed`, then refresh `status.json`.
- The refresh event records old and new version pointers, the SHA-256 manifest
  hash, and the command source. The snapshot files are not run artifacts.
- Refresh is rejected while an active attempt exists. Compatibility validation
  is conservative: the workflow name must stay the same, the current workflow
  state and all past attempt step ids must still exist, and the selected or
  retryable pending step must keep valid report outcome routing. Adding steps,
  changing future routing that evaluates cleanly, and changing retry, loop,
  timeout, report-grace, agent, or runtime settings for future attempts is
  allowed. If safety cannot be proven, refresh fails instead of publishing a
  new current snapshot.
- Existing runs adopt live `.orc` edits only through
  `orc run refresh-config <run-id>`. There is no silent live reload and no
  `--force` override in v1.

Attempt lifecycle APIs follow the same status-backed write failure contract:
ambiguous append and status refresh failures return the candidate attempt/event
when the event may have committed, and callers should reload before retrying.
`RecordAttemptReport` with report content is the exception to pure
append-then-status ordering: it commits the report artifact before appending
`attempt.reported`, as described in [Artifacts](run-store-status-artifacts.md#artifacts).

### API Families

- `AppendEvent`, `UpdateStatus`, `WriteArtifact`, `StartAttempt`,
  `StartAttemptContext`, `RecordAttemptPrompt`, `RecordAttemptLog`,
  `RecordAttemptProcess`, `RecordAttemptProcessContext`, `FinishAttempt`,
  `RecoverAttempt`, `RecordAttemptReport`, `RecordAttemptWarning`, and
  `RecordIgnoredReport` take a run-level lock, append their event, then refresh
  `status.json`, except for the report-content commit-order case described in
  [Artifacts](run-store-status-artifacts.md#artifacts).
- `WriteInitialConfigSnapshot` takes the run-level lock and commits the current
  pointer after the version directory files are durable.
- `RefreshConfigSnapshot` takes the run-level lock while validating
  compatibility, committing the next version, updating `current.json`, and
  appending `config_snapshot_refreshed`.
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
- `attempt.started` lifecycle preconditions are replay-validated. Replay rejects
  starts that skip or mismatch a latest consumable outcome, or whose routing
  metadata does not match the `attempt.started` contract in
  [run-store-events.md](run-store-events.md#v1-event-types).

## Log Append API

- `OpenArtifactAppend` only opens recorded `log` artifacts and rejects
  symlinks, directories, and other non-regular files before returning an append
  handle.
- It only opens the current active attempt's linked log; terminal or unrelated
  attempt logs are immutable through this API.

Malformed or incomplete event state is not repaired during load. Load errors
name the broken file or artifact path.
