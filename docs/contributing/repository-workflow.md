# Repository Workflow

## Purpose

Describe the day-to-day contributor workflow for building, testing, formatting, code generation, and committing changes.

## Audience

Contributors preparing, validating, and packaging repository changes.

## Read This When

- You need the standard commands for local work.
- You need the repo's commit and review conventions.

## Related Docs

- [../../CONTRIBUTING.md](../../CONTRIBUTING.md)
- [coding-standards.md](coding-standards.md)
- [documentation-rules.md](documentation-rules.md)
- [../testing/local-test-workflows.md](../testing/local-test-workflows.md)

## Commands

- `task tests`: run `go test ./...`.
- `task tests-race`: run the race-detector test suite through `task test-unit-race`.
- `task test-unit-race`: run `go test -race ./...`.
- `task lsp`: run `gopls` diagnostics on changed Go files.
- `task lsp-all`: run `gopls` diagnostics on all Go files.
- `task lsp-stats`: print `gopls` workspace statistics for troubleshooting.
- `task fix`: run `go fix ./...`.
- `task format`: run `goimports` and `gofumpt` on Go files.
- `task lint`: run `golangci-lint` with `.golangci.yml`.
- `task build`: build `cmd/orc` to `bin/orc` with the `dev` version.
- `task check`: run fix, format, lint, tests, and build.

Run these commands inside the Nix development shell when possible. For one-shot checks, prefer `nix develop -c task <name>` so the pinned toolchain is used.

## Issue Tracking

The repository uses beads for tracked work.

- Run `bd prime/human` when you need current workflow context.
- Run `BEADS_DIR=$PWD/../.beads bd ready --json` before selecting new work.
- Claim the issue before editing it.
- Do not close issues unless the reviewer or maintainer asks you to close them.

## Workflow Rules

- Prefer the `task` commands over ad hoc local command variants.
- Use the repo-local `go-lsp-workflow` skill for Go semantic navigation and
  edited-file diagnostics. Prefer `gopls` for Go symbols and references, `rg`
  for text search, and `grep` only as a fallback or simple pipeline filter.
- Use race checks when changing shared mutable state, watcher loops, worker launch/supervision, or run-state coordination.
- If cache permissions under `$HOME/.cache` break `go test` or `golangci-lint`, rerun with workspace-safe cache env vars before treating the failure as a code issue.
- If durable behavior changes, update the matching permanent doc in the same change.

Common update-together expectations in this repo:

- CLI behavior or command output -> root README or feature docs when the behavior is durable
- config schema or validation behavior -> reference/config docs and scaffold source
- workflow graph semantics -> architecture or feature docs plus scaffold source
- local workflow or test-command change -> getting-started/testing docs

## Commit Conventions

- Use `jj` for repository operations instead of `git`.
- Use conventional commit titles such as `feat:`, `fix:`, or `refactor:`.
- Keep the first line concise and include a longer description when the change needs it.

Practical commit guideline:

- avoid mixing unrelated doc churn with behavior changes unless the doc updates are required by that behavior change
