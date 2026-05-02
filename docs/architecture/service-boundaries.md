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
- `internal/workflow` should own deterministic workflow transitions when runtime execution is implemented.
- `internal/runstore` should own persistent run state when orchestration runs become inspectable.
- `internal/launcher` should own worker process launch and supervision.

## Boundary Rules

- Keep config schema validation independent from process-launch behavior.
- Keep command routing, help output, and command-level error wrapping in
  `internal/cli`; command packages such as `internal/initconfig` own
  domain-specific prompts and status output.
- Keep future runtime state transitions out of the file-loading layer.
- Add narrow package-local helpers before introducing shared abstractions.
- When a behavior spans CLI and config validation, test the deterministic validation logic directly and keep CLI tests focused on command behavior.
