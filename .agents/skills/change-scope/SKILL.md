---
name: change-scope
description: Default first skill for non-trivial runtime, architecture, or durable-doc changes. Classify blast radius, identify required downstream skills, decide whether read-only subagents are needed, and produce the minimal docs, generation, and verification plan before editing.
---

# change-scope

Use this first for non-trivial runtime, architecture, or durable-doc changes, or whenever you need a scope report before editing.

This is the entry-point skill for non-trivial work. Its job is to classify the change, identify the smallest correct workflow, and route to narrower skills.

## Must use when

Use this when:
- the task changes runtime behavior, architecture, or durable docs;
- the task changes generated inputs, or verification shape for real behavior changes;
- the correct ownership boundary is not obvious;
- the task may cross multiple major surfaces;
- you need to know which skills, docs, generators, and verification paths apply before editing.

Do not use this only for:
- tiny, clearly local edits that stay within one package and do not affect behavior, contracts, generated artifacts, or durable docs;
- read-only inspection or explanation tasks with no planned edits.

If the change is clearly entity-centered, route next to `cross-surface-impact` and keep the scope report minimal instead of expanding into broad change-type analysis.

## Common paired skills

- `cross-surface-impact` for feature or entity-centered work that may span surfaces
- `docs-sync` for durable behavior, contract, or workflow changes
- `test-surface-selection` for behavior and verification planning

## Steps

1. Classify the primary change type.
   Choose one or more:
   - runtime wiring
   - service behavior
   - docs only
   - testing only
   - mixed

2. Identify affected technical boundaries.
   Choose all that apply:
   - composition root
   - generated code
   - permanent docs
   - tests
   - runtime-role or privilege boundary

3. Identify the smallest owner area where the work should begin.
   Prefer the narrowest relevant subtree and call out when work should start from:
   - `internal/`
   - `docs/`
   - `tests/`
   - or another narrower local owner path

4. Route to the next required skill(s).
   Recommend only the skills that actually apply.
   Typical routing:
   - feature/entity spanning multiple surfaces -> `cross-surface-impact`
   - runtime/composition-root seam changes -> `test-surface-selection` and `docs-sync` when durable behavior or workflow docs change
   - durable behavior or contract changes -> `docs-sync`
   - behavior-change verification planning -> `test-surface-selection`

5. Identify docs, generators, and verification categories.
   List only what is likely needed:
   - permanent docs to read or update
   - generators or regeneration workflows
   - verification categories such as package tests, integration tests, formatting, lint, build, or repo-state checks

6. Call out hidden blast radius.
   Look for:
   - runtime-role or service-role wiring implications
   - cutover constraints
   - generated artifacts that must stay in sync
   - cross-service effects
   - docs or contract drift risk
   - test-surface expansion beyond the obvious package

7. Decide whether read-only subagents are warranted and permitted.
   Use read-only subagents only when the active runtime and user instructions permit delegation, and parallel analysis will reduce main-thread noise.
   Consider them when at least one of these is true:
   - the task crosses more than one major surface;
   - ownership is unclear;
   - verification/doc impact is non-obvious;
   - the change is mixed and likely to produce noisy exploration.

   If subagents are permitted and useful:
   - spawn at most 5;
   - keep scopes disjoint;
   - prefer read-only analysis;
   - use lanes such as:
     1. code-path / ownership impact
     2. verification / test impact
     3. docs / contracts
   - always wait for all results before doing any work.

8. Produce the minimal execution plan.
   Return the smallest correct next-step plan rather than an exhaustive map.

## Output

Return a short scope report with:

- `change type`
- `affected boundary types`
- `owner area to start from`
- `recommended next skills`
- `docs to read or update`
- `generation needed`
- `verification categories`
- `hidden blast radius`
- `subagents needed`
