# Reference

## Purpose

Provide lookup-style documentation for configuration surfaces and other reference material.

## Audience

Contributors and maintainers who need quick factual lookup docs rather than narrative explanations.

## Read This When

- You need to locate configuration surfaces.
- You want quick links to subsystem-specific reference docs.

## Related Docs

- [../operations/README.md](../operations/README.md)
- [../features/README.md](../features/README.md)

## Docs

- [configuration.md](configuration.md): code-backed `.orc` config landing page, loaders, and environment-variable notes
- [configuration-init.md](configuration-init.md): `orc init` scaffold files, overwrite prompts, `.gitignore`, `.orc/runs/`, and scaffolded workflow and agent inventory
- [configuration-init-upgrade.md](configuration-init-upgrade.md): `orc init upgrade` plan/apply contract, setup version marker, migration scope, safety rules, and initial `0 -> 1` migration
- [configuration-project.md](configuration-project.md): `.orc/config.yaml`, project config validation, loop caps, and sandbox config schema
- [configuration-runtimes.md](configuration-runtimes.md): configurable runtime descriptor schema and workflow-selection contract
- [configuration-workflows.md](configuration-workflows.md): workflow files, step contracts, report outcomes, terminal states, and agent descriptor files
- [run-store.md](run-store.md): durable `.orc/runs/<run-id>` contract landing page and reader-task index
- [run-store-layout.md](run-store-layout.md): run ID, directory layout, and filesystem safety contract
- [run-store-events.md](run-store-events.md): append-only event log and v1 event payload contract
- [run-store-status-artifacts.md](run-store-status-artifacts.md): latest status and artifact reference contract
- [run-store-operations.md](run-store-operations.md): operational rules, attempt lifecycle preconditions, and log append API
- [workflow-engine.md](workflow-engine.md): deterministic workflow transition contract
