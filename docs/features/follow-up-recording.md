# Follow-Up Recording

## Purpose

Define how Tiny Orc records substantial out-of-scope work without expanding the
current run.

## Audience

Contributors changing `orc report`, `orc run add-followup`, run-store
follow-up artifacts, or summary-context inputs.

## Behavior

Follow-ups are appended to `.orc/runs/<run-id>/followups.md` through the Run
Store. V1 records local artifacts only; it does not create, update, close, or
comment on beads automatically.

Follow-ups can be recorded from two sources:

- valid worker reports with structured follow-up suggestions
- orchestrator commands through `orc run add-followup <run-id> --title <title>
  [--details <markdown>]`

Each follow-up requires a non-blank title. Details are optional. The `orc
report --follow-up <title>` flag form records title-only suggestions; workers
that need details should use `orc report --json-file`, whose `followups` array
supports `title` and `details`.

Invalid reports and ignored stale or wrong-target reports do not append
`followups.md`.

## Markdown Format

Report-sourced entries use:

```md
## <title>

Source: report
Step: <step-id>
Agent: <agent-id>
Attempt: <attempt-id>
Recorded-At: <RFC3339 timestamp>

<details>
```

Orchestrator-sourced entries use:

```md
## <title>

Source: orchestrator
Recorded-At: <RFC3339 timestamp>

<details>
```

The details block is omitted when no details are provided. The file is a
persisted, inspectable input for later `orc run summary-context` rendering.

## Event Contract

V1 does not emit a dedicated `followup.recorded` event.

Orchestrator-sourced appends use the existing Run Store `artifact.written`
event with `kind=followup` and `path=followups.md`.

Report-sourced appends are committed by the store-owned `attempt.reported`
operation. The `attempt.reported` event carries `followup_refs`, so the valid
report and its follow-up artifact append share one success boundary.
