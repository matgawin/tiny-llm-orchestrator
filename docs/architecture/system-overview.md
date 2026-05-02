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

The CLI currently exposes help and version behavior. Config loading and validation, deterministic workflow transitions, and durable run-state primitives are implemented as package logic and are exercised by tests and fixtures. No CLI run commands consume the run store or workflow engine yet.

## Core Data Flow

Config loading follows this shape:

1. Resolve the project root and `.orc` directory.
2. Read `.orc/config.yaml`.
3. Resolve referenced workflow and agent descriptor paths relative to `.orc`.
4. Reject absolute paths, path traversal, and symlink escapes outside `.orc`.
5. Parse and validate workflows, deterministic transitions, retries, task-context policy, and agent descriptors.

## Runtime Packages

These packages define or reserve ownership for orchestration behavior outside the config-loading boundary:

- `internal/runstore`: inspectable persistent run state under `.orc/runs/<run-id>`.
- `internal/workflow`: deterministic workflow graph transitions.
- `internal/launcher`: future worker process start and supervision.

Do not push future launcher or run-store concerns into `internal/config`; config validation should remain a contract-loading boundary.
