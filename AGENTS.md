# Agent Guide

## Purpose

Keep the always-loaded agent context small. Use this file for repo-wide guardrails, then rely on local `AGENTS.md` files and repo docs for subtree-specific guidance.

## Repo Map

- [README.md](README.md): repo map and subsystem ownership
- [CONTRIBUTING.md](CONTRIBUTING.md): workflow and required checks
- [docs/README.md](docs/README.md): permanent docs index

## Mandatory Skills

Use repo-local skills from `.agents/skills/` whenever their objective trigger applies. Match triggers from:
- user intent words in the request;
- changed paths or owner areas;
- diff shape such as generated artifacts, mocks, or boundary changes;
- required workflow phase such as scoping, verification planning, docs sync, or final handoff.

Treat this as the default phase order for non-trivial work:
1. run `change-scope` first for any implementation, config, runtime, workflow, or durable-doc change that is not a tiny local refactor or read-only inspection;
2. run the narrow task-specific skill(s) that match the request, changed paths, or diff shape;
3. run `verify-change` before handoff.

Use `verify-change` before handoff. For multi-surface or higher-risk changes, `verify-change` should perform the final review pass with bounded read-only subagents when the active runtime permits delegation.

Use `docs-sync` whenever a change affects durable behavior, contracts, or workflow; do not leave doc drift for later.

### Planning and scoping
- `change-scope`: first step for any non-trivial runtime, config, workflow, architecture, or durable-doc change
- `cross-surface-impact`: immediately after `change-scope` when a feature or entity change may span code, config, docs, and tests

### Config and docs workflows
- `docs-sync`: when durable behavior, contracts, workflow, or architecture rules change
- `beads-issue-create`: when creating, splitting, validating, or updating beads issues

### Verification and handoff
- `test-surface-selection`: for any behavior, contract, storage, runtime, or migration change that needs proof selection before final verification
- `local-mock-review`: when `_test.go` diffs add local mocks, fakes, stubs, generated mocks, or mock-heavy package tests
- `jj-workflow`: when workspace state, unrelated diffs, or stacked changes could affect verification or handoff
- `verify-change`: after code or docs changes, before handoff

## Global Rules

- If a change affects generated sources, edit the canonical inputs first and regenerate derived artifacts instead of patching generated outputs directly.
- Human docs are canonical for durable repository policy. If code and docs disagree, trust the code and fix the stale doc in the same change.
- Use `jj` for repository operations instead of `git`.
- Prefer `nix develop -c ...` and `task` commands so checks and generators use the pinned toolchain.
- Prefer narrow owner packages over generic helper buckets.
- Prefer pure helpers and real integration coverage over mock-heavy seam tests.

## Subagent Policy

Use subagents only when the active runtime and user instructions permit delegation, and only for bounded, read-heavy work that would otherwise add noise to the main thread.

Consider subagents when:
- a task crosses more than one major layer.
- a feature or entity change may span code, docs, and verification surfaces.
- or the correct verification/doc-impact path is unclear and parallel exploration will reduce context load.

Default subagent rules:
- Spawn at most 5 subagents unless the task explicitly benefits from more.
- Keep scopes disjoint; one concern per subagent.
- Prefer read-only analysis by default.
- Do not delegate repo editing to subagents unless file ownership is clearly split and conflicts are impossible.
- If subagents are used, require the main agent to wait for all subagents, merge their findings, and summarize paths, risks, and recommended next steps before editing.

Default lanes when needed:
1. code-path / ownership impact
2. verification / test surface impact
3. docs / contracts / migration impact

## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run `bd prime` for workflow context.

**Quick reference:**
- `bd ready` - Find unblocked work
- `bd create "Title" --type task --priority 2` - Create issue

For full workflow details: `bd prime`

Important:
- This repo uses jj, not git.
- Use `jj status`, `jj diff`, and `jj describe`.
- Do not run git commands unless explicitly asked.
- Beads is git-free here. Use `BEADS_DIR=$PWD/../.beads`.
- Do not use `bd edit`; use `bd update` with flags, only when scope of the task changes.
- Use `bd comments` for adding additional info.
- Before working on new task: `bd ready --json`.
- Claim work: `bd update <id> --claim`.
- Never close issues on your own, only if asked by reviewer.
- Most of the nix and task files will need elevated execution, so ask for it.
- Never add mocks/stubs to tests, always test actual logic, which may require splitting functions.

## Done When

- Relevant local `AGENTS.md` files and required skills were used.
- Generated artifacts were regenerated when source inputs changed.
- Permanent docs were updated when durable behavior or contracts changed.
- Verification was run at the narrowest sufficient scope, or any unrun validation and the reason for it were called out explicitly.

## Tiny Orc

- Project-local orchestration config lives under `.orc/`.
- Persistent workflow and role descriptor files are user-owned and reviewable.
- Runtime run state belongs under `.orc/runs/`, which should stay ignored by VCS.
- Use `orc init --dry-run` before changing an existing scaffold.
- In this project, use `orc` for orchestration, it should be available in PATH; do not use `go run`, or `./bin/*` builds, to execute it.
