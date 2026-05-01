# Documentation Rules

## Purpose

Define how permanent docs, and subsystem-local READMEs should be used in this repository.

## Audience

Contributors adding, moving, or updating documentation.

## Read This When

- You are deciding where a new doc should live.
- You are updating a workflow, runtime contract, or durable behavior.

## Related Docs

- [../README.md](../README.md)
- [../../CONTRIBUTING.md](../../CONTRIBUTING.md)

## Rules

- Keep one canonical home for each durable topic.
- Start each permanent doc with Purpose, Audience, Read This When, and Related Docs.
- Keep repository-wide durable guidance under `docs/` rather than in subsystem-local READMEs.
- Exception: a subsystem-local README that is copied verbatim into a shipped artifact may optimize for artifact consumers instead of repo navigation. In that case it may omit the standard header sections if the surrounding permanent docs still describe the workflow and point to the artifact.
- Keep low-level package or subsystem details close to the code when that location is the natural canonical home.
- Link sideways to adjacent docs instead of duplicating the same policy in several places.
- Keep lookup-heavy material in `docs/reference/`.

## Update Expectations

- Update permanent docs in the same change that modifies durable behavior, workflow, architecture, or runtime contracts.
- Review category indexes whenever you add a new permanent doc so the navigation stays current.
