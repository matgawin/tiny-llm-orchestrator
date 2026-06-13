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

## Documentation writing rules

When this skill requires writing or editing durable docs, enforce these rules before handoff.

### 1. Make every durable claim concrete

- Anchor behavior claims with a route, command, config key, package path, migration name, role name, error, example, number, or explicit condition.
- Do not write universal filler such as "many teams", "common scenarios", "significant improvements", or "key considerations" unless the doc names the actual cases.
- If the code is the only verified source, describe the observed code behavior directly. Do not invent product rationale, metrics, adoption claims, or examples to make prose sound complete.
- Prefer domain-native wording over generic verbs: "wire", "route", "grant", "backfill", "regenerate", "cut over", "reject", "return". Avoid generic substitutions such as "leverage", "utilize", "facilitate", "streamline", "robust", or "comprehensive".

### 2. Write direct technical prose

- State the rule, behavior, or workflow directly. Avoid helper-assistant framing such as "Here is an overview", "There are a few things to consider", "It is important to note", or "This section will...".
- Use hedges only for real uncertainty. If an exception exists, name it: "This breaks when `X` is disabled", not "this generally works".
- Do not lead with strawman pivots like "This is not about X, it is about Y". Start with the actual rule or behavior.
- Do not add a closing summary that restates the preceding paragraph. End on the last substantive rule, example, or next action.

### 3. Use structure only when it carries information

- Use headings, tables, bullets, and numbered lists for reference material, checklists, commands, ordered workflows, or scannable policy. Do not impose a three-part structure on content that is not actually sequential or enumerable.
- Avoid topic sentence -> evidence -> restatement paragraphs. Give the claim and evidence. Skip the recap.
- Avoid perfectly symmetrical triples, repeated sentence openers, balanced parenthetical tradeoffs, and "either X or Y" binaries unless the code or workflow truly has exactly those branches.
- Let one paragraph do two related jobs when that is clearer than splitting into artificial one-idea paragraphs.

### 4. Normalize punctuation and register

- Prefer periods and commas. Use at most one em dash per roughly 300 words, and never use double-em-dash wrapping (`X — like this — Y`).
- Treat semicolons as errors in repo docs unless they separate list items that already contain commas.
- Use colons only after a complete clause, usually to introduce a list, command, or example. Avoid mid-sentence colon patterns like "The problem: X".
- Use straight quotes and apostrophes in technical docs unless a file already follows a different house style.
- Match the local doc register. Engineering docs should be specific, terse, and operational. They should not read like marketing copy, a tutorial transcript, or a polished status update.

### 5. Run a docs prose audit before output

Before finalizing a docs change, scan the edited text and fix every hit:

- banned or low-specificity vocabulary: "delve", "leverage" as a verb, "utilize", "robust", "comprehensive", "streamline", "significant", "notable", "pivotal", "nuanced", "foster", "facilitate".
- reflexive hedges: "often", "generally", "typically", "in many cases", "it is worth noting", "it is important to note".
- robotic transitions: "Furthermore", "Moreover", "Additionally", "This highlights", "This underscores", "As previously mentioned".
- rhetorical scaffolding: "turns out", "What X was Y", "The key insight", "The rule is", "not just X", "more X than Y", "between X and Y".
- punctuation tells: em-dash overuse, semicolons, mid-sentence colons, curly quotes.
- generic claims without anchors.
- paragraph closers that read like aphorisms or motivational taglines.

If a flagged phrase is required because it is an API field, quoted output, command text, or existing proper name, keep it and treat it as evidence rather than prose.

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

6. Apply the documentation writing rules.

   For every edited durable doc:
   - make claims concrete and source-backed.
   - remove AI-pattern filler, hedges, transitions, punctuation tells, and rhetorical scaffolding.
   - keep structure purposeful and local-register consistent.
   - preserve exact API names, SQL names, command text, quoted output, and proper nouns even when they match a banned phrase.

7. Suggest follow-on skills when needed.

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
