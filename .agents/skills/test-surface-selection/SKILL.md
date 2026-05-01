---
name: test-surface-selection
description: Must use for any behavior, contract, storage, runtime, or migration change to choose the minimal sufficient proof surface across package tests, integration tests, and race checks before final verification.
---

# test-surface-selection

Use this for any non-trivial behavior, contract runtime, or migration change before selecting verification commands for this repo.

This skill is for selecting the proof surface only. Use `verify-change` later for the final validation pass and handoff-ready command set.

## Must use when

- behavior, contracts, storage semantics, runtime wiring, privilege boundaries, or migration behavior changed;
- tests were added or modified to prove a real behavior change;
- you need to decide between package, integration, or race coverage.

## Do not use when

- the task is docs-only, comment-only, formatting-only, or a tiny refactor with no behavior or contract impact.

## Common paired skills

- `change-scope` first for non-trivial changes
- `local-mock-review` whenever local mocks, fakes, stubs, or seam doubles were added to package tests
- `verify-change` after proof selection

## Purpose

Choose the narrowest test surface that still proves the changed behavior with confidence, while enforcing repo testing policy:
- prefer package tests for pure deterministic logic;
- prefer integration coverage for repository semantics, runtime behavior, and external-runtime effects;
- reject mock-heavy package tests when the behavior should instead be proved by pure-helper extraction or real integration coverage.

## Classification (do first)

Classify the changed behavior as one or more of:

- pure helper or deterministic decision logic
- parser, validator, sorter, codec, or mapping logic
- runtime wiring or composition-root behavior
- external-side-effect cleanup or compensating cleanup path
- generated-surface change

If classification is unclear, call it out explicitly.

## Selection rules

### 1. Package tests

Use package tests when the changed behavior is:
- pure helper logic;
- deterministic decision logic;
- parsers, validators, codecs, mappers, or sort helpers;

Do not rely on package tests alone when the behavior depends on:
- runtime-role wiring;
- external runtime dependencies;
- watcher or background-loop coordination.

### 2. Integration tests

Use integration tests when the changed behavior involves:
- repository semantics;
- runtime wiring or composition-root ownership;
- external-runtime behavior;
- cleanup or compensating cleanup flows;
- durable side effects that matter more than call choreography.

Prefer integration tests over local doubles when package tests would need handwritten mocks for handler, service, or repository seams.

If the chosen proof surface requires final command selection across generation, formatting, build, or repo-state checks, hand off to `verify-change`.

### 3. Race checks

Add race checks when the change touches:
- shared mutable state;
- watchers;
- queues;
- background workers or readiness coordination.

### 4. Companion surfaces

Add companion surfaces when one layer alone is insufficient.

Common pairings:
- package tests + integration for extracted pure logic plus durable runtime effects

## Smell checks

Flag these as warnings or likely mis-selection:
- handwritten doubles for handler, service, or repository seams in package tests;
- mock-heavy orchestration tests where durable outcomes should be proved instead;
- fixed-sleep assertions for watchers or background loops;
- package-test-only proof for repository, auth, runtime wiring, or external-runtime behavior;

If local mocks or stubs were introduced to make the test writable, run `local-mock-review`.

## Optional subagent use

For broad or multi-surface changes, you may use up to 2 read-only subagents when the active runtime and user instructions permit delegation:

1. classify changed behavior and likely proof surfaces
2. identify integration, or any other companion surfaces

Wait for both and merge findings before final output.

## Output

Return:

- `classification`
- `recommended test surfaces`
- `minimum required commands`
- `additional recommended commands`
- `why these test surfaces`
- `test-smell warnings`
- `follow-on skills` (for example `local-mock-review` or `verify-change`)
