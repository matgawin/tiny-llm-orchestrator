# Coding Standards

## Purpose

Capture the durable coding and architecture rules contributors must follow when changing the codebase.

## Audience

Contributors editing application code, tests, or wiring in this repository.

## Read This When

- You need the package-boundary or config-validation rules.
- You need to align new code with the repo's existing standards.

## Related Docs

- [repository-workflow.md](repository-workflow.md)
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)
- [../testing/strategy.md](../testing/strategy.md)

## Rules

- Follow normal Go naming and formatting conventions.
- Keep validation errors explicit and close to the package that owns the contract.
- Use `errors.Is` when translating sentinel errors across package boundaries.
- Do not add new generic helper buckets such as `utils` or `serviceutils`; use narrow package names or keep helpers local to the owning package.
- Keep filesystem path safety checks close to config loading when the behavior protects `.orc` ownership boundaries.

## Error And DTO Boundaries

- Preserve enough context in errors for the caller to identify the config file, workflow, agent, or field that failed.
- Wrap lower-level parse/read errors with the logical config surface being loaded.
- Use public structs for the YAML contract and private helper structs for parsing-only details.

Good pattern:

```go
return nil, fmt.Errorf("workflow %q: %w", name, err)
```

Bad pattern:

```go
return nil, err
```

The bad pattern loses the workflow or config surface that made the error actionable.

## Composition-Root Guidance

- Keep command-line stream handling in `internal/cli`.
- Keep process entrypoint work in `cmd/orc`.
- Keep config file loading and schema validation in `internal/config`.
- Keep runtime-only concerns like worker launch, process supervision, and run persistence out of config validation packages.
- Prefer small local adapters over widening shared interfaces for one caller.

## Generated Code

- Do not manually edit generated files.
- This repo currently has no generated Go or API surface. If one is added later, document the canonical input and regeneration command in the same change.
