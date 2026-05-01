---
name: verify-change
description: Use before handoff to choose the final review and verification set across tests, generation, formatting, linting, build, docs, and repo-state checks. For multi-surface or higher-risk changes, consider bounded read-only subagents before finalizing the verification plan when delegation is permitted.
---

# verify-change

Use this after code or docs changes and before handoff.

This skill has two jobs:
1. perform the final review pass for regressions, gaps, and drift;
2. assemble the final verification set across tests, generation, formatting, linting, build, docs, and repo-state checks.

## When to use

Use this:
- after implementation is complete;
- after narrower workflow skills such as `test-surface-selection` or `docs-sync`;
- before handoff or final summary.

If used before handoff, ensure the `handoff summary` aligns with the verification results from `verify-change`.

If the workspace is dirty, stacked, or the final response needs to separate task-relevant diffs from unrelated work, run `jj-workflow` before assembling the final handoff summary.

## Review mode threshold

Do a direct single-agent review when the change is small and local.

Use read-only subagents only when the active runtime and user instructions permit delegation, and at least one of these is true:
- the diff spans more than one major surface;
- the change mixes code, generated artifacts, docs, or migrations;
- the review needs multiple distinct lenses such as behavioral risk, verification gaps, and docs/contracts drift;
- parallel analysis will reduce main-thread noise.

Do not spawn subagents for tiny or purely local changes.

## Subagent policy

If review mode uses subagents:
- spawn at most 5 subagents;
- keep them read-only and analysis-only;
- keep scopes disjoint;
- always wait for all subagents before doing any work;
- merge their findings into one final review summary before choosing the verification set;
- do not delegate editing to subagents.

Default review lanes:
1. behavioral / code-path risk
2. verification / test gap analysis
3. docs / contracts / migration / generated-artifact drift

## Inputs to collect first

1. Collect outputs from narrower workflow skills already used, especially:
   - `test-surface-selection`
   - `docs-sync`
2. Run `jj-workflow` when workspace state may affect verification or handoff.
3. Check which files changed.
4. Check whether generated inputs changed.
5. Check whether durable docs or contracts changed.
6. Check whether the change crosses multiple major surfaces.

## Steps

### 1. Run the final review pass
Review the completed change for:
- likely regressions;
- missing or weak verification;
- generated-artifact drift;
- docs or contract drift;
- boundary violations not caught earlier;
- any obvious mismatch between changed files and claimed scope.
- any redundancies;
- readability issues;

If subagents are warranted and permitted, spawn them here and wait for all results before continuing.

### 2. Assemble the final verification set
Choose the final commands across:
- package tests;
- generation or regeneration;
- formatting;
- linting;
- build or compile checks;
- repo-state checks such as `jj status`, `jj diff`, or other repository-state inspection needed for a clean handoff;

Prefer the narrowest sufficient set, but expand the set when:
- concurrency-sensitive behavior changed;
- request-role/service-role or runtime wiring changed;
- generated artifacts changed;
- migrations, privileges, or runtime-role boundaries changed;
- docs and code changed together in a way that raises drift risk.

### 3. Record gaps explicitly
Record:
- checks that were intentionally not run;
- why they were not run;
- any remaining review risk or uncertainty;
- any follow-up verification that should happen outside the current session.

### 4. Prepare handoff-ready conclusion
Return a concise final verification and review summary that can be used directly in the handoff.

## Output

Return:

- `review findings`
  - severity
  - file paths
  - issue or confirmation
- `commands run`
- `commands recommended but not run`
- `why this final verification set`
- `remaining risks or follow-ups`
