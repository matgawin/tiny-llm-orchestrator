---
name: cross-surface-impact
description: Use after `change-scope` when a task is anchored on a domain feature or entity docs, and test surfaces, even if the user did not phrase it as an entity change.
---

# cross-surface-impact

Use this after `change-scope` when a change is centered on a feature or entity.

This skill identifies which surfaces move together for that entity and where the change should be anchored.

## Must use when

Use this when:
- the task is described in terms of a feature or entity;
- the user asks to change behavior for a named domain area, even if they do not say "entity" or "feature";
- you need to ensure all ownership surfaces for that entity are updated together;
- you want to avoid partial updates across repository, docs, and tests.

Do not use this only when the task is purely technical and local to one surface with no feature or entity anchor.

## Common paired skills

- `change-scope` first
- `docs-sync` when durable behavior or contracts changed
- `test-surface-selection` for proof planning

## Steps

1. Identify the entity or feature.

2. Identify the primary owner surface.
   Determine where the change should be anchored:
   - runtime behavior change → `internal/...`
   - docs-first change → `docs/`

3. Enumerate all surfaces that normally move with this entity.

   For each, classify as:
   - required
   - confirm
   - unlikely

   Surfaces to consider:
   - feature docs under `docs/features/`
   - reference docs when contracts or routes change

4. Identify coupling and invariants.

   Call out:
   - contract coupling
   - runtime-role or privilege implications
   - generated artifacts tied to this entity
   - migration or cutover implications

5. Suggest follow-on skills.

   Recommend only what applies:
   - `docs-sync`
   - `test-surface-selection`

6. Keep blast-radius analysis brief.
   `change-scope` should already have been used first; stay focused on entity ownership surfaces and follow-on skills.

## Output

Return:

- `entity`
- `primary owner surface`
- `required surfaces`
- `surfaces to confirm`
- `unlikely surfaces`
- `coupling or invariants`
- `recommended next skills`
