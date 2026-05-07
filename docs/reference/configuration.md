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

Project config may also declare an Orc-managed sandbox command contract:

```yaml
sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  cwd: "."
  bubblewrap:
    enabled: true
    network: true
    mounts:
      repo: rw
      beads: auto
      codex_home: rw
      tmp: rw
  env:
    pass: []
    set: {}
  mounts:
    - host: ".orc/cache"
      target: "/workspace/.orc/cache"
      mode: rw
      optional: true
```

The sandbox section configures `orc sandbox run`. This reference documents the
configuration shape and validation rules; see
[../features/sandbox-run.md](../features/sandbox-run.md) for the executable CLI
behavior and the canonical bubblewrap mount, environment, home, network, and
non-default policy. Bubblewrap sandbox execution is Linux-only for v1, although
the configuration schema can be loaded on any platform.

`sandbox.command.argv` is required whenever `sandbox` is present. It must be a
non-empty argv list with no empty entries. Shell-string command declarations
are rejected, and Orc does not default this field to Codex, yolo mode, or any
other command.

`sandbox.cwd` defaults to the repository root when omitted. When set, it is
interpreted relative to the repository root and must be an existing directory
that is not absolute, traversing outside the repository, or escaping through a
symlink.

`sandbox.bubblewrap.enabled` is reserved for bubblewrap policy selection; v1
`orc sandbox run` always shells out to `bwrap` and never treats this field as
permission to run unsandboxed. `sandbox.bubblewrap.network` accepts `true` or
`false` and defaults to `true`. Preset `sandbox.bubblewrap.mounts` validates
named mount policy declarations for the sandbox contract: `repo`, `codex_home`,
and `tmp` accept `ro` or `rw`; `beads` accepts `auto`, `ro`, or `rw`.

`sandbox.env.pass` is an optional list of environment variable names to pass
from the host when present. `sandbox.env.set` is an optional map of fixed
environment variable values; duplicate keys are allowed with pass-through names,
and the fixed value takes precedence.

Extra `sandbox.mounts` entries declare project-specific host mounts. `mode` must
be exactly `ro` or `rw`. `host` may be absolute or repository-relative.
Repository-relative writable host paths must resolve inside the repository.
`target` must be a clean absolute sandbox path that passes the protected-target
validation used by `orc sandbox run`. Missing required mounts are validation
errors; missing mounts with `optional: true` are skipped.

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

Workflow steps may be agent-backed or deterministic process steps. Omitted
`kind` is backward-compatible and means `agent`.

Agent steps declare:

- `agent`: an agent id present in `.orc/config.yaml`
- `allowed_results`: a non-empty map of allowed statuses to non-empty result lists
- `on`: a deterministic transition map keyed by `status/result`

Agent steps may also set `kind: agent`; they must not set `command` or
`script`.

Command steps declare argv-only process execution:

```yaml
steps:
  check:
    kind: command
    command:
      argv: ["task", "check"]
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
`command`; unsupported `kind` values are configuration errors.

Allowed result values must be non-empty strings. Every `on` key must be declared in `allowed_results`, and every declared `status/result` pair must have a deterministic transition to another step or a supported terminal state.

Worker-authored reports may use workflow-declared outcome pairs except reserved
system-owned failure results. `orc report` rejects `failed/error`,
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

## Environment Variables

The application has no general runtime environment-variable configuration
surface. Tooling-related environment variables such as `CODEX_BIN` belong to
the development shell and agent workflow rather than the `orc` app config
schema.

`orc run start --bead <id>` observes inherited `BEADS_DIR` as command source
metadata, not as a `.orc/config.yaml` schema field. See
[../features/run-start.md](../features/run-start.md) for run-start task-source
behavior.
