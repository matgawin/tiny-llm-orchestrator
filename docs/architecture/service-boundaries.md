# Service Boundaries

## Purpose

Define the package ownership boundaries used by the current CLI and config loader.

## Audience

Contributors changing package structure, config validation, CLI behavior, or future orchestration runtime code.

## Read This When

- You are deciding where new behavior belongs.
- You are reviewing a change that moves logic across packages.
- You are adding a new runtime package or broadening an existing package boundary.

## Related Docs

- [system-overview.md](system-overview.md)
- [../contributing/coding-standards.md](../contributing/coding-standards.md)

## Main Boundaries

- `cmd/orc` owns process startup only.
- `internal/cli` owns the command boundary.
- `internal/initconfig` owns the project-local `orc init` scaffold.
- `internal/config` owns `.orc` config loading, path safety, YAML parsing, workflow validation, and agent descriptor validation.
- `internal/runstart` owns explicit task-context resolution for `orc run start`.
  Feature semantics live in [../features/run-start.md](../features/run-start.md).
- `internal/runinspect` owns read-only run inspection for `orc run status` and
  `orc run next`. Feature semantics live in
  [../features/run-inspection.md](../features/run-inspection.md).
- `internal/promptrender` owns role-specific worker prompt rendering and prompt
  artifact persistence. Feature semantics live in
  [../features/worker-prompt-rendering.md](../features/worker-prompt-rendering.md).
- `internal/runstore` owns persistent run state under `.orc/runs/<run-id>`.
- `internal/workflow` owns deterministic workflow transitions for validated workflow definitions and in-memory run state.
- `internal/launcher` should own worker process launch and supervision.

## Boundary Rules

- Keep config schema validation independent from process-launch behavior.
- Keep command routing, help output, and command-level error wrapping in
  `internal/cli`; command packages such as `internal/initconfig` own
  domain-specific prompts and status output.
- Keep future runtime state transitions out of the file-loading layer.
- Keep workflow routing, worker launch, content redaction, and cross-process
  write coordination out of `internal/runstore`; it is the persistence
  boundary for v1.
- Add narrow package-local helpers before introducing shared abstractions.
- When a behavior spans CLI and config validation, test the deterministic validation logic directly and keep CLI tests focused on command behavior.
