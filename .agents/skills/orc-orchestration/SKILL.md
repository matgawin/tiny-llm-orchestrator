---
name: orc-orchestration
description: Use when the user asks Codex to orchestrate Tiny Orc runs, execute Beads through with orc, manage Orc worker loops, or act as the Orc orchestrator without editing files directly.
---

# orc-orchestration

Use this skill when the main Codex thread is acting as the Tiny Orc
orchestrator. The orchestrator makes routing and supervision decisions, but does
not implement code changes directly.

## Must use when

- The user asks to run Bead work through Orc.
- The user says to use `orc`, or Tiny Orc to execute tasks.
- The user asks Codex to act as the Orc orchestrator.
- A running Orc workflow needs supervision, worker launch, status inspection, or
  handoff.

## Core stance

- The main thread is the orchestrator, not the worker.
- Do not edit task files directly while orchestrating.
- Let Orc select steps and launch only the selected step.
- Inspect repo state, Beads, Orc run state, prompts, logs, and reports as needed
  to make executive decisions, but not preemptively.
- If a worker makes changes, review status and reports, but do not repair the
  worker's code yourself unless the user explicitly exits orchestration mode.
- Do not close Beads unless explicitly asked by the user.

## Required companion skills

Use these when their triggers apply:

- `beads-issue-create`: when selecting, claiming, creating, updating, or
  validating Beads.
- `jj-workflow`: when checking workspace state, separating unrelated diffs, or
  preparing handoff.
- `verify-change`: before final handoff if the orchestration produced code,
  docs, config, or workflow changes and Orc did not already run an adequate
  final verification workflow.

## Workflow

1. Load Beads context and inspect ready work.

   ```bash
   BEADS_DIR=$PWD/../.beads bd prime
   BEADS_DIR=$PWD/../.beads bd ready --json
   jj status
   ```

   Use `jj status` before starting implementation workflows because many Orc
   workflows block dirty starts.

2. Select the next task.

   Prefer the top `bd ready --json` item unless the user gave a specific Bead.
   Inspect details before starting:

   ```bash
   BEADS_DIR=$PWD/../.beads bd show <issue-id>
   ```

   Claim work only when actually starting it:

   ```bash
   BEADS_DIR=$PWD/../.beads bd update <issue-id> --claim
   ```

3. Choose the workflow.

   Common choices:

   - `implementation`: feature/runtime/config/docs behavior work.
   - `bugfix`: a defect with a reproduce step.
   - `mechanical-change`: narrow mechanical edits or deterministic cleanup.
   - `test-only`: test-only work.
   - `review-*`: read-only review of existing dirty changes.

   Inspect workflow config if uncertain:

   ```bash
   sed -n '1,260p' .orc/workflows/<workflow>.yaml
   ```

4. Start the Orc run.

   For Bead-backed runs:

   ```bash
   BEADS_DIR=$PWD/../.beads orc run start --workflow <workflow> --bead <issue-id>
   ```

   For explicit task text:

   ```bash
   orc run start --workflow <workflow> --task "<markdown task>"
   ```

   If start fails because project config is invalid, inspect the cited workflow
   and report the blocker. Do not patch config unless the user explicitly asks.

5. Route by Orc state, not intuition.

   Before launching each step:

   ```bash
   orc run status <run-id>
   orc run next <run-id>
   ```

   Launch only when `run next` says a step is selected and not launched:

   ```bash
   orc worker launch-next <run-id>
   ```

   If worker launch fails because Codex session files or similar setup need
   writes outside the sandbox, rerun the same launch with escalated permission.

6. Poll long-running workers.

   Keep the launch command session open and poll until it exits. While a worker
   is active:

   - Do not start another worker in the same sequential run.
   - Do not edit files.
   - You may inspect `run status` and `jj status` to understand progress.
   - Avoid reading active diffs unless needed for routing; the worker owns them
     until it reports.

7. Handle outcomes.

   After each worker exits:

   ```bash
   orc run status <run-id>
   orc run next <run-id>
   ```

   Continue launching selected steps until Orc reports a terminal state:

   - `ready_for_human`: stop launching; prepare handoff.
   - `blocked_for_human`: inspect logs/reports and summarize the blocker.
   - `cancelled` or another terminal state: stop and summarize.

   If a report is `done/changes_requested`, `done/failed`, or equivalent, do
   not intervene manually. Let the workflow route to the configured coder/fixer
   step.

8. Inspect logs when needed.

   Run status shows log artifact paths. Open only the relevant log:

   ```bash
   sed -n '1,240p' .orc/runs/<run-id>/logs/<log-file>
   ```

   Use logs to distinguish environmental failures, blocks, and requested
   changes.

9. Final handoff.

   Collect:

   ```bash
   orc run status <run-id>
   orc run summary-context <run-id>
   jj status
   jj diff --stat
   ```

   Report:

   - run id and terminal state;
   - worker steps and outcomes;
   - changed paths;
   - verification commands reported by Orc;
   - remaining risks or blocked reasons;
   - Bead status, explicitly noting if it was not closed.

## Guardrails

- Use `orc` for Orc v1 in this repo unless the user specifies another
  binary.
- Keep `BEADS_DIR=$PWD/../.beads` on Beads commands.
- Use `jj`, not `git`.
- Do not edit `.orc/runs` directly.
- Do not launch workers after Orc says `ready_for_human; no more workers should
  launch`.
- Do not assume a worker's claimed verification passed; read the report or run
  explicitly requested checks.
- If an important check fails because of sandbox/cache/network restrictions,
  retry with the appropriate escalation request rather than treating it as a code
  failure.
- If a workflow design allows terminal readiness without required checks, report
  that as a workflow gap and ask whether to run a corrective workflow or update
  the workflow.

## Typical command loop

```bash
BEADS_DIR=$PWD/../.beads bd ready --json
BEADS_DIR=$PWD/../.beads bd update <issue-id> --claim
BEADS_DIR=$PWD/../.beads orc run start --workflow implementation --bead <issue-id>
orc run next <run-id>
orc worker launch-next <run-id>
orc run status <run-id>
```

Repeat `run next` and `worker launch-next` until terminal.
