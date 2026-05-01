# Local Test Workflows

## Purpose

Describe the main local commands and supporting assets used to verify repository behavior.

## Audience

Contributors running tests locally during development or review.

## Read This When

- You need the standard test commands.
- You need to know where config fixtures live.
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

- `testdata/config/valid/.orc`: valid v1 project config fixture.
- Tests under `internal/config` create additional temporary invalid projects for validation coverage.

## If You Changed X, Run Y

- CLI command behavior: `go test ./internal/cli`
- config parsing, path safety, or workflow validation: `go test ./internal/config`
- package boundaries or shared behavior: `go test ./internal/...`
- task commands, toolchain docs, or build behavior: `task build` and `task tests`
- concurrency-sensitive future runtime behavior: `task tests-race`
