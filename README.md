# Tiny Orc

## Purpose

Provide the canonical top-level index for the repository, including the code map, docs map, and the main entrypoints for contributors.

## Audience

Contributors, maintainers, reviewers, operators, and code agents landing in the repository.

## Read This When

- You need the main map of the repository.
- You want to find the right code area or canonical doc before making a change.
- You need to know which subsystem owns a behavior.

## Related Docs

- [CONTRIBUTING.md](CONTRIBUTING.md)
- [docs/README.md](docs/README.md)

## Repository At A Glance

Tiny Orc is a small Go control-plane CLI for project-local LLM orchestration.
The current code scaffolds and validates `.orc` configuration, starts durable
runs from explicit bead or Markdown task context, evaluates deterministic
workflow transitions, records local follow-up work, exposes read-only run
inspection, renders internal worker prompts, launches workflow-selected worker
processes, and provides durable run-store primitives.
The default scaffold is intended to be usable immediately in a new project: it
includes detailed role prompts and workflows for implementation, bugfix,
mechanical changes, test-only work, and focused mechanical, readability,
redundancy, and documentation reviews.

In Tiny Orc docs and command output, "orchestrator" means the supervising
caller that drives the `orc` CLI. In normal Codex use, that is the main Codex
thread started by the user before any worker launch. When `orc` is run manually,
the human operator is effectively filling the same orchestrator role. The
default workflow does not launch an orchestrator worker; `orc worker
launch-next` starts the workflow-selected role-specific worker, such as planner,
coder, tester, or reviewer.

Runtime entrypoint:

- `cmd/orc`: builds the `orc` command.

Primary dependencies:

- Go `1.26.x`
- `github.com/goccy/go-yaml` for YAML config parsing
- Nix development shell with `go-task`, `jujutsu`, `beads`, formatters, and lint tooling

## Documentation Index

Entrypoints:

- [CONTRIBUTING.md](CONTRIBUTING.md): contributor workflow and required checks
- [docs/README.md](docs/README.md): permanent docs index
- [docs/getting-started/README.md](docs/getting-started/README.md): local setup and repo layout
- [docs/architecture/README.md](docs/architecture/README.md): system shape and package boundaries
- [docs/testing/README.md](docs/testing/README.md): test strategy and local verification paths
- [docs/operations/README.md](docs/operations/README.md): runtime stack notes
- [docs/features/README.md](docs/features/README.md): durable behavior areas
- [docs/reference/README.md](docs/reference/README.md): configuration and durable contract lookup docs

## Where To Look For X

- CLI behavior: `internal/cli`
- run start, task context capture, and dirty-start VCS policy: `internal/runstart`, `internal/vcs`, and [docs/features/run-start.md](docs/features/run-start.md)
- follow-up recording: [docs/features/follow-up-recording.md](docs/features/follow-up-recording.md)
- run inspection behavior: [docs/features/run-inspection.md](docs/features/run-inspection.md)
- summary context rendering: [docs/features/summary-context.md](docs/features/summary-context.md)
- final summary recording: [docs/features/final-summary-recording.md](docs/features/final-summary-recording.md)
- worker prompt rendering: `internal/promptrender` and [docs/features/worker-prompt-rendering.md](docs/features/worker-prompt-rendering.md)
- worker launching and process supervision: `internal/launcher` and [docs/features/worker-launching.md](docs/features/worker-launching.md)
- project configuration, init scaffolding, and workflow graph schema: [docs/reference/configuration.md](docs/reference/configuration.md)
- deterministic workflow transition logic: `internal/workflow` and [docs/reference/workflow-engine.md](docs/reference/workflow-engine.md)
- run persistence: `internal/runstore` and [docs/reference/run-store.md](docs/reference/run-store.md)
- local setup and troubleshooting: [docs/getting-started/README.md](docs/getting-started/README.md)
- tests, local verification, and coverage expectations: [docs/testing/README.md](docs/testing/README.md)
- contributor workflow and repo rules: [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/contributing/README.md](docs/contributing/README.md)

## Code Index

### Entrypoints

- `cmd/orc/main.go`: process entrypoint.
- `internal/cli`: CLI command parsing and output.
- `internal/initconfig`: project-local `orc init` scaffold planning and writes.
- `internal/config`: `.orc` config, workflow, and agent descriptor loading/validation.

### Runtime Packages

- `internal/workflow`: deterministic workflow transition engine.
- `internal/runstart`: explicit task-context resolution and run creation for `orc run start`.
- `internal/vcs`: read-only jj/git/no-VCS inspection and VCS summary snapshot rendering.
- `internal/runinspect`: read-only run inspection command implementation.
- `internal/promptrender`: internal role-specific worker prompt renderer.
- `internal/report`: worker report validation and report-sourced follow-up recording.
- `internal/runstore`: persistent run-state package.
- `internal/launcher`: external worker launcher package.

## Local Workflow Index

Use these docs instead of treating this page as the only setup guide:

- [docs/getting-started/local-development.md](docs/getting-started/local-development.md): local toolchain and commands
- [docs/getting-started/project-layout.md](docs/getting-started/project-layout.md): where code and docs live
- [docs/reference/configuration.md](docs/reference/configuration.md): `.orc` config files and schema surfaces
- [docs/testing/local-test-workflows.md](docs/testing/local-test-workflows.md): test commands and config fixture policy

The shortest local-start sequence is:

```bash
nix develop
task tests
task build
./bin/orc version
```
