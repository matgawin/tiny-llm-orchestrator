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
- [../reference/configuration-workflows.md](../reference/configuration-workflows.md)
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
caller-provided step metadata. It finds the persisted attempt by its exact id
and checks its step and agent ids. It does not treat that newly starting attempt
as a reason to refuse rendering. The attempt transitions to active only after
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

Scaffolded role descriptors use one authority order. Human `Task Context`
defines the maximum scope. A planner can reduce this scope but cannot expand
it. A reviewer can request changes only for original requirements, preserved
behavior, or regressions introduced by the run. A repository skill controls
the work method only. It cannot change task scope, role boundaries, required
results, or stop conditions. Work outside these limits is a follow-up or a
blocker.

Repository instruction files remain mandatory. A matching repository skill
replaces matching method rules only. Planner prompts require `Required change`,
`Out of scope`, `Expected files`, `Required checks`, and `Stop after`.
`Expected files` is guidance and is not an allowlist. Coding prompts permit an
unexpected path only when correct requested behavior requires it. They require
one separate structured risk for each path:
`unexpected path <path>: <reason the requested behavior cannot be correct without this change>`.
Review prompts inspect the repository diff and compare each unexpected path
and reason with the human task, planner scope, and actual diff.

Loop context includes the workflow name, repeated state, current count, soft
cap, hard cap, prior statuses when recorded by workflow state-entry metadata,
and guidance to break the loop or escalate instead of repeating the same
outcome.

Prior report context selects accepted reports for the current route. It orders planner scope, the latest implementation report, the latest modifying report, fresh full-verification evidence, the routing report, and finding origins. It removes duplicate attempt ids. It does not include unrelated history or a history index.

A full-verification report is fresh when it uses the review attempt's config snapshot and follows the latest accepted report with non-empty `changed_paths`. Accurate `changed_paths` values control freshness. A review prompt uses fresh evidence without repeating the check. If evidence is absent, the prompt requires `verification.full_check.argv`. If both are absent, the reviewer reports `blocked/blocked`.

A correction prompt includes the routing report and its structured findings without truncation. It limits investigation to the failed input, changed lines, and direct definitions, callers, and dependencies.

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
Accepted valid reports always receive one canonical Markdown `report` artifact.
When `--report-file` or JSON `report_file` is supplied, that file's Markdown is
appended under `## Report Detail` after Orc-generated structured report
sections; structured-only reports still receive the same canonical artifact
without a detail section.

Live worker-authored progress is a separate prompt guidance surface from final
reports. Rendered prompts tell workers they may use `orc progress <short
update>` for crucial operator-visible updates, such as starting analysis,
choosing an approach, beginning tests, or finding a blocker. They also warn
workers not to stream logs, file lists, diffs, frequent heartbeat messages, or
routine chatter through live progress. Prompts continue to present
`orc report --status/--result` as the only final worker outcome submission
path.

Rendered prompts include an `Attempt Deadline` section. It names
`orc time-left` as the command workers can use to inspect deadline, elapsed
time, remaining time, timeout, phase, and action guidance during the attempt.
The section records `started_at`, `deadline`, `timeout`, `calculated_at`,
`initial_remaining`, `initial_phase`, and `initial_action`. The initial values
are launch-time values calculated when Orc renders the prompt. They are not
current values after launch. Orc resolves the action from the attempt's pinned
configuration snapshot.
The section keeps `orc report` as the only final completion or blockage path.
See [attempt-time-left.md](attempt-time-left.md) for phase thresholds and hook
behavior.

The prompt treats `ORC_PROGRESS_SOCKET`, `ORC_PROGRESS_TOKEN`, `ORC_RUN_ID`,
`ORC_STEP_ID`, `ORC_ATTEMPT_ID`, `ORC_PROJECT_ROOT`,
`ORC_ATTEMPT_STARTED_AT`, `ORC_ATTEMPT_DEADLINE`, and
`ORC_ATTEMPT_TIMEOUT` as injected troubleshooting details, not normal manual
arguments. The full live progress contract is defined in
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
