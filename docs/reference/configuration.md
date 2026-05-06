# Configuration Reference

## Purpose

Provide a code-backed reference for the repository's configuration files and config surfaces.

## Audience

Contributors and maintainers changing config loading, validation, scaffold source, or init behavior.

## Read This When

- You need to know which `.orc` files are consumed by the app.
- You are updating config schema validation.
- You are updating scaffold source or generated init output.

## Related Docs

- [../getting-started/local-development.md](../getting-started/local-development.md)
- [../operations/runtime-stack.md](../operations/runtime-stack.md)

## Config Files And Loaders

The current config loader is `internal/config.Load(projectRoot)`.

It reads:

- `.orc/config.yaml`
- workflow files referenced by `workflows` entries
- agent descriptor files referenced by `agents` entries

The canonical scaffold source for the current v1 shape is
`internal/initconfig/scaffold/.orc`.

## Init Scaffolding

`orc init` scaffolds the v1 `.orc` configuration shape into the current working
directory:

- `orc init --dry-run` previews planned files without writing.
- `orc init --yes` creates missing scaffold files noninteractively.
- Interactive `orc init` prompts before overwriting differing scaffold files and
  before creating a missing `.gitignore`.
- Instruction files: interactive init prompts before creating or updating
  `AGENTS.md`; `--yes` skips `AGENTS.md` creation or update; v1 only supports
  `AGENTS.md`.
- `orc init` creates and ignores `.orc/runs/`.
- Persistent files under `.orc/` are user-owned and reviewable; runtime run
  state belongs under the ignored `.orc/runs/` directory; see
  [run-store.md](run-store.md) for the durable file contract.
- If `.gitignore` broadly ignores `.orc`, `orc init` fails and asks you to
  replace that broad rule with `.orc/runs/` so persistent config remains
  trackable.

The scaffold includes these workflows:

- `implementation`: plan, code, test, and review a general change.
- `bugfix`: reproduce the bug before planning, coding, testing, and review.
- `mechanical-change`: plan, apply low-judgment mechanical edits, run focused
  verification, and complete mechanical review.
- `test-only`: plan, design tests, edit tests, run tests, and review without
  intentional production behavior changes.
- `review-mechanical`: review a change for stale references, generated drift,
  config mismatch, and mechanical completeness.
- `review-readability`: review changed code or docs for clarity and
  maintainability.
- `review-redundancy`: review for duplicated logic, duplicated docs, unused
  scaffold, and unnecessary surface area.
- `review-docs`: review durable docs, indexes, examples, and links for
  contract accuracy.

Implementation, bugfix, mechanical-change, and test-only workflows block dirty
starts by default so unrelated pre-existing changes do not mix with new work.
Review-only workflows allow dirty starts by default because their normal input
is often the existing working-copy diff being reviewed.

The scaffold includes detailed descriptors for these agents:

- `planner`
- `coder`
- `mechanical-coder`
- `bug-reproducer`
- `tester`
- `test-designer`
- `reviewer`
- `mechanical-reviewer`
- `readability-reviewer`
- `redundancy-reviewer`
- `docs-reviewer`

Each scaffold descriptor is written for the full rendered worker prompt, not as
a standalone instruction. Descriptors explicitly tell workers how to use
`Attempt Metadata`, `Task Context`, `Prior Report Context`, and `Report
Contract`, which are injected by the prompt renderer at worker launch time.
They also allow workers to use available repo-local skills and bounded
subagents when the active worker runtime exposes those capabilities.

## `.orc/config.yaml`

Required fields:

- `version`: currently `1`
- `workflows`: map of workflow name to either a legacy `.orc`-relative
  workflow file path scalar or an object with `path` and optional `loop_caps`
- `agents`: map of agent id to `.orc`-relative descriptor file path

The `workflows` and `agents` maps must each contain at least one entry.
Referenced paths must be relative to `.orc`; absolute paths, traversal outside `.orc`, and symlink escapes are rejected.

Project config also supports workflow loop cap defaults:

```yaml
defaults:
  loop_caps:
    enabled: true
    soft: 2
    hard: 4
```

`defaults.loop_caps` may be omitted for older configs. Missing loop cap config
resolves to the built-in default `enabled: true`, `soft: 2`, and `hard: 4`.
New scaffolded configs include those values explicitly.

Workflow-level loop cap overrides use the expanded workflow object form:

```yaml
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      hard: 6
```

Workflow overrides merge with `defaults.loop_caps`, so partial overrides inherit
omitted fields. `enabled: false` is the only supported disable signal. When the
effective value is disabled, `soft` and `hard` may be omitted and are ignored if
present. When the effective value is enabled, `soft` and `hard` must resolve to
positive integers, and `hard` must be greater than `soft`. Negative caps are
always invalid; zero caps are invalid when the effective value is enabled. Loop
caps apply only to workflow routing loops. They do not change agent execution
retry caps, report validation retries, or the `defaults.retries` workflow
outcome retry policy.

## Workflow Files

Workflow files define:

- `name`
- `start`
- `execution.mode`, currently `sequential`
- `task_context.beads`, one of `disabled`, `optional`, or `required`
- `task_context.markdown_fallback`
- `vcs.dirty_start`, optional, one of `block` or `allow`
- `vcs.no_vcs`, optional, one of `allow` or `block`
- `defaults.timeout`
- `defaults.report_exit_grace`
- `defaults.retries`
- `steps`

Validation rules:

- `name` and `start` are required.
- `execution.mode` must be `sequential`.
- `steps` must contain at least one step.
- `start` must name a declared step.
- Omitted `vcs.dirty_start` defaults to `block`.
- Omitted `vcs.no_vcs` defaults to `allow`.
- `defaults.timeout` and `defaults.report_exit_grace` are required Go duration strings and must be greater than zero.
- `defaults.retries` is required.
- Retry counts must be zero or greater.
- Retry keys must match `status/result` pairs declared by the workflow's steps.

`vcs` is workflow-level policy, separate from task context and step defaults.
`dirty_start: block` rejects dirty working copies before a run directory is
created. `dirty_start: allow` records the dirty pre-run snapshot and lets the
run start. `no_vcs: allow` permits project contexts where neither `jj` nor
`git` is detected; `no_vcs: block` rejects them.

Workflow-declared outcome statuses are:

- `done`
- `blocked`
- `failed`

`allowed_results` defines every outcome pair the workflow engine may route,
including system-owned synthesized outcomes. It is broader than the set of
outcomes a worker may author with `orc report`.

Each step must declare:

- `agent`: an agent id present in `.orc/config.yaml`
- `allowed_results`: a non-empty map of allowed statuses to non-empty result lists
- `on`: a deterministic transition map keyed by `status/result`

Allowed result values must be non-empty strings. Every `on` key must be declared in `allowed_results`, and every declared `status/result` pair must have a deterministic transition to another step or a supported terminal state.

Worker-authored reports may use workflow-declared outcome pairs except reserved
system-owned failure results. `orc report` rejects `failed/error`,
`failed/invalid_report`, `failed/missing_report`, `failed/timeout`, and
`failed/process_error`; those are written only by report validation, run-store,
or launcher paths.

Supported terminal states:

- `ready_for_human`
- `blocked_for_human`
- `cancelled`

## Agent Descriptors

Agent descriptors are Markdown files with YAML frontmatter:

- `id`
- `role`
- `description`

The descriptor must start with YAML frontmatter delimited by `---` lines, and it must include a non-empty Markdown body. The `id`, `role`, and `description` values are trimmed and required.

The descriptor id must match the key used in `.orc/config.yaml`.

## Environment Variables

The application has no general runtime environment-variable configuration
surface. Tooling-related environment variables such as `CODEX_BIN` belong to
the development shell and agent workflow rather than the `orc` app config
schema.

`orc run start --bead <id>` observes inherited `BEADS_DIR` as command source
metadata, not as a `.orc/config.yaml` schema field. See
[../features/run-start.md](../features/run-start.md) for run-start task-source
behavior.
