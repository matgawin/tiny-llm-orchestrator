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
selects the workflow start step, while terminal states such as
`ready_for_human` and `blocked_for_human` have no runnable step.

The worker launcher intentionally creates the starting attempt before rendering
the prompt. Prompt rendering still checks the selected step from run status and
caller-provided step metadata; it does not treat that newly starting attempt as
a reason to refuse rendering. The attempt transitions to active only after
process metadata is recorded.

Richer selected-step state based on persisted outcomes and retry lineage belongs
to later report and retry-routing slices. Active-attempt state is persisted by
the worker launcher.

An internal unselected-step option may render a declared non-selected step in a
running run for tests or a future debug caller. It does not override terminal
run states. Unselected-step rendering still validates that the requested step
exists and that the requested agent matches the workflow step.

## Prompt Content

Rendered prompts include:

- explicit attempt metadata
- the project-local role descriptor frontmatter fields and Markdown body
- captured task context from `task/context.md`
- prior report artifact paths with bounded Markdown excerpts when report
  artifacts exist
- the allowed `status/result` pairs for the selected step
- the exact provisional `orc report` command shape

Until structured report persistence exists, prior report context is bounded
Markdown excerpting from recorded report artifacts. If a recorded report
artifact cannot be read through the Run Store, rendering fails instead of
silently omitting recorded context. Later report persistence can replace those
excerpts with structured summaries without changing the renderer boundary.

## Report Contract

Worker prompts must tell workers to report through `orc report` and not write
directly into `.orc/runs`.

The current provisional report command shape is:

```bash
orc report --run <run-id> --step <step-id> --agent <agent-id> --attempt <attempt-id> --status <status> --result <result> --summary "<summary>"
```

`<status>` and `<result>` must be one of the selected step's allowed
`status/result` pairs from workflow config. The future report command will own
final validation and persistence for additional fields such as changed paths,
commands, tests, risks, follow-ups, and Markdown report details.

## Persistence

Prompt artifacts are written through the Run Store as `prompt` artifacts under:

```text
prompts/<six-digit-sequence>-<step-id>.md
```

The Run Store records prompt artifacts with the existing `artifact.written`
event and materializes the artifact reference into `status.json`.
