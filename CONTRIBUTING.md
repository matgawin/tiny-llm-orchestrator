# Contributing

## Purpose

Define the contributor workflow and route contributors to the canonical policy and subsystem docs.

## Audience

Anyone making code, test, documentation, or operational changes in this repository.

## Read This When

- You are preparing to make a change in this repo.
- You need the required local commands and repo workflow.
- You want to know which docs must be updated with code changes.

## Related Docs

- [README.md](README.md)
- [docs/README.md](docs/README.md)
- [docs/contributing/README.md](docs/contributing/README.md)
- [docs/testing/README.md](docs/testing/README.md)

## Workflow

- Use `task` for common local workflows.
- Use beads, if available, for tracked repository work. Run `bd prime/human` when you need workflow context, `bd ready --json` before selecting new work, and claim an issue before editing it.
- Keep changes small enough that docs, tests, and behavior stay aligned in the same change.

## Required Local Checks

Run the relevant checks for your change before sending it for review:

```bash
task fix
task format
task lint
task tests
task build
```

Or run the combined check:

```bash
task check
```

## Documentation Rule

Update the matching permanent docs in the same change when you modify:

- durable runtime behavior
- contributor workflow or coding policy
- architecture or dependency boundaries
- test strategy or operator-facing workflows
- configuration or CLI contracts

## Policy Index

- [docs/contributing/coding-standards.md](docs/contributing/coding-standards.md)
- [docs/contributing/repository-workflow.md](docs/contributing/repository-workflow.md)
- [docs/contributing/documentation-rules.md](docs/contributing/documentation-rules.md)
- [docs/testing/strategy.md](docs/testing/strategy.md)
- [docs/testing/local-test-workflows.md](docs/testing/local-test-workflows.md)
