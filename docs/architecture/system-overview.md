# System Overview

## Purpose

Describe the repository's current runtime shape, main package responsibilities, and major dependencies in enough detail to reason about cross-cutting changes.

## Audience

Contributors changing CLI behavior, project config validation, workflow semantics, or future runtime orchestration packages.

## Read This When

- You are new to the repository architecture.
- You need to decide which package owns a behavior.
- You are preparing a change that may affect config, workflow, launcher, or run-state behavior.

## Related Docs

- [service-boundaries.md](service-boundaries.md)
- [../reference/configuration.md](../reference/configuration.md)
- [../getting-started/project-layout.md](../getting-started/project-layout.md)

## Current Runtime

The only runnable service today is the `orc` CLI:

- `cmd/orc` starts the process.
- `internal/cli` handles command dispatch and user-facing output.
- `internal/config` loads and validates `.orc` project configuration.

The CLI currently exposes help, version, init, and `run start` behavior.
Config loading and validation, deterministic workflow transitions, task-context
resolution, and durable run-state primitives are implemented as package logic
and are exercised by tests and fixtures. Later run commands will consume the
run store and workflow engine for inspection, prompt rendering, report
handling, and worker launch.

## Core Data Flow

Config loading follows this shape:

1. Resolve the project root and `.orc` directory.
2. Read `.orc/config.yaml`.
3. Resolve referenced workflow and agent descriptor paths relative to `.orc`.
4. Reject absolute paths, path traversal, and symlink escapes outside `.orc`.
5. Parse and validate workflows, deterministic transitions, retries, task-context policy, and agent descriptors.

Run start loads validated project config, enforces task-context policy, and
persists explicit task context through the Run Store. See
[../features/run-start.md](../features/run-start.md) for source-specific
behavior.

## Runtime Packages

These packages define or reserve ownership for orchestration behavior outside the config-loading boundary:

- `internal/runstore`: inspectable persistent run state under `.orc/runs/<run-id>`.
- `internal/runstart`: explicit task-context resolution and run creation for
  `orc run start`.
- `internal/workflow`: deterministic workflow graph transitions.
- `internal/launcher`: future worker process start and supervision.

Do not push future launcher or run-store concerns into `internal/config`; config validation should remain a contract-loading boundary.
