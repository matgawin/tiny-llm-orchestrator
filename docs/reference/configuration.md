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

## `.orc/config.yaml`

Required fields:

- `version`: currently `1`
- `workflows`: map of workflow name to `.orc`-relative workflow file path
- `agents`: map of agent id to `.orc`-relative descriptor file path

The `workflows` and `agents` maps must each contain at least one entry.
Referenced paths must be relative to `.orc`; absolute paths, traversal outside `.orc`, and symlink escapes are rejected.

## Workflow Files

Workflow files define:

- `name`
- `start`
- `execution.mode`, currently `sequential`
- `task_context.beads`, one of `disabled`, `optional`, or `required`
- `task_context.markdown_fallback`
- `defaults.timeout`
- `defaults.report_exit_grace`
- `defaults.retries`
- `steps`

Validation rules:

- `name` and `start` are required.
- `execution.mode` must be `sequential`.
- `steps` must contain at least one step.
- `start` must name a declared step.
- `defaults.timeout` and `defaults.report_exit_grace` are required Go duration strings and must be greater than zero.
- `defaults.retries` is required.
- Retry counts must be zero or greater.
- Retry keys must match `status/result` pairs declared by the workflow's steps.

Allowed worker report statuses are:

- `done`
- `blocked`
- `failed`

Each step must declare:

- `agent`: an agent id present in `.orc/config.yaml`
- `allowed_results`: a non-empty map of allowed statuses to non-empty result lists
- `on`: a deterministic transition map keyed by `status/result`

Allowed result values must be non-empty strings. Every `on` key must be declared in `allowed_results`, and every declared `status/result` pair must have a deterministic transition to another step or a supported terminal state.

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
