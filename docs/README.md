# Documentation Index

## Purpose

Provide the canonical index for permanent repository documentation, including what each category is for and how contributors should navigate between them.

## Audience

Maintainers and contributors looking for durable guidance about the repository.

## Read This When

- You need to find the canonical doc for a topic.
- You want to navigate the docs by responsibility instead of by guessing filenames.
- You need the bridge between global docs and subsystem-local READMEs.

## Related Docs

- [../README.md](../README.md)
- [../CONTRIBUTING.md](../CONTRIBUTING.md)

## How To Use This Docs Tree

- Start at the root [README.md](../README.md) if you need the repository map.
- Use [../CONTRIBUTING.md](../CONTRIBUTING.md) when the question is about workflow, required checks, or update expectations.
- Use the category indexes below for durable docs.
- Use subsystem-local READMEs when the detail belongs to one package or tree and would become vague if moved into a global doc.

## Categories

### Getting Started

[getting-started/README.md](getting-started/README.md)

Use this category for:

- local setup
- toolchain expectations
- repo layout and first-run orientation

### Contributing

[contributing/README.md](contributing/README.md)

Use this category for:

- coding standards
- repository workflow
- documentation rules
- contributor-facing policy that is stable across changes

### Architecture

[architecture/README.md](architecture/README.md)

Use this category for:

- service boundaries
- runtime and config-loading flow
- package ownership responsibilities
- subsystem ownership and seams

### Testing

[testing/README.md](testing/README.md)

Use this category for:

- test strategy
- local verification workflows
- coverage expectations
- when to prefer pure-function, unit, or integration coverage

### Operations

[operations/README.md](operations/README.md)

Use this category for:

- runtime dependency stack
- toolchain-sensitive operational workflows

### Features

[features/README.md](features/README.md)

Use this category for:

- durable behavior docs
- CLI and orchestration feature areas
- user-visible or business-facing semantics that outlive a specific implementation

### Reference

[reference/README.md](reference/README.md)

Use this category for:

- `.orc` config surfaces
- `.orc/runs` durable state contracts
- workflow engine transition contracts
- lookup-heavy material that should stay literal and searchable

## Recommended Reading Paths

If you are:

- setting up locally: read [getting-started/local-development.md](getting-started/local-development.md), then [reference/configuration.md](reference/configuration.md)
- changing package wiring or config flow: read [architecture/system-overview.md](architecture/system-overview.md) and [architecture/service-boundaries.md](architecture/service-boundaries.md)
- changing durable CLI, parsing, or validation behavior: start with [features/README.md](features/README.md)
- changing tests: read [testing/strategy.md](testing/strategy.md), then [testing/local-test-workflows.md](testing/local-test-workflows.md)
- changing runtime config: read [reference/configuration.md](reference/configuration.md)
- changing run persistence: read [reference/run-store.md](reference/run-store.md), then [architecture/service-boundaries.md](architecture/service-boundaries.md) if the change touches package ownership
