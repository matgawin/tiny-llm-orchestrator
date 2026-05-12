# Run Start

## Purpose

Define how `orc run start` creates a durable run from explicit task context.

## Audience

Contributors changing run creation, task-context capture, or Beads integration.

## Read This When

- You are changing `orc run start`.
- You need to know how bead and Markdown task context are captured.
- You are wiring later commands that consume `task/context.md` or `task/snapshot.json`.

## Related Docs

- [../reference/run-store.md](../reference/run-store.md)
- [../reference/configuration-workflows.md](../reference/configuration-workflows.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)

## Command Shape

`orc run start` requires a configured workflow and exactly one primary task
source. `--fallback-task-file` is an optional secondary source that can only
accompany `--bead`:

```bash
orc run start --workflow implementation --bead <id>
orc run start --workflow implementation --bead <id> --fallback-task-file <path>
orc run start --workflow implementation --task-file <path>
orc run start --workflow implementation --task "..."
orc run start --workflow implementation --task-stdin
```

Plain noninteractive `orc run start --workflow implementation` fails instead
of opening an editor or prompt. Interactive prompting is reserved for a later
slice.

`--fallback-task-file` is valid only with `--bead`. It is used only when the
explicit bead lookup fails.

## Task Context Policy

Run start loads `.orc/config.yaml`, verifies the requested workflow exists, and
enforces the workflow's `task_context` policy before creating a run:

- `beads: disabled` rejects `--bead`.
- `beads: required` requires `--bead`; fallback is still allowed after a
  failed explicit bead lookup when `markdown_fallback: true`.
- `beads: optional` allows bead or Markdown sources.
- `markdown_fallback: false` rejects `--task-file`, `--task`, `--task-stdin`,
  and `--fallback-task-file`.

## VCS Policy

After task context is resolved and before any run directory is created, run
start inspects the project working copy with read-only VCS commands and applies
workflow `vcs` policy. Detection prefers `jj`, then `git`, then records that no
supported VCS was detected. See
[../reference/configuration-workflows.md](../reference/configuration-workflows.md) for option
defaults and allowed values.

VCS inspection never reverts, resets, checks out, stages, commits, or otherwise
mutates repository state. The read-only command sequence is:

- `jj root`, then `jj status` when a jj repository is detected.
- `git rev-parse --show-toplevel`, then
  `git status --porcelain=v1 -z --untracked-files=all` when jj is not detected
  and git is detected.

## Bead Context

Bead integration is read-only in v1. The command reads explicit bead context
through:

```bash
bd show <id> --json
```

The inherited environment is used for lookup, including `BEADS_DIR` when
present. The command records the observed `BEADS_DIR` value in
`task/snapshot.json` source metadata when present.

If explicit bead lookup fails without fallback, run start fails before creating
a run directory. If fallback is provided, `task/snapshot.json` records both the
bead lookup failure and fallback source.

V1 never writes bead notes, updates beads, creates beads, or closes beads.

## Persisted Task Artifacts

Each successful start first loads and validates the live project `.orc`
configuration, then creates a run through the Run Store and writes config
snapshot version `000001`:

```text
config/current.json
config/000001/resolved.json
config/000001/manifest.json
```

`current.json` is a regular JSON file, not a symlink. `resolved.json` is the
canonical fully resolved runtime contract that later run-bound commands load
for this run. `manifest.json` records audit metadata for the snapshot, including
the `run_start` reason, workflow name, source file list, and SHA-256 content
hashes for `.orc/config.yaml`, loaded workflows, loaded agent descriptors, and
loaded runtime descriptors.

Run start then writes:

- `task/context.md`: the captured task context used by later prompt and summary
  commands.
- `task/snapshot.json`: source metadata, bead lookup result, and fallback
  metadata for the captured context.

Run start initializes durable run state only. It does not launch a worker or
evaluate the next workflow action.

Successful starts also write `snapshots/<sequence>-vcs-pre-run.json`, a
run-store snapshot artifact with the pre-run VCS summary. The VCS inspector also
provides the internal API for recording
`snapshots/<sequence>-vcs-post-run.json`; later summary-context work decides
when that API is called.

## Task Snapshot Schema

`task/snapshot.json` is run-start-owned JSON:

```json
{
  "schema_version": 1,
  "source": {
    "type": "bead",
    "bead_id": "main-too",
    "command": ["bd", "show", "main-too", "--json"],
    "env": {
      "BEADS_DIR": "/path/to/.beads"
    }
  },
  "bead_lookup": {
    "attempted": true,
    "ok": true,
    "bead_id": "main-too",
    "command": ["bd", "show", "main-too", "--json"]
  },
  "fallback": {
    "used": false
  }
}
```

`source.type` values are:

- `bead`
- `task_file`
- `inline_task`
- `stdin_task`
- `fallback_task_file`

Omitted empty fields are allowed. For Markdown sources, `source.path` is set
when the source is a file. For bead sources, `source.command` and
`bead_lookup.command` record the read-only lookup command. `source.env` records
only observed source metadata needed for replay or audit, currently `BEADS_DIR`
when present.

For fallback runs, `source.type` is `fallback_task_file` because the fallback
file is the task context actually used. `fallback.source_type` is `task_file`
because it identifies the fallback source category that replaced the failed
bead lookup:

```json
{
  "source": {
    "type": "fallback_task_file",
    "path": "tasks/fallback.md",
    "env": {
      "BEADS_DIR": "/path/to/.beads"
    }
  },
  "bead_lookup": {
    "attempted": true,
    "ok": false,
    "bead_id": "main-too",
    "command": ["bd", "show", "main-too", "--json"],
    "error": "bead lookup failed"
  },
  "fallback": {
    "used": true,
    "source_type": "task_file",
    "path": "tasks/fallback.md"
  }
}
```

## VCS Snapshot Schema

The VCS inspector owns the JSON schema for VCS snapshot artifacts:

```json
{
  "schema_version": 1,
  "phase": "pre_run",
  "kind": "jj",
  "dirty": false,
  "summary": "The working copy has no changes.",
  "changed_paths": [],
  "commands": [["jj", "root"], ["jj", "status"]]
}
```

`phase` is `pre_run`, `post_run`, or `config_refresh`. `kind` is `jj`, `git`,
or `none`.
`changed_paths` are deterministic observations for summary context; they are
not enforcement rules and are never used to modify the working copy. `error` is
optional and reserved for future persisted degraded-inspection summaries; run
start currently fails rather than persisting broken VCS probe errors.
