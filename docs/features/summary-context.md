# Summary Context

## Purpose

Define the read-only review context rendered by `orc run summary-context`.

## Audience

Contributors changing final handoff, run inspection, worker report summaries,
follow-up handling, or VCS summary rendering.

## Read This When

- You are changing `orc run summary-context`.
- You need the canonical v1 output structure for final review context.
- You are wiring later commands that record final ready-for-review summaries.

## Related Docs

- [run-inspection.md](run-inspection.md)
- [final-summary-recording.md](final-summary-recording.md)
- [follow-up-recording.md](follow-up-recording.md)
- [run-start.md](run-start.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)

## Command Shape

```bash
orc run summary-context <run-id>
```

Unknown run ids fail with a clear not-found error.

Use `summary-context` to gather the persisted inputs for a final handoff. Use
[`orc run record-summary`](final-summary-recording.md) to persist that handoff
for `ready_for_human` runs.

## Read-Only Behavior

`summary-context` renders persisted run-store state and artifacts together with
the current workflow config named by the run. It reads only the sources listed
in [State Sources](#state-sources) and is read-only with respect to run domain
state, run events, run artifacts, beads, worker launches, and VCS snapshots.
Like other Run Store readers, loading a legacy run may still create the
metadata-only `.lock` file before reading state.

## State Sources

The renderer reads:

- `status.json` and append-only events through the Run Store.
- `status.skipped_steps` for audited human skip decisions.
- `task/context.md` for task context.
- structured `status.attempts[].report` fields for worker summaries, changed
  paths, commands, tests, risks, and structured report follow-ups.
- `followups.md` for recorded report-sourced and orchestrator-sourced
  follow-ups.
- `vcs-pre-run` and `vcs-post-run` snapshot artifacts when already recorded;
  missing snapshots are reported as not recorded.
- the current `.orc` workflow config for workflow step declaration order and
  current decision evaluation. Step declaration order comes from the canonical
  loaded workflow config.

The renderer does not consume hidden process logs or live agent conversation.

## Output Format

The approved v1 section order is:

1. `Run`
2. `Task Context`
3. `Workflow Path`
4. `Worker Reports`
5. `Changes`
6. `Commands And Tests`
7. `Risks`
8. `Follow-Ups`
9. `VCS`
10. `Suggested Human Review Focus`

`Run` shows the run id, workflow, persisted state, effective state, terminal
state label, and last sequence.

`Task Context` renders a bounded Markdown excerpt from `task/context.md`.

`Workflow Path` renders workflow steps using the declaration order from State
Sources. Attempts are counted under their declared step. Audited skips are
rendered in this section with the exact wording
`step <step-id> skipped by human decision: <reason>`. The current workflow
decision and terminal reason are included when applicable.

`Worker Reports` renders reported attempts with these rules:

- Reports are grouped by workflow step declaration order.
- Attempts within the same step remain chronological according to persisted
  attempt order.
- Report identity fields such as step id, attempt id, agent id, status, and
  result are rendered as quoted fields under fixed headings rather than inside
  heading text.
- Worker-authored scalar fields are quoted so newlines or Markdown-looking
  content cannot inject fake headings or list items into the summary structure.
- String-valued fields are quoted by default; boolean and integer fields remain
  raw.

`Changes` keeps worker-reported paths and VCS-observed paths separate, then
prints a deduplicated combined rollup. VCS path sections include all latest
post-run changed paths, latest pre-existing changed paths that are still present
post-run, and newly observed paths computed as latest post-run minus latest
pre-run. The combined rollup uses worker-reported paths plus all post-run VCS
paths so pre-existing files that were legitimately edited during the run remain
visible.

`Commands And Tests` renders worker-reported commands and tests as separate
deduplicated lists.

`Risks` renders worker-reported risks and explicitly calls out
`blocked_for_human` when that is the effective terminal state.

`Follow-Ups` renders structured report follow-ups separately from recorded
`followups.md` content. V1 intentionally does not parse `followups.md` into
typed entries. A report-sourced follow-up may appear in both places: once as
the structured report field and once as the persisted follow-up artifact text.

`VCS` renders the latest matching snapshot summaries from State Sources. Other
snapshot artifacts are ignored even if their paths contain `vcs`.

`Suggested Human Review Focus` is deterministic guidance derived from terminal
state, recorded risks, tests, follow-ups, and VCS snapshot availability.

## Format Approval

This v1 format was approved for `main-9zx` on 2026-05-06 and is locked by
run-inspection test coverage. Future format changes should update this doc and
the golden structure together.
