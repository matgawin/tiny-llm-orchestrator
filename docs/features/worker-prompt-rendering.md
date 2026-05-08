# Worker Prompt Rendering

## Purpose

Define how Tiny Orc renders role-specific worker prompts before a worker
process is launched.

## Audience

Contributors changing prompt content, worker launch inputs, report contracts,
or run-store prompt artifacts.

## Read This When

- You are changing worker prompt rendering.
- You are wiring `orc worker launch-next` to rendered prompts.
- You need to know what report contract a worker prompt must include.

## Related Docs

- [live-worker-progress.md](live-worker-progress.md)
- [run-start.md](run-start.md)
- [run-inspection.md](run-inspection.md)
- [../reference/configuration.md](../reference/configuration.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../reference/workflow-engine.md](../reference/workflow-engine.md)

## Renderer Boundary

Prompt rendering is an internal reusable runtime API. The renderer does not
expose a public debug CLI command and does not launch Codex.

The worker launcher owns active attempt creation and passes explicit metadata
into the renderer:

- run id
- step id
- agent id
- attempt id

The attempt id is an opaque non-empty string. The renderer does not create,
parse, or sequence attempt ids.

## Selected-Step Enforcement

By default, prompt rendering only accepts the currently selected runnable step.
The current implementation computes the selected step by evaluating the
workflow from persisted run status. That means a newly started `running` run
selects the workflow start step, retry-routed runs select the `retry_step`, and
terminal states such as `ready_for_human` and `blocked_for_human` have no
runnable step.

The worker launcher intentionally creates the starting attempt before rendering
the prompt. Prompt rendering still checks the selected step from run status and
caller-provided step metadata; it does not treat that newly starting attempt as
	a reason to refuse rendering. The attempt transitions to active only after
	process metadata is recorded.

An internal unselected-step option may render a declared non-selected step in a
running run for tests or a future debug caller. It does not override terminal
run states. Unselected-step rendering still validates that the requested step
exists and that the requested agent matches the workflow step.

## Prompt Content

Rendered prompts include:

- explicit attempt metadata
- the project-local role descriptor frontmatter fields and Markdown body
- captured task context from `task/context.md`
- workflow loop context after the selected state has passed its soft cap
- prior report context
- the worker-reportable `status/result` pairs for the selected step
- the exact required `orc report` command shape

Loop context includes the workflow name, repeated state, current count, soft
cap, hard cap, prior statuses when recorded by workflow state-entry metadata,
and guidance to break the loop or escalate instead of repeating the same
outcome.

Prior report context includes structured reports persisted on completed
attempts, so loopback prompts for coder steps include tester failure summaries
and reviewer requested-change summaries even when the worker did not attach a
separate report file. When report artifacts exist, the renderer also includes
bounded Markdown excerpts. If a recorded report artifact cannot be read through
the Run Store, rendering fails instead of silently omitting recorded context.

## Report Contract

Worker prompts must tell workers to report through `orc report` and not write
directly into `.orc/runs`.

The required report command shape is:

```bash
orc report --run <run-id> --step <step-id> --agent <agent-id> --attempt <attempt-id> --status <status> --result <result> --summary "<summary>"
```

`<status>` and `<result>` must be one of the selected step's worker-reportable
`status/result` pairs from workflow config. Reserved system-owned outcomes such
as `done/skipped`, `failed/invalid_report`, `failed/missing_report`,
`failed/timeout`, `failed/process_error`, and `failed/error` are not shown in
the prompt because workers cannot submit them through `orc report`.

Rendered prompts also list optional structured report fields: repeatable
`--changed-path`, `--command`, `--test`, `--risk`, and `--follow-up` flags,
`--report-file <path>` for Markdown details, and the alternative `orc report
--json-file <path>` form for richer structured reports. The prompt tells workers
not to combine `--json-file` with report field flags. The command validates
required identity fields against the current `active_attempt` in attempt state
`active` before persisting the structured report through the Run Store.

Live worker-authored progress is a separate prompt guidance surface from final
reports. Rendered prompts tell workers they may use `orc progress <short
update>` for crucial operator-visible updates, such as starting analysis,
choosing an approach, beginning tests, or finding a blocker. They also warn
workers not to stream logs, file lists, diffs, frequent heartbeat messages, or
routine chatter through live progress. Prompts continue to present
`orc report --status/--result` as the only final worker outcome submission
path.

The prompt treats `ORC_PROGRESS_SOCKET`, `ORC_PROGRESS_TOKEN`, `ORC_RUN_ID`,
`ORC_STEP_ID`, and `ORC_ATTEMPT_ID` as injected troubleshooting details, not
normal manual arguments. The full live progress contract is defined in
[live-worker-progress.md](live-worker-progress.md).

## Persistence

Prompt artifacts are written through the Run Store as `prompt` artifacts under:

```text
prompts/<six-digit-sequence>-<step-id>.md
```

The Run Store records prompt artifacts with the existing `artifact.written`
event and materializes the artifact reference into `status.json`. See
[run-store-status-artifacts.md](../reference/run-store-status-artifacts.md#artifacts)
for the artifact path contract.
