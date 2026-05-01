# Runtime Stack

## Purpose

Describe the local runtime dependencies used by the repository.

## Audience

Contributors and maintainers working on runtime-sensitive changes or local startup issues.

## Read This When

- You need to know which external systems the repo depends on.
- You are debugging local CLI startup or toolchain issues.
- You are updating operational assumptions.

## Related Docs

- [../reference/configuration.md](../reference/configuration.md)
- [../getting-started/local-development.md](../getting-started/local-development.md)

## Runtime Dependencies

The current `orc` CLI has no database, queue, HTTP service, or containerized runtime dependency.

The local development stack is toolchain-only:

- Go
- go-task
- golangci-lint
- goimports
- gofumpt
- jj
- beads

The Nix flake provides these tools for normal development.

## Service Runtimes

The repository currently builds one binary:

- `bin/orc`, produced by `task build`

## Local Stack

Use the development shell and task commands:

```bash
nix develop
task tests
task build
```

For a single command without entering the shell:

```bash
nix develop -c task tests
```

## When The Local Stack Does Not Start

- If `task` resolves to a non-go-task binary, enter `nix develop`.
- If `codex` inside the Nix shell cannot start, set `CODEX_BIN` to the underlying Codex executable as described by the wrapper in `flake.nix`.
- If config loading fails, inspect `.orc/config.yaml` and ensure all referenced files stay under `.orc`.
