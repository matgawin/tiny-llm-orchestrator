# Project Layout

## Purpose

Explain the repository structure and show where the main application responsibilities live.

## Audience

Contributors navigating the codebase or deciding where new code and docs should live.

## Read This When

- You are new to the codebase.
- You need to find the entrypoint or ownership area for a change.
- You are deciding whether a doc belongs globally or near a subsystem.

## Related Docs

- [../../README.md](../../README.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)

## Main Layout

- `cmd/orc`: CLI process entrypoint.
- `internal/cli`: command parsing, help/version output, and CLI stream handling.
- `internal/config`: `.orc` project config loading, YAML parsing, path safety, workflow validation, and agent descriptor validation.
- `internal/workflow`: future deterministic workflow graph execution logic.
- `internal/runstore`: future persistent orchestration run state.
- `internal/launcher`: future worker process launcher and supervision code.
- `testdata/config`: config fixtures used by package tests.
- `docs`: durable repository documentation.
- `.agents`: Codex guidance and repo-local workflow skills.
- `nix`, `flake.nix`, `flake.lock`: reproducible development shell and package definition.

## Documentation Placement

- Put repository-wide durable guidance under `docs/`.
- Put low-level, subsystem-specific guidance near the owning package when that location is clearer.
- Keep the root `README.md` as an index, not a duplicate of every detailed doc.

Useful split in practice:

- `docs/features/`: durable CLI/config behavior areas
- `docs/reference/`: lookup-heavy config material
- `docs/architecture/`: package ownership and boundary rules
- subsystem-local docs: implementation details that are only meaningful inside one package
