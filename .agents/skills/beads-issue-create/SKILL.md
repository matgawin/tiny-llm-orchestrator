---
name: beads-issue-create
description: Use when creating, splitting, validating, or updating Beads issues in this repo. Guides project-local BEADS_DIR usage, existing issue review, noninteractive bd commands, issue shape, dependencies, validation, and handoff expectations.
---

# beads-issue-creation

Use this skill before creating or reshaping Beads issues for this repository.

## Must use when

- The user asks to create, split, refine, or validate Beads issues.
- You need to record follow-up work discovered during another task.
- You need to add dependencies, labels, acceptance criteria, notes, or design context to an issue.

## Workflow

1. Load local workflow context.

   ```bash
   BEADS_DIR=$PWD/../.beads bd prime
   ```

2. Inspect existing work before creating anything.

   Use the project-local database and prefer structured output when you need details:

   ```bash
   BEADS_DIR=$PWD/../.beads bd ready --json
   BEADS_DIR=$PWD/../.beads bd list
   BEADS_DIR=$PWD/../.beads bd list --json
   BEADS_DIR=$PWD/../.beads bd search "<keyword>"
   ```

   Check for duplicates, dependency relationships, label conventions, and whether an existing issue should be updated instead of creating a new one.

3. Shape the issue from existing repo patterns.

   Current project issues generally use:
   - priority `2` / `P2` for normal planned work;
   - type `feature` for durable product/runtime behavior and `task` for tooling, docs, or workflow chores;
   - labels such as `afk`, `hitl`, and `orc-v1` when they match the existing issue family;
   - descriptions with a short intent paragraph plus a `Required behavior:` list;
   - checkbox acceptance criteria that are observable and testable;
   - notes for spec lineage, workflow type, dependencies, human-review constraints, or scope exclusions.

4. Create or update noninteractively.

   Do not use `bd edit`. Use flags, stdin, or files:

   ```bash
   BEADS_DIR=$PWD/../.beads bd create \
     --title "Title" \
     --type feature \
     --priority 2 \
     --labels afk,orc-v1 \
     --description "Why this issue exists and what needs to be done." \
     --acceptance "- [ ] Given ..., when ..., then ..." \
     --notes "Spec lineage: ... Type: AFK. Depends on ..."
   ```

   For longer bodies, prefer `--body-file`, `--design-file`, or `--reason-file` over opening an editor.

5. Wire dependencies explicitly.

   Beads dependency direction is: `bd dep add <issue> <depends-on>`.

   ```bash
   BEADS_DIR=$PWD/../.beads bd dep add <new-issue-id> <blocker-id>
   BEADS_DIR=$PWD/../.beads bd dep cycles
   ```

   Use dependencies when work truly cannot start until the blocker is complete. Use notes for soft context.

6. Validate the result.

   After creating or updating, inspect what was written:

   ```bash
   BEADS_DIR=$PWD/../.beads bd show <issue-id>
   BEADS_DIR=$PWD/../.beads bd list --id <issue-id> --json
   ```

   If dependency changes were made, run `bd dep cycles`. If issue quality is uncertain, use `bd lint` or `bd doctor --check=conventions`.

7. Handoff clearly.

   Report:
   - created or updated issue IDs and titles;
   - dependency changes;
   - labels/type/priority chosen;
   - validation commands run;
   - any issue intentionally left unclosed.

## Guardrails

- Always set `BEADS_DIR=$PWD/../.beads` in this repo.
- Run `bd ready --json` before selecting new work.
- Claim work with `bd update <id> --claim` when starting implementation, not merely when creating follow-up issues.
- Never close issues unless the user explicitly asks or the current workflow instructions require it.
- Avoid creating broad catch-all issues; split by independently reviewable behavior and explicit acceptance criteria.
- Do not create automatic Beads from Tiny Orc runtime follow-up artifacts unless the human asks for Beads issue creation.
