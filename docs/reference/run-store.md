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
types are `run.created`, `status.updated`, and `artifact.written`; those are
written only through the dedicated store APIs.

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

`run.created` is written once when a run directory is created.

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

`artifact.written` is written when the store persists an artifact.

```json
{
  "artifact": "<artifact reference>"
}
```

The artifact reference shape is defined in [Artifacts](#artifacts).

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
  "artifacts": []
}
```

In v1, `state` is caller-validated. The store requires status updates and
`status.updated` event payloads to provide a non-empty state string, but
workflow/report layers own the allowed-state policy.

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

`followup` appends new content by rewriting `followups.md` before the
`artifact.written` event is appended.

## V1 Operational Rules

V1 is single-writer. Commands that introduce concurrent writers must add an
explicit locking contract before relying on simultaneous writes.

V1 writes `schema_version: 1` but does not implement migrations.

V1 treats persisted content as caller-owned. Redaction and size limits belong to
callers or future policy layers, not the run-store package.

Write failure semantics:

`AppendEvent`:

- Order: append event, then refresh `status.json`.
- Ambiguous append: returns the candidate event so callers can reload before
  retrying.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed event.

`UpdateStatus`:

- Order: append `status.updated`, then refresh `status.json`.
- Ambiguous append: returns the candidate status and event so callers can
  reload before retrying.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed status and event.

`WriteArtifact`:

- Order: commit the artifact file, append `artifact.written`, then refresh
  `status.json`.
- Ambiguous append: returns the candidate artifact reference and keeps the
  artifact because the event may be durable.
- Status refresh failure: returns `StatusMaterializationError` with the
  committed artifact reference.

If a `WriteArtifact` event append definitely fails before a line can be
appended, the store attempts to roll back the artifact file. Retrying after a
successful event append or status materialization failure is not idempotent
unless the caller adds its own idempotency key in a future event contract.

Malformed or incomplete event state is not repaired during load. Load errors
name the broken file or artifact path.
