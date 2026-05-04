# Test Strategy

## Purpose

Capture the repository's testing strategy with enough detail to guide refactors and new coverage decisions.

## Audience

Contributors deciding how to cover behavior changes or reduce test-smell in existing code.

## Read This When

- You are adding or modifying tests.
- You are deciding between pure-function tests, unit tests, and integration tests.
- You are reviewing code that introduces new seams, stubs, or mocks.

## Related Docs

- [local-test-workflows.md](local-test-workflows.md)
- [../contributing/coding-standards.md](../contributing/coding-standards.md)

## Testing Priorities

The repository prefers:

- direct tests of extracted pure helpers
- real filesystem fixtures for config-loading behavior
- package tests around deterministic validation and command behavior
- integration-style coverage when runtime behavior depends on real process, storage, or orchestration effects

The repository forbids:

- generated mocks
- wide shared mock packages
- orchestration tests that only replay scripted double behavior
- preserving awkward production seams just because the old tests depend on them

## What To Test At Each Layer

### Pure Helpers

Extract and test pure functions when the behavior is fundamentally:

- mapping
- parsing
- validation
- state-transition logic
- small decision tables

This is the preferred way to cover logic-heavy code without building large fake environments.

Common good targets:

- config validators
- workflow transition validation
- retry and status-transition rules
- path-safety helpers

### Package Behavior

Use package tests when the behavior needs real package wiring but not a full process:

- `internal/cli` command output and errors
- `internal/config` loading and validation
- config behavior backed by `internal/initconfig/scaffold/.orc`; see
  [../reference/configuration.md](../reference/configuration.md)

### Integration Behavior

Use integration-style tests when behavior depends on real collaborators:

- worker process launch and termination
- persisted run state
- cross-package orchestration flows

Do not replace these effects with broad mocks when a real test surface is practical.

## Test Double Policy

- Generated mocks: forbidden
- Shared mock packages: forbidden
- Repository mocks above the repository layer: forbidden
- Local stubs, spies, fakes, and handwritten mocks for production seams: forbidden

Preferred replacement patterns:

- extract deterministic decision logic into pure helpers and test those helpers directly
- use real files and temporary directories for config-loading behavior
- use CLI stream injection for command output instead of mocking writers
- use integration-style tests when runtime behavior is what actually matters
- use bounded executable shims or shell harnesses when the behavior under test is process launch, signal handling, PATH lookup, or stdio persistence
- simplify production seams when a behavior is otherwise hard to test cleanly

Practical reading of this policy:

- if a behavior can be expressed as mapping, validation, or status logic, extract it and test the pure function
- if a handler, service, or runtime path is only testable through handwritten doubles, that is a signal to refactor the production code or add integration coverage instead
- executable shims are acceptable only when they stand in for an external command at the process boundary and the assertion is about durable CLI/runtime behavior, not call choreography

## Change-Oriented Guidance

- CLI changes usually need focused `internal/cli` tests.
- Config schema or validation changes usually need `internal/config` tests and scaffold source updates when the valid config shape changes.
- Workflow semantics usually need deterministic package tests for transition and terminal-state behavior.
- Run-store changes should use real temporary project directories and assert persisted events, latest status, artifact refs, recovery, and malformed-state errors directly.
- Launcher changes should prefer real processes where practical and race checks for shared mutable state.
- Documentation-only changes usually need link/path review and a grep check for stale placeholders or dead references.

## Change Expectations

- When a test would require handwritten stubs or mocks, prefer extraction or integration coverage instead of adding the doubles.
- When a new test fails, do not weaken assertions to match buggy behavior unless the intended contract is explicitly changing. First determine whether the failure is a test bug or a production bug; if the production behavior is wrong, fix the code and keep the stronger assertion.

Review expectation:

- if a change touches durable behavior and adds no new tests, reviewers should expect a clear reason why existing coverage is still enough
