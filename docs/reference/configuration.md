# Configuration Reference

## Purpose

Provide a code-backed reference for the repository's configuration files and config surfaces.

## Audience

Contributors and maintainers changing config loading, validation, scaffold source, or init behavior.

## Read This When

- You need to know which `.orc` files are consumed by the app.
- You are updating config schema validation.
- You are updating scaffold source or generated init output.

## Related Docs

- [../getting-started/local-development.md](../getting-started/local-development.md)
- [../operations/runtime-stack.md](../operations/runtime-stack.md)
- [configuration-init.md](configuration-init.md)
- [configuration-project.md](configuration-project.md)
- [configuration-workflows.md](configuration-workflows.md)

## Contract Pages

- [configuration-init.md](configuration-init.md): `orc init` scaffold files, overwrite prompts, `.gitignore`, `.orc/runs/`, and scaffolded workflow and agent inventory.
- [configuration-project.md](configuration-project.md): `.orc/config.yaml`, project config validation, loop caps, and sandbox config schema.
- [configuration-workflows.md](configuration-workflows.md): workflow files, step contracts, report outcomes, terminal states, and agent descriptor files.

## Config Files And Loaders

The current config loader is `internal/config.Load(projectRoot)`.

It reads:

- `.orc/config.yaml`
- workflow files referenced by `workflows` entries
- agent descriptor files referenced by `agents` entries

The canonical scaffold source for the current v1 shape is
`internal/initconfig/scaffold/.orc`.

## Environment Variables

The application has no general runtime environment-variable configuration
surface. Tooling-related environment variables such as `CODEX_BIN` belong to
the development shell and agent workflow rather than the `orc` app config
schema.

`orc run start --bead <id>` observes inherited `BEADS_DIR` as command source
metadata, not as a `.orc/config.yaml` schema field. See
[../features/run-start.md](../features/run-start.md) for run-start task-source
behavior.
