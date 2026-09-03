---
name: minimum
description: >
  Use the smallest correct solution for coding work. Question whether the task
  must exist, reuse repository code first, prefer the standard library, prefer
  native platform features, and add only the minimum code that works. Supports
  intensity levels: lite, full (default), and ultra. Use for coding tasks that
  write, add, refactor, fix, review, design, or choose dependencies. Also use
  when the user says "minimum", "be lazy", "lazy mode", "simplest
  solution", "minimal solution", "yagni", "do less", or "shortest path", or
  asks to reduce over-engineering, bloat, boilerplate, or unnecessary
  dependencies. Do not use for non-coding requests.
argument-hint: "[lite|full|ultra]"
---

# Minimum

Minimum is the repository minimality skill for coding work. It makes the
solution smaller only after the relevant code path is understood.

## Activation

Use this skill when its trigger applies in the current request. It creates no
repository state, hooks, or automatic subagent behavior. The default level is
`full`. The user can request `lite`, `full`, or `ultra`.

## Decision Ladder

Stop at the first rung that holds:

1. **Must this exist?** Skip speculative work. Say what was skipped.
2. **Does this repository already have it?** Reuse the helper, type, pattern, or command.
3. **Does the standard library do it?** Use the standard library.
4. **Does the platform do it?** Use the platform feature before custom code or a dependency.
5. **Does an existing dependency do it?** Use an installed dependency before you add a new one.
6. **Can it be one line?** Use one line.
7. **Only then:** write the minimum code that works.

Use the ladder after you read the task and the code it touches. Trace the real
flow first. When two rungs both work, use the higher rung.

For a bug fix, fix the root cause. A report can name only one symptom. Before
you edit a shared function, search its callers. Prefer one guard in the shared
path to duplicate guards at each caller.

## Rules

- No unrequested abstractions: no interface with one implementation, no factory for one product, no config for a value that never changes.
- No boilerplate and no scaffolding for later.
- Prefer deletion over addition.
- Prefer clear code over clever code.
- Use the fewest files that can contain the correct change.
- For a complex request, implement the smallest correct version and state what it does not cover.
- Two stdlib options, same size? Take the one that is correct on edge cases. Lazy means writing less code, not picking the weaker algorithm.
- Mark deliberate simplifications that cut a real corner with a known ceiling (global lock, O(n^2) scan, naive heuristic) with a `minimum:` comment naming the ceiling and upgrade path (`# minimum: global lock, per-account locks if throughput matters`).

## Intensity

| Level | What change |
|-------|------------|
| **lite** | Build what the user asked for, but name the smaller alternative in one line. User picks. |
| **full** | The ladder enforced. Stdlib and native first. Shortest diff, shortest explanation. Default. |
| **ultra** | Strong YAGNI. Deletion before addition. Implement the smallest useful change and question speculative parts. |

Example: "Add a cache for these API responses."
- lite: "Done, cache added. FYI: `functools.lru_cache` covers this in one line if you do not want to own a cache class."
- full: "`@lru_cache(maxsize=1000)` on the fetch function. Skipped custom cache class, add when lru_cache measurably falls short."
- ultra: "No cache until a profiler shows need. When it does: `@lru_cache`. Do not add a custom TTL cache until that fails."

## When NOT to be lazy

Never simplify away: input validation at trust boundaries, error handling
that prevents data loss, security measures, accessibility basics, anything
explicitly requested. When the user insists on the full version, build it. Do
not keep arguing.

Never reduce the reading step. The ladder shortens the solution, not the
analysis. Trace every file that the change touches before you pick a rung.

Follow repository test policy. Leave the narrowest proof required for changed
behavior.

## Boundaries

Minimum governs what you build. Use `simple-english` for repository prose.
Use all required repository skills before handoff.

The shortest path to done is the right path.
