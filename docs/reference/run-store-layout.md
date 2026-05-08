# Run Store Layout Reference

## Purpose

Provide the v1 on-disk contract for durable run state under `.orc/runs`.

## Audience

Contributors who need exact file names, event fields, status fields, and artifact paths for run persistence.

## Read This When

- You are changing `internal/runstore`.
- You are wiring future CLI commands to persisted run state.
- You need to inspect a run directory by hand.

## Related Docs

- [run-store.md](run-store.md)
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
