# Future Work

## Purpose

Track useful ideas that are intentionally outside the current Tiny Orc v1
surface.

## Audience

Maintainers and contributors deciding whether a proposal belongs in the current
implementation or should stay deferred.

## Read This When

- You are considering expanding the v1 orchestration surface.
- You need to check whether an idea was already deferred.
- You are creating future beads issues from known non-v1 work.

## Related Docs

- [../README.md](../README.md)
- [README.md](README.md)
- [features/README.md](features/README.md)
- [reference/configuration.md](reference/configuration.md)
- [reference/workflow-engine.md](reference/workflow-engine.md)

## Deferred Scope

These features are useful, but are not required for the first useful version:

- zellij layout generation
- zellij live pane or tab control
- native Codex subagent descriptor compatibility
- native Codex subagent execution
- symlinks into Codex custom agent directories
- automatic jj workspace creation
- localhost HTTP report server
- report delivery fallback from HTTP to direct file persistence
- expected-path enforcement beyond recording observed changed paths
- richer manual inspection commands
- web or TUI dashboard
- MCP integration
- replacing beads or wrapping the full bead lifecycle
- automatically closing beads
- requiring beads for every run
- running multiple orchestrators in one checkout
- automatically resolving conflicts
- automatically reverting unexpected changes
- remote or multi-machine orchestration
- Go plugin system for workflows
- non-flake distribution
- migrating the earlier Bash prototype directly

## Deferred Policy Questions

- Whether a later version should support explicit bead note writing. V1 keeps
  beads read-only and leaves note writing and closure to humans after review.
- Whether workflow or agent configuration should ever be edited automatically
  during a normal run. V1 treats those files as user-owned and reviewable.

## Notes

- Current v1 behavior belongs in the feature, reference, architecture, testing,
  and operations docs rather than in this file.
- Future items should become beads issues when they are ready for design or
  implementation.
