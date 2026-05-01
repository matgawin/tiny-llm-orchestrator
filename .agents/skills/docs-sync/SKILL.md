---
name: docs-sync
description: Must use whenever code, workflow, test policy, or architecture changes may make durable docs, examples, or repo guidance stale. Identify the canonical documentation source, required updates, index changes, and duplicated statements to remove.
---

# docs-sync

Use this whenever a code or docs change affects durable behavior, architecture, workflow, configuration, testing policy, migration rules, or canonical repository guidance.

This skill ensures there is a single canonical source of truth and that all related documentation stays consistent.

## Must use when

Use this when:
- behavior visible to users or operators changes;
- schemas, or contracts change;
- migrations, or runtime-role behavior change;
- workflow, testing strategy, or repo rules change;
- a change would make any existing doc statement stale.

Common cues:
- changed paths under `docs/`, `internal/`
- changed examples, schemas, workflow commands, or architecture rules;
- code changes that would otherwise leave a README, feature doc, reference doc, or example inaccurate.

## Do not use when

- the task is purely local formatting, renaming, or refactoring with no durable behavior, contract, workflow, or policy impact.

## Common paired skills

- `change-scope` first for non-trivial changes
- `verify-change` for the final consistency check

## Steps

1. Classify the durable change.

   Identify what actually changed:
   - runtime behavior
   - architecture or boundaries
   - workflow or testing policy
   - configuration surface

2. Identify the canonical source of truth.

   Choose exactly one:
   - `docs/features/...` for feature behavior
   - `docs/architecture/...` for system or boundary rules
   - `docs/reference/...` for contracts, configuration, or schemas
   - `docs/contributing/...` for workflow and policy
   - subsystem `README.md` when that tree owns the behavior

3. Identify all dependent surfaces.

   List any locations that must stay consistent:
   - category index pages (e.g. `docs/README.md`, sub-READMEs)
   - related feature or reference docs
   - inline comments or examples that mirror behavior

4. Detect drift and duplication.

   For each related surface:
   - mark as:
     - update
     - remove
     - confirm unchanged

   Prefer:
   - updating the canonical doc,
   - removing duplicated or stale statements,
   - avoiding adding new parallel descriptions.

5. Check contract and generation alignment.

   If applicable:
   - ensure examples reflect actual behavior;
   - call out when regeneration must happen alongside doc updates.

6. Suggest follow-on skills when needed.

   Only when applicable:
   - `verify-change` for final consistency check

## Output

Return:

- `canonical doc`
- `docs to update`
- `index updates`
- `docs to remove or consolidate`
- `contract or example drift`
- `follow-on skills`

If no doc update is required, return:

- `no-doc-update reason`
