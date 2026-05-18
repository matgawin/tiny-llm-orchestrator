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
- [configuration-init-upgrade.md](configuration-init-upgrade.md)
- [configuration-project.md](configuration-project.md)
- [configuration-workflows.md](configuration-workflows.md)

## Contract Pages

- [configuration-init.md](configuration-init.md): `orc init` scaffold files, overwrite prompts, `.gitignore`, `.orc/runs/`, and scaffolded workflow and agent inventory.
- [configuration-init-upgrade.md](configuration-init-upgrade.md): `orc init upgrade` plan/apply contract, setup version marker, migration scope, safety rules, and initial `0 -> 1` migration.
- [configuration-project.md](configuration-project.md): `.orc/config.yaml`, project config validation, loop caps, and sandbox config schema.
- [configuration-runtimes.md](configuration-runtimes.md): `.orc/runtimes/*.yaml` descriptor schema, runtime selection, descriptor-built argv, prompt delivery, capabilities, sandbox requirements, and Codex migration.
- [configuration-workflows.md](configuration-workflows.md): workflow files, step contracts, report outcomes, terminal states, and agent descriptor files.

## Config Files And Loaders

The current config loader is `internal/config.Load(projectRoot)`.

It reads:

- `.orc/config.yaml`
- workflow files referenced by `workflows` entries
- agent descriptor files referenced by `agents` entries
- runtime descriptor files referenced by `runtimes` entries

`.orc/config.yaml` contains both `version`, the project config schema version,
and optional `setup_version`, the `orc init upgrade` setup/scaffold version.
Missing `setup_version` is treated as legacy setup version `0`; new scaffolds
write `setup_version: 1`.

Agents are prompt/persona descriptors. Runtimes are executable descriptors.
`internal/config` validates both inventories, keeps them separate, and validates
workflow agent-step runtime, model, reasoning, and runtime directory selection
against the loaded runtime descriptors. See
[configuration-runtimes.md](configuration-runtimes.md) for the runtime contract
and [configuration-workflows.md](configuration-workflows.md) for workflow
precedence rules.

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
