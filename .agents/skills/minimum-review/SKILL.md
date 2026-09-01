---
name: minimum-review
description: >
  Code review focused exclusively on over-engineering. Finds what to delete:
  reinvented standard library, unneeded dependencies, speculative abstractions,
  dead flexibility. One line per finding: location, what to cut, what replaces
  it. Use when the user says "review for over-engineering", "what can we
  delete", "is this over-engineered", "simplify review", or invokes
  /minimum-review. Complements correctness-focused review, this one only
  hunts complexity.
---

Review diffs for unnecessary complexity. One line per finding: location, what
to remove, and what replaces it. The best outcome is a smaller diff.

## Format

`L<line>: <tag> <what>. <replacement>.`, or `<file>:L<line>: ...` for
multi-file diffs.

Tags:

- `delete:` dead code, unused flexibility, speculative feature. Replacement: nothing.
- `stdlib:` hand-rolled thing the standard library ships. Name the function.
- `native:` dependency or code doing what the platform already does. Name the feature.
- `yagni:` abstraction with one implementation, config nobody sets, layer with one caller.
- `shrink:` same logic, fewer lines. Show the shorter form.

## Examples

Bad: "This EmailValidator class is more complex than necessary, have you
considered whether all these validation rules are needed at this stage?"

Good: `L12-38: stdlib: 27-line validator class. "@" in email, 1 line, real validation is the confirmation mail.`

Good: `L4: native: moment.js imported for one format call. Intl.DateTimeFormat, 0 deps.`

Good: `repo.py:L88: yagni: AbstractRepository with one implementation. Inline it until a second one exists.`

Good: `L52-71: delete: retry wrapper around an idempotent local call. Nothing replaces it.`

Good: `L30-44: shrink: manual loop builds dict. dict(zip(keys, values)), 1 line.`

## Scoring

End with the only metric that matters: `net: -<N> lines possible.`

If there is nothing to cut, say `Lean already. Ship.` and stop.

## Boundaries

Scope: over-engineering and complexity only. Correctness bugs, security holes,
and performance are explicitly out of scope. Route them to a normal review
pass, not this one. A single smoke test or `assert`-based
self-check is the minimum skill baseline, not bloat, never flag it for deletion.
This skill lists findings only. It does not apply fixes.
