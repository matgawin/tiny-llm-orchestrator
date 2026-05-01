---
name: jj-workflow
description: Use whenever workspace state, unrelated diffs, or stacked changes could affect verification or handoff. Summarize `jj` status, isolate task-relevant diffs, and clearly separate unrelated workspace changes.
---

# jj-workflow

Use this when inspecting pending repository work, describing diffs, or preparing a handoff or verification step that depends on current patch state.

This skill ensures that only task-relevant changes are summarized and unrelated workspace state is clearly separated.

## Must use when

Use this when:
- preparing a handoff or summary of current changes;
- reviewing pending work before verification;
- inspecting diffs in a workspace with multiple or stacked changes;
- confirming that only intended files are modified.

## Do not use when

- workspace state is irrelevant because the task is purely read-only and no handoff, verification, or diff summary is needed.

## Common paired skills

- `verify-change` when repo-state checks or final summaries depend on current patch state

## Steps

1. Inspect repository state.

   Use:
   - `jj status` to identify modified, added, or untracked files;
   - current change vs broader workspace context.

2. Identify task-relevant scope.

   Determine:
   - which files belong to the current task;
   - which files are unrelated or from other changes;
   - whether the workspace contains stacked or partial changes.

3. Inspect relevant diffs.

   Use:
   - `jj diff --git`

   Focus only on:
   - files relevant to the current task;
   - meaningful changes (avoid noise such as formatting-only unless relevant).

4. Separate unrelated work.

   Explicitly list:
   - files or changes not part of this task;
   - whether they appear to belong to another change or incomplete work.

   Do not:
   - merge unrelated changes into the task summary;
   - assume ownership of unrelated modifications.

5. Handle stacked or partial changes carefully.

   - describe only the current task’s intended changes;
   - avoid summarizing the entire stack unless explicitly required.

6. Preserve user workspace integrity.

   - do not revert, amend, or rewrite unrelated changes;
   - do not modify commit structure as part of this step.

7. Prepare handoff-ready summary.

   - produce a concise, task-scoped diff summary;
   - ensure it aligns with the intended change description;
   - if this is part of final validation, hand off to `verify-change`.

## Output

Return:

- `repo state`
  - summary of modified/untracked files
- `relevant diffs`
  - concise summary of task-related changes with file paths
- `unrelated work noticed`
  - files and brief explanation
- `handoff summary`
  - clear description of what this change does at a high level
