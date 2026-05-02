# Features

## Purpose

Index the durable behavior docs for the repository's main feature areas.

## Audience

Contributors documenting or changing user-visible or orchestration-facing behavior.

## Read This When

- You need stable behavior docs rather than implementation wiring docs.
- You need the canonical durable doc for a product-facing behavior area.

## Related Docs

- [../architecture/README.md](../architecture/README.md)
- [../operations/README.md](../operations/README.md)
- [../reference/README.md](../reference/README.md)

## Current Feature Areas

- Run-store behavior: currently documented in [../reference/run-store.md](../reference/run-store.md)
- CLI command behavior: [../../README.md](../../README.md) and `internal/cli`
- `.orc` config schema and validation: [../reference/configuration.md](../reference/configuration.md)
- Workflow behavior: package boundaries in [../architecture/system-overview.md](../architecture/system-overview.md). Config schema lives in [../reference/configuration.md](../reference/configuration.md); transition rules live in [../reference/workflow-engine.md](../reference/workflow-engine.md).

Add a dedicated feature doc here when another behavior area grows beyond what belongs in the root README or reference docs.
