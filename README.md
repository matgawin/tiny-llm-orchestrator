# Tiny Orc

## Purpose

Tiny Orc is a project-local control plane for supervising LLM coding work as
durable, inspectable workflow runs. It records task context, launches
workflow-selected workers, preserves logs and reports, and keeps a human or
main agent accountable for routing, verification, and final handoff.

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
default workflow does not launch an orchestrator worker. Operators normally use
`orc run advance` to drive workflow-selected role-specific workers, such as
planner, coder, tester, or reviewer, until the run reaches a conservative stop.
Use `orc worker launch-next` when intentionally launching exactly one selected
attempt.

Runtime entrypoint:

- `cmd/orc`: builds the `orc` command.

Primary dependencies:

- Go `1.26.x`
- `github.com/spf13/cobra` for CLI command routing and help
- `github.com/goccy/go-yaml` for YAML config parsing
- Nix development shell with `go-task`, `jujutsu`, `beads`, formatters, and lint tooling
- (optional) [Beads](https://github.com/gastownhall/beads)

## Documentation Index

- [CONTRIBUTING.md](CONTRIBUTING.md): contributor workflow and required checks
- [docs/README.md](docs/README.md): permanent docs index
- [docs/getting-started/README.md](docs/getting-started/README.md): local setup and repo layout
- [docs/architecture/README.md](docs/architecture/README.md): system shape and package boundaries
- [docs/testing/README.md](docs/testing/README.md): test strategy and local verification paths
- [docs/operations/README.md](docs/operations/README.md): runtime stack notes
- [docs/features/README.md](docs/features/README.md): durable behavior areas
- [docs/reference/README.md](docs/reference/README.md): configuration and durable contract lookup docs

## Where To Look For X

- run start, task context capture, and dirty-start VCS policy: `internal/runstart`, `internal/vcs`, and [docs/features/run-start.md](docs/features/run-start.md)
- follow-up recording: [docs/features/follow-up-recording.md](docs/features/follow-up-recording.md)
- run inspection behavior: [docs/features/run-inspection.md](docs/features/run-inspection.md)
- summary context rendering: [docs/features/summary-context.md](docs/features/summary-context.md)
- final summary recording: [docs/features/final-summary-recording.md](docs/features/final-summary-recording.md)
- worker prompt rendering: `internal/promptrender` and [docs/features/worker-prompt-rendering.md](docs/features/worker-prompt-rendering.md)
- worker launching and process supervision: `internal/launcher` and [docs/features/worker-launching.md](docs/features/worker-launching.md)
- live worker progress contract: [docs/features/live-worker-progress.md](docs/features/live-worker-progress.md)
- project configuration, runtime descriptors, init scaffolding, and workflow graph schema: [docs/reference/configuration.md](docs/reference/configuration.md), [docs/reference/configuration-runtimes.md](docs/reference/configuration-runtimes.md), [docs/reference/configuration-init.md](docs/reference/configuration-init.md), [docs/reference/configuration-project.md](docs/reference/configuration-project.md), and [docs/reference/configuration-workflows.md](docs/reference/configuration-workflows.md)
- deterministic workflow transition logic: `internal/workflow` and [docs/reference/workflow-engine.md](docs/reference/workflow-engine.md)
- run persistence: `internal/runstore` and [docs/reference/run-store.md](docs/reference/run-store.md)
- local setup and troubleshooting: [docs/getting-started/README.md](docs/getting-started/README.md)
- tests, local verification, and coverage expectations: [docs/testing/README.md](docs/testing/README.md)
- contributor workflow and repo rules: [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/contributing/README.md](docs/contributing/README.md)

The shortest local-start sequence is:

```bash
nix develop
task tests
task build
./bin/orc version
```
