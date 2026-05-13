# System Overview

## Purpose

Describe the repository's current runtime shape, main package responsibilities, and major dependencies in enough detail to reason about cross-cutting changes.

## Audience

Contributors changing CLI behavior, project config validation, workflow
semantics, launcher behavior, sandbox behavior, or runtime descriptor support.

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
- `internal/config` loads and validates `.orc` project configuration,
  workflow files, agent descriptors, and runtime descriptors.

The CLI currently exposes help, version, shell completion generation, init,
live progress reporting, `run start`, `run add-followup`, read-only
`run status` / `run next` behavior, `run advance` for conservatively advancing
selected worker attempts, `worker launch-next` for launching one
workflow-selected worker attempt, `sandbox run` for configured sandbox command
execution, and `report` for worker report submission.

Config loading and validation, deterministic workflow transitions,
task-context resolution, inspection, prompt rendering, worker launch, report
validation, and durable run persistence are implemented as package logic and are
exercised by tests and fixtures. Later routing work consumes reported outcomes
for retry integration.

## Core Data Flow

Config loading follows this shape:

1. Resolve the project root and `.orc` directory.
2. Read `.orc/config.yaml`.
3. Resolve referenced workflow, agent descriptor, and runtime descriptor paths
   relative to `.orc`.
4. Reject absolute paths, path traversal, and symlink escapes outside `.orc`.
5. Parse and validate workflows, deterministic transitions, retries,
   task-context policy, agent descriptors, runtime descriptors, and
   workflow-selected runtime/model/runtime directory fields.

Run start loads validated project config, enforces task-context and VCS policy,
rejects dirty or no-VCS starts when the workflow requires that, and persists
explicit task context plus the pre-run VCS snapshot through the Run Store. See
[../features/run-start.md](../features/run-start.md) for run-start behavior.

## Runtime Packages

These packages define or reserve ownership for orchestration behavior outside the config-loading boundary:

- `internal/runstore`: inspectable persistent run state under `.orc/runs/<run-id>`.
- `internal/runstart`: explicit task-context resolution and run creation for
  `orc run start`.
- `internal/vcs`: read-only jj/git/no-VCS inspection and VCS summary snapshot
  rendering.
- `internal/runinspect`: read-only run status and next-action inspection.
- `internal/promptrender`: role-specific worker prompt rendering for selected
  workflow steps.
- `internal/report`: report validation and persistence for active worker
  attempts, including report-sourced follow-up recording.
- `internal/workflow`: deterministic workflow graph transitions.
- `internal/launcher`: worker process start and supervision from the
  workflow-selected runtime descriptor.

Do not push launcher or run-store concerns into `internal/config`; config
validation should remain a contract-loading boundary. `internal/config` owns
runtime descriptor schema and static validation, while `internal/launcher`
owns building the selected worker argv and checking active sandbox-mode
compatibility before process start.
