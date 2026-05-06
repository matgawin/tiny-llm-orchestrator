# Final Summary Recording

## Purpose

Define how `orc run record-summary` stores an orchestrator-authored final
ready-for-review summary.

## Audience

Contributors changing final ready-for-review summaries, run-store summary
artifacts, or terminal ready-for-review behavior.

## Read This When

- You are changing `orc run record-summary`.
- You need the command contract for final ready-for-review summaries.
- You are deciding whether an orchestrator handoff may mutate beads.

## Related Docs

- [summary-context.md](summary-context.md)
- [run-inspection.md](run-inspection.md)
- [follow-up-recording.md](follow-up-recording.md)
- [../reference/run-store.md](../reference/run-store.md)

## Command Shape

```bash
orc run record-summary <run-id> --file <path>
```

`--file` is required. The file is copied into the run directory through the Run
Store as a `summary` artifact under `summaries/`.

## State Rules

`record-summary` is accepted only when the persisted run state is
`ready_for_human`. Runs that are still `running`, `blocked_for_human`, or
`cancelled` are rejected with a clear message and no summary artifact is
recorded. Non-ready runs should use `orc run summary-context <run-id>` for
inspection instead of recording a final ready-for-review summary.

The ready-state check is made at the Run Store write boundary, so a summary is
not recorded if the run is no longer `ready_for_human` by the time the artifact
would be committed.

Recording a summary does not change the run state. `status.json` records the
summary artifact reference through the Run Store artifact list, and `orc run
status` shows it.

## Beads Boundary

V1 never mutates beads during final summary recording. The summary Markdown may
contain suggested bead notes or follow-up text for a human to apply manually,
but `record-summary` only copies the provided file into the Run Store.

## Summary Content

The orchestrator-authored final ready-for-review summary should preserve the
handoff content generated from summary context, including changes, tests, risks,
follow-ups, VCS summary, and review checklist notes.
