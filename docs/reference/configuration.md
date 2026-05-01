# Configuration Reference

## Purpose

Provide a code-backed reference for the repository's configuration files and config surfaces.

## Audience

Contributors and maintainers changing config loading, validation, fixtures, or future init behavior.

## Read This When

- You need to know which `.orc` files are consumed by the app.
- You are updating config schema validation.
- You are updating config fixtures or generated init output.

## Related Docs

- [../getting-started/local-development.md](../getting-started/local-development.md)
- [../operations/runtime-stack.md](../operations/runtime-stack.md)

## Config Files And Loaders

The current config loader is `internal/config.Load(projectRoot)`.

It reads:

- `.orc/config.yaml`
- workflow files referenced by `workflows` entries
- agent descriptor files referenced by `agents` entries

The public fixture for the current v1 shape is `testdata/config/valid/.orc`.

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

Allowed result values must be non-empty strings. Every `on` key must be declared in `allowed_results`, and every declared `status/result` pair must have a matching `on` transition. Transition targets must be either another declared step or a supported terminal state.

Workflow transitions are deterministic: every allowed result pair must have an `on` transition to another step or a supported terminal state.

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

The application currently has no runtime environment-variable configuration surface. Tooling-related environment variables, such as `BEADS_DIR` and `CODEX_BIN`, belong to the development shell and agent workflow rather than the `orc` app config schema.
