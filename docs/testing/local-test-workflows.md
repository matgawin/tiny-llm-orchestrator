# Local Test Workflows

## Purpose

Describe the main local commands and supporting assets used to verify repository behavior.

## Audience

Contributors running tests locally during development or review.

## Read This When

- You need the standard test commands.
- You need the scaffold source policy.
- You are looking for local test support utilities.

## Related Docs

- [strategy.md](strategy.md)
- [../getting-started/local-development.md](../getting-started/local-development.md)

## Commands

Run the default suite:

```bash
task tests
```

Run the race-detector suites:

```bash
task tests-race
task test-unit-race
```

Run package-level tests directly when narrowing a change:

```bash
go test ./internal/...
```

Run `gopls` diagnostics on changed Go files:

```bash
task lsp
```

Run `gopls` diagnostics across the Go workspace when a change spans broad
package structure:

```bash
task lsp-all
```

Run a single package or test when narrowing behavior:

```bash
go test ./internal/config -run TestLoad
go test ./internal/cli -run Test
```

Run format/lint/build checks that commonly accompany a Go change:

```bash
task fix
task format
task lint
task build
task check
```

## Fixtures

- Config tests use `internal/initconfig/scaffold/.orc` as the canonical valid
  scaffold source; see [../reference/configuration-init.md](../reference/configuration-init.md).
- Tests under `internal/config` create additional temporary invalid projects for validation coverage.

## If You Changed X, Run Y

- CLI command behavior: `go test ./internal/cli`
- config parsing, path safety, or workflow validation: `go test ./internal/config`
- run-store persistence: `go test ./internal/runstore`
- worker launching, active-attempt inspection, or process supervision: `go test ./internal/launcher ./internal/runstore ./internal/runinspect ./internal/cli`
- worker-launching race coverage: add `go test -race ./internal/launcher ./internal/runstore ./internal/cli` when active-attempt coordination, timeout cleanup, public launch wiring, signal propagation, or process recovery changes
- Go semantic navigation or edited-file diagnostics: `task lsp`
- package boundaries or shared behavior: `go test ./internal/...`
- task commands, toolchain docs, or build behavior: `task build` and `task tests`
- concurrency-sensitive runtime behavior: `task tests-race`
