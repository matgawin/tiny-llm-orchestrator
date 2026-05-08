# Workflow Configuration Reference

## Purpose

Document workflow file and agent descriptor contracts loaded from `.orc/config.yaml`.

## Audience

Contributors and maintainers changing workflow validation, worker routing contracts, process steps, report outcomes, or agent descriptor parsing.

## Read This When

- You are updating workflow file schema validation.
- You need step, result, retry, skip, or terminal-state contracts.
- You are changing agent descriptor loading or validation.

## Related Docs

- [configuration.md](configuration.md)
- [configuration-project.md](configuration-project.md)
- [workflow-engine.md](workflow-engine.md)
- [../features/worker-launching.md](../features/worker-launching.md)
- [../features/worker-prompt-rendering.md](../features/worker-prompt-rendering.md)

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
- `defaults.runtime`, required when agent steps omit `runtime`
- `defaults.model`, optional
- `defaults.runtime_dirs`, optional
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
- Agent step runtime resolution is `steps.<id>.runtime`, then
  `defaults.runtime`; missing effective runtime is invalid.
- Agent step model resolution is `steps.<id>.model`, then `defaults.model`,
  then the selected runtime descriptor's `model.default`.
- Runtime directory resolution appends `defaults.runtime_dirs` before
  `steps.<id>.runtime_dirs` and preserves configured order.

Runtime defaults are workflow-level defaults for agent steps:

```yaml
defaults:
  timeout: 30m
  report_exit_grace: 30s
  runtime: codex
  model: gpt-5.3-codex
  runtime_dirs:
    - shared-worktree
  retries:
    failed/missing_report: 1
```

Agent steps may override the effective runtime, model, and directory args:

```yaml
steps:
  code:
    agent: coder
    runtime: custom-runtime
    model: custom-model
    runtime_dirs:
      - /home/matt/Documents/other-repo
```

The effective runtime is `steps.<id>.runtime`, then `defaults.runtime`; there
is no runtime descriptor default. The effective model is `steps.<id>.model`,
then `defaults.model`, then the selected runtime descriptor's `model.default`.
The effective runtime directories are all `defaults.runtime_dirs` followed by
all `steps.<id>.runtime_dirs`, preserving order and exact duplicates.

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

Workflow steps may be agent-backed or deterministic process steps. Omitted
`kind` is backward-compatible and means `agent`.

Agent steps declare:

- `agent`: an agent id present in `.orc/config.yaml`
- `runtime`: optional runtime id present in `.orc/config.yaml`
- `model`: optional model value passed only when the selected runtime supports models
- `runtime_dirs`: optional clean repo-relative paths or absolute host paths
- `skippable`: optional explicit opt-in for system-owned skip routing
- `allowed_results`: a non-empty map of allowed statuses to non-empty result lists
- `on`: a deterministic transition map keyed by `status/result`

Agent steps may also set `kind: agent`; they must not set `command` or
`script`.

Validation uses the selected runtime descriptor. A selected runtime must exist.
Model values are rejected when `model.supported` is false, required when
`model.required` is true and no effective model resolves, and checked against
non-empty `model.allowed` lists. `runtime_dirs` require
`directories.supported: true`. Runtime directory entries are argv values only:
Orc rejects empty entries, unclean relative paths, traversal outside the repo,
and shell, environment, or tilde expansion syntax.

Command and script steps are deterministic local process steps. They do not use
agent descriptors or runtime descriptors, and validation rejects `runtime`,
`model`, and `runtime_dirs` on those step kinds.

Command steps declare argv-only process execution:

```yaml
steps:
  check:
    kind: command
    command:
      argv: ["timeout", "--kill-after=10s", "5m", "task", "check"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: ready_for_human
      done/failed: code
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
```

`command.argv` must contain at least one non-empty argument. Shell-string
commands are not supported in v1; argv entries are passed directly to process
execution without shell parsing, expansion, or interpolation.

The bundled implementation, bugfix, mechanical-change, test-only, and
review-fix workflows run `task check` through GNU `timeout` with a 5-minute
wall-clock limit and a 10-second forced-kill grace. This keeps hung package
tests from occupying a run until the broader workflow attempt timeout expires.

The bundled `docs-update` workflow is intentionally lighter: it edits durable
docs and routes directly to docs review. It does not run a command verification
step by default, so use it only when the task is documentation-only and does not
need code, generated artifact, or scaffold validation.

Script steps declare a repository-relative executable path plus optional args:

```yaml
steps:
  verify-loop-counters:
    kind: script
    script:
      path: scripts/verify-loop-counters.sh
      args: ["--strict"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: ready_for_human
      done/failed: blocked_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
```

Script paths must be clean repository-relative paths. Absolute paths,
traversal outside the repository, and symlink escapes are rejected. Inline
script bodies are not supported in v1. Command and script steps may set
repo-relative `cwd`, which defaults to the repository root, and `env` entries
that override inherited environment values.

Kind-specific validation rejects mixed definitions: agent steps require
`agent`; command steps require `command.argv` and must not set `agent` or
`script`; script steps require `script.path` and must not set `agent` or
`command`; command and script steps must not set `runtime`, `model`, or
`runtime_dirs`; unsupported `kind` values are configuration errors.

Allowed result values must be non-empty strings. Every `on` key must be declared in `allowed_results`, and every declared `status/result` pair must have a deterministic transition to another step or a supported terminal state.

`skippable: true` is the per-step workflow contract for the system-owned skip
outcome `done/skipped`. Omitted or false means the step is not skippable. A
skippable step must declare both `allowed_results.done` containing `skipped`
and an explicit `on.done/skipped` transition:

```yaml
steps:
  review:
    agent: reviewer
    skippable: true
    allowed_results:
      done: [approved, changes_requested, skipped]
    on:
      done/approved: ready_for_human
      done/changes_requested: code
      done/skipped: ready_for_human
```

Configuration validation rejects `skippable: true` unless both declarations are
present. It also rejects `done/skipped` in either `allowed_results` or `on` for
non-skippable steps. This keeps skip routing discoverable in workflow config
and prevents workers from accidentally authoring the reserved skip outcome.

Reviewer-requested remediation uses the same contract. For example, the
implementation workflow lets a human bypass a selected remediation step and
continue to the next review lane:

```yaml
steps:
  code_fixer:
    agent: coder
    skippable: true
    allowed_results:
      done: [ready, skipped]
    on:
      done/ready: test-redundancy
      done/skipped: readability-review
```

Worker-authored reports may use workflow-declared outcome pairs except reserved
system-owned results. `orc report` rejects `done/skipped`, `failed/error`,
`failed/invalid_report`, `failed/missing_report`, `failed/timeout`, and
`failed/process_error`; those are written only by report validation, run-store,
or launcher paths. Command and script steps do not call `orc report`; Orc writes
their system reports with fixed v1 outcomes: exit code 0 becomes `done/passed`,
nonzero exit becomes `done/failed`, timeout becomes `failed/timeout`, and
spawn or process setup failure becomes `failed/process_error`.

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
