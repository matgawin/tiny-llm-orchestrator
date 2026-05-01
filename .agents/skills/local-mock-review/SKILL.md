---
name: local-mock-review
description: Must use whenever `_test.go` diffs add or expand local mocks, stubs, fakes, generated mocks, or mock-heavy seam tests. Check whether the test should instead use a pure helper, a narrower owned seam, or real integration coverage.
---

# local-mock-review

Use this when a change adds, edits, or depends on local mocks, fakes, stubs, generated mock packages, or handwritten seam doubles.

## Must use when

Use this skill when:
- a package test introduces a new fake, stub, or mock;
- a handler, service, or repository seam is mocked in a local package test;
- a test becomes writable only after introducing a test double;
- `_test.go` diffs add files, helpers, or packages named `mock`, `fake`, `stub`, or generated doubles;
- package tests replace handler with doubles.

## Common paired skills

- `test-surface-selection` for choosing the proof surface
- `verify-change` before handoff

## Review questions

1. What behavior is the test actually trying to prove?
2. Can that behavior be covered by extracting a pure helper?
3. Is the current seam too broad, suggesting ownership or boundary problems?
4. Should the behavior be proved through integration coverage instead?
5. Is the mock asserting implementation choreography instead of durable behavior?
6. Does this introduce local test infrastructure that the repo normally avoids?

## Preferred order of alternatives

Prefer, in order:
1. extract and test a pure helper;
2. narrow the seam to a real owner-local contract;
3. use real integration coverage;

## Reject by default

Treat these as likely anti-patterns unless clearly justified:
- handwritten doubles for handler, service, or repository seams in package tests;
- local mock packages created only to make orchestration tests writable;
- mocks that mirror production storage or runtime behavior;
- mocks used where container-backed or integration coverage is the normal proof surface;
- tests that mainly verify call choreography rather than durable outcomes.

## Acceptable exceptions

A local double may be acceptable only when:
- the test depends on an external service boundary that is expensive, nondeterministic, or otherwise impractical to exercise in normal integration coverage;
- extracting a pure helper would distort production design;
- integration coverage would be disproportionate for the risk;

## Output

Return:
- `finding`: allowed | questionable | reject
- `files`
- `why`
- `preferred alternative`
- `recommended test shape`
