# Local Development

## Purpose

Describe the supported local development workflow.

## Audience

Contributors running the repo locally for development, debugging, or test verification.

## Read This When

- You need the local toolchain.
- You want to run `orc` locally.
- You need to know which task commands matter.

## Related Docs

- [../../README.md](../../README.md)
- [project-layout.md](project-layout.md)
- [../testing/local-test-workflows.md](../testing/local-test-workflows.md)

## Prerequisites

- Go `1.26.x`
- The go-task `task` binary
- `golangci-lint`, `gofumpt`, and `goimports` for the standard check workflow

The repository includes a Nix development shell in `flake.nix` with the expected toolchain. Use it when your host machine is missing tools or maps `task` to a different program.

```bash
nix develop
```

The repo `.envrc` uses the same flake and adds `.direnv/bin/t`, a short wrapper for `task`.

## Configuration Files

Project-local Tiny Orc configuration is expected under `.orc/` in the project being orchestrated. The current loader reads:

- `.orc/config.yaml`
- workflow files referenced from `.orc/config.yaml`
- agent descriptor files referenced from `.orc/config.yaml`

The repository fixture at `testdata/config/valid/.orc` documents the current v1 config shape used by tests.

## Local Workflow

Common checks:

```bash
task fix
task format
task lint
task tests
task build
task check
```

For one-shot commands from outside the shell, use the pinned toolchain through Nix:

```bash
nix develop -c task tests
nix develop -c task build
```

Useful direct commands:

```bash
go test ./...
go test ./internal/config -run TestLoad
go run ./cmd/orc version
```

## Notes

- `task build` builds `cmd/orc` to `bin/orc`.
- `task tests` runs `go test ./...`.
- `task tests-race` runs `go test -race ./...` through `task test-unit-race`.
- `task check` runs fix, format, lint, tests, and build.

## Troubleshooting

- If `task` runs the wrong executable, enter `nix develop` or use the `.envrc` wrapper `t`.
- If Go cache permissions under `$HOME/.cache` fail in a restricted environment, rerun with workspace-local cache variables before treating the failure as a code issue.
- If `orc` cannot load a config fixture, check that referenced workflow and agent paths stay under `.orc`; symlink escapes are rejected.
