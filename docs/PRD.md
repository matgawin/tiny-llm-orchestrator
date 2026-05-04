# PRD: Tiny LLM Orchestrator

## Problem Statement

The user wants one main Codex orchestrator agent to manage normal project work while the human acts as a supervisor. Today, the user manually coordinates planning, coding, testing, documentation, readability review, and code review sessions. That creates unnecessary context switching and makes larger tasks harder to manage.

The project should stay simple. The core need is not a dashboard, terminal multiplexer, or full autonomous project manager. The core need is a deterministic local control plane that lets an orchestrator delegate work to role-specific Codex worker processes, collect structured reports, advance a configured workflow, and stop cleanly when human review or judgment is needed.

The existing Bash-based prototype proves that sequential agent prompting can work, but shell orchestration is too brittle for durable state, structured reporting, retry handling, workflow transitions, and clear `blocked_for_human` run states.

## Goals

- Provide a Go CLI that acts as the deterministic control plane for Codex-based work.
- Let one long-running orchestrator agent supervise a task from start to ready-for-human-review.
- Launch role-specific worker agents as separate Codex CLI processes.
- Require workers to communicate through structured `orc report` calls instead of free-form log scraping.
- Store all run state in inspectable project-local files.
- Use configuration files for workflows and agent roles.
- Keep workflow transitions deterministic and owned by Go code, not by free-form agent judgment.
- Stop before destructive actions, ambiguous scope expansion, worker timeouts, repeated worker failures, or task closure.
- Keep v1 small enough to implement and dogfood quickly.

## Non-Goals

- The CLI does not replace the human supervisor.
- The CLI does not guarantee deterministic agent reasoning or code quality.
- The CLI does not close beads automatically.
- The CLI does not resolve conflicts automatically.
- The CLI does not revert unexpected changes automatically.
- The CLI does not need zellij integration in v1.
- The CLI does not need native Codex subagent integration in v1.
- The CLI does not need jj workspace creation or management in v1.
- The CLI does not need a web dashboard.

## Solution

Build a flake-packaged Go CLI named `orc`. The product name is Tiny LLM Orchestrator, shortened to Tiny Orc. Project-local config and runtime state live under `.orc`.

The orchestrator is the main long-running Codex process. It uses `orc` to start a run, inspect current state, launch the next workflow step, read structured worker reports, and write final ready-for-review summaries. The orchestrator may reason about the task and summarize results, but it does not own workflow routing.

The Go CLI owns deterministic behavior:

- project configuration loading
- workflow validation
- run directory layout
- append-only event persistence
- worker prompt rendering
- Codex worker process launching
- report validation
- retry policy
- workflow transitions
- `blocked_for_human` run-state handling
- ready-for-human-review terminal state

Worker agents are treated as useful but unreliable executors. A worker succeeds only when it provides a valid structured report. In the current worker-launching slice, if a worker exits without reporting or times out, the CLI records a synthesized outcome and parks it for later report/retry routing. The workflow's deterministic retry or `blocked_for_human` policy is applied by follow-on report-routing work.

Beads remains the preferred external issue tracker when available. The CLI may read bead context, but v1 does not write bead notes or close beads. If beads is unavailable or a run is started without a bead, the CLI uses explicit local Markdown task context. Task closure remains a manual human action after review.

## Determinism Model

The system is deterministic where software can be deterministic:

- workflow graph validation
- allowed statuses and results
- report schema validation
- transition selection
- retry exhaustion
- timeout handling
- run state persistence
- dirty-start policy
- terminal `blocked_for_human` and ready-for-review states

The system is not deterministic where LLMs are inherently not deterministic:

- worker reasoning
- code quality
- reviewer judgment
- task interpretation
- completeness of generated summaries

The design therefore treats each worker as an external process that must produce valid structured output. The workflow engine decides what happens next from that output.

## Target V1 Scope

Target V1 should implement the smallest useful orchestrator loop. The current
implementation is landing this surface incrementally; for example, the current
worker-launching slice records synthesized no-report outcomes, while `orc
report` and report/retry routing remain follow-on target V1 work.

- `orc init`
- `orc run start --workflow implementation --bead <id>`
- `orc run start --workflow implementation --bead <id> --fallback-task-file <path>`
- `orc run start --workflow implementation --task-file <path>`
- `orc run start --workflow implementation --task "..."`
- `orc run start --workflow implementation --task-stdin`
- `orc run start --workflow implementation` is reserved for later interactive human use
- `orc run status <run-id>`
- `orc run next <run-id>`
- `orc worker launch-next <run-id>`
- `orc run add-followup <run-id> --title "..." --details "..."`
- `orc report --run <run-id> ...`
- `orc run summary-context <run-id>`
- `orc run record-summary <run-id> --file <path>`
- project config under `.orc`
- runtime state under `.orc/runs`
- one built-in `implementation` workflow
- project-local role descriptors for planner, coder, tester, reviewer, and orchestrator
- direct filesystem report persistence
- append-only event log
- latest status materialization
- worker retry policy for synthesized and reported failure results
- worker timeout policy
- worker attempt ids
- sequential execution with one active worker per run
- ready-for-human-review final state
- blocked-for-human final state
- optional read-only bead context import
- local Markdown task context fallback
- follow-up task artifact under each run
- jj-first dirty working copy check
- final VCS summary

## Later Scope

These features are useful, but should not be required for the first useful version:

- zellij layout generation
- zellij live pane or tab control
- native Codex subagent descriptor compatibility
- symlinks into Codex's custom agent directories
- automatic jj workspace creation
- multiple built-in workflows such as planning and documentation
- localhost HTTP report server
- report delivery fallback from HTTP to direct file persistence
- expected-path enforcement beyond reporting unexpected changed paths
- richer manual inspection commands
- web or TUI dashboard

## User Stories

1. As a human supervisor, I want one orchestrator agent to manage normal task flow, so that I do not manually switch between role-specific sessions.

2. As a human supervisor, I want worker agents to run as separate Codex CLI processes, so that each role can perform focused work with its own prompt and context.

3. As a human supervisor, I want workflows to be configuration files, so that routine task flow is reviewable and adjustable without changing Go code.

4. As a human supervisor, I want workflow routing to be deterministic, so that failed tests, requested changes, worker-blocked reports, and success paths do not depend on ad hoc agent judgment.

5. As a human supervisor, I want worker output to be structured, so that the orchestrator and CLI can reason from validated reports instead of scraped logs.

6. As a human supervisor, I want the orchestrator to stop before closing a bead, so that I always perform final review before work is considered complete.

7. As a human supervisor, I want a ready-for-review summary, so that I can quickly inspect changes, tests, risks, and suggested review focus.

8. As an orchestrator agent, I want commands for starting runs, launching next steps, reading state, and recording summaries, so that I can supervise work without owning persistence or workflow logic.

9. As an orchestrator agent, I want `blocked_for_human` run states to be explicit, so that I can stop and tell the human exactly what needs attention.

10. As a worker agent, I want a simple `orc report` command, so that I can report status, result, summary, changed paths, and optional Markdown detail without knowing the run directory layout.

11. As a tester agent, I want to report blocked when tests require network access or approval, so that the orchestrator stops instead of entering an approval loop.

12. As a reviewer agent, I want to report approval, requested changes, or blocked findings, so that the workflow can route deterministically.

13. As a project maintainer, I want runtime state under ignored `.orc/runs`, so that generated orchestration artifacts do not pollute committed project config.

14. As a project maintainer, I want persistent config under `.orc`, so that workflows and agents are grouped in one predictable place.

15. As a project maintainer, I want dirty working copy detection before a run, so that unrelated pre-existing changes do not silently mix with agent work by default.

16. As a project maintainer, I want beads to remain external, so that existing issue tracking remains visible and explicit.

17. As a human supervisor, I want local Markdown task files to work when beads is unavailable, so that the orchestrator workflow is still usable in projects without beads.

18. As an orchestrator agent, I want worker reports to include attempt ids, so that stale or wrong-step reports cannot advance the workflow.

19. As a human supervisor, I want substantial follow-up work captured in a local artifact, so that scope expansion is visible even when the run is not bead-backed.

## Workflow Model

Workflows are config files that define steps, role assignments, allowed reports, retry policy, and deterministic transitions.

Example shape:

```yaml
name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  retries:
    failed/missing_report: 1
    failed/timeout: 0
    failed/invalid_report: 0
    failed/process_error: 1
    failed/error: 0
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/ready: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human

  code:
    agent: coder
    allowed_results:
      done: [ready]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/ready: test
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human

  test:
    agent: tester
    allowed_results:
      done: [passed, failed]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/passed: review
      done/failed: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human

  review:
    agent: reviewer
    allowed_results:
      done: [approved, changes_requested]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/approved: ready_for_human
      done/changes_requested: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human
```

The exact schema may change during implementation, but the invariant is fixed:
workers report facts; Go selects the next state.

Run statuses are:

- `running`: workflow has started and may launch or wait for worker attempts
- `ready_for_human`: workflow completed and needs human review
- `blocked_for_human`: workflow cannot continue without human decision or intervention
- `cancelled`: run was manually stopped

Worker report statuses are distinct from run statuses.

V1 workflows are sequential. Exactly one worker step may be active for a run at a time.

The workflow engine is the only authority for selecting the next step. `orc worker launch-next <run-id>` launches the currently selected runnable step. A debug command may allow launching a named step, but it must refuse to launch a step that is not currently selected unless an explicit force flag is provided.

## Current v1 Launcher Slice

The current implemented worker-launching slice creates a structured attempt id,
renders a worker prompt, supervises the worker process group, streams logs, and
records launcher-synthesized failed outcomes when no valid report exists yet:

- `failed/missing_report`
- `failed/process_error`
- `failed/timeout`

The launcher does not yet consume retry policy or launch replacement attempts
from those synthesized outcomes. It parks the terminal outcome so follow-on
report/routing work can apply retry accounting deterministically. The
operational attempt contract lives in
[worker-launching.md](features/worker-launching.md), and the durable event/status
shape lives in [run-store.md](reference/run-store.md).

## Report Model

`orc report` is the target report-authority model for the follow-on
report-routing slice. It is not exposed by the current public CLI. In the target
model, only the current active attempt can receive a valid report. Future retry
routing creates replacement attempts and records retry lineage without allowing
workers to choose the next step directly.

When implemented, `orc report` accepts structured fields and validates them
before updating run state.

Target required fields:

- run id
- step id
- agent id
- attempt id
- status
- result
- summary

Target optional fields:

- changed paths
- commands run
- tests run
- risks
- follow-up suggestions
- Markdown report file

Example target report payload:

```json
{
  "run_id": "20260501T120000Z-implementation-main-997-a1b2c3",
  "step": "test",
  "agent": "tester",
  "attempt": 2,
  "status": "done",
  "result": "failed",
  "summary": "Unit tests fail because punctuation is not stripped before palindrome comparison.",
  "changed_paths": [],
  "tests": ["pytest"],
  "followups": [
    {
      "title": "Add integration coverage for attempt recovery",
      "details": "Current work only covers unit-level recovery behavior."
    }
  ],
  "report_path": "reports/000004-tester.md"
}
```

Target report artifact paths are assigned by the run store and use
sequence-prefixed filenames. Reports are one-way: a worker report never directly
chooses the next worker. The workflow engine chooses the next step from the
validated `(step, status, result)` tuple.

Target validation rules:

- Reports are accepted only for the current active worker attempt.
- The report must match the active run id, expected step id, expected agent id,
  and expected attempt id.
- Malformed or schema-invalid reports for the current active attempt terminalize
  that attempt as `failed/invalid_report`.
- Reports for stale attempts, wrong steps, wrong agents, or future steps are
  recorded as ignored invalid report events.
- Ignored invalid report events do not change the active attempt state, consume
  retries, or advance the workflow.

The global status enum is:

- `done`
- `blocked`
- `failed`

Each workflow step defines the allowed result values for each status. Invalid
status/result pairs are rejected and recorded as invalid reports. Worker report
status is distinct from run terminal state. Workers may report `status=blocked`
and `result=blocked`; the workflow may route that outcome to the run terminal
state `blocked_for_human`.

Target report authority:

- Workers never write directly into `.orc/runs`; they call `orc report`.
- A valid report is terminal for the worker attempt.
- If a valid report exists, the report controls workflow routing even when the
  worker later exits nonzero.
- If a valid report exists but the worker keeps running past
  `report_exit_grace`, the launcher terminates the process and the valid report
  still controls routing.
- No-report outcomes, invalid reports, timeouts, and process errors feed retry
  policy in the report-routing slice.

Target retry policy is keyed by reported or synthesized `(status, result)` pairs
and scoped to a step execution lineage. The relevant pair's count increments
when the same step is retried. Counts reset when the workflow applies an `on:`
transition and later enters the step through normal routing, even when that
transition targets the same step.

## Task Context Model

Runs may use bead context, local Markdown task context, or an inline task prompt captured by the CLI.

Beads are preferred when available because they are the project's external issue tracker. A run may start with `--bead <id>`, in which case the CLI imports read-only bead context into the run store. The orchestrator does not write bead notes in v1. Ready-for-review summaries may include suggested bead notes for the human to apply manually.

If a bead id is explicitly provided and cannot be read, the run fails unless an explicit fallback task source is also provided, such as `--fallback-task-file <path>`. If no bead id is provided and beads are unavailable, the CLI uses explicit local Markdown task context.

When beads are unavailable or the user does not want a bead-backed run, a run may start with `--task-file <path>`. The CLI copies or snapshots that Markdown task file into the run store as the task context. Markdown task context is local to the run and does not imply any external issue lifecycle.

When no bead id or task file is provided, the CLI may create local Markdown task context from `--task` or `--task-stdin`, then snapshot that context into the run store. Interactive editor prompting is reserved for a later slice. In noninteractive mode, `run start` must receive `--bead`, `--task-file`, `--task`, or `--task-stdin`; it must not open an editor or prompt.

Orchestrator usage must use noninteractive task input. Plain `orc run start --workflow implementation` is reserved for humans and fails until interactive prompting is implemented.

Workflows may declare whether task context is required and whether beads are required, optional, or disabled. The built-in implementation workflow should allow bead context, Markdown task-file context, or CLI-created local Markdown task context.

## Follow-Up Task Model

Substantial new findings should not silently expand a run. When the orchestrator or a worker identifies follow-up work that is outside the current task, it records the proposed work in `.orc/runs/<run-id>/followups.md`.

If a valid worker report includes follow-up suggestions, `orc report` appends them to `followups.md`. The orchestrator can also record follow-ups directly with `orc run add-followup <run-id> --title "..." --details "..."`.

Each follow-up requires a `title`; `details` is optional.

If the run is bead-backed, the ready-for-review summary may suggest creating beads from those follow-ups, but v1 does not create or close beads automatically. If the run is Markdown-backed, `followups.md` is the local follow-up artifact.

## Implementation Decisions

- The CLI is implemented in Go.
- V1 distribution is through a Nix flake.
- The CLI is primarily used by the orchestrator after project initialization.
- Persistent project config lives under `.orc`.
- Runtime run state lives under `.orc/runs`.
- `.orc/runs` is ignored by VCS.
- V1 includes one built-in implementation workflow.
- Additional workflows are added after dogfooding the implementation workflow.
- Role descriptors live under `.orc/agents`.
- Role descriptors are project-local and user-owned.
- Codex custom agent descriptor compatibility is deferred.
- Native Codex subagents are deferred.
- Zellij integration is deferred.
- jj workspace management is deferred.
- The orchestrator is a hybrid system: Go owns control flow and persistence; the orchestrator agent owns task-level reasoning and human-facing summaries.
- Worker roles run as separate Codex CLI processes.
- Worker prompts must include the exact `orc report` command contract.
- V1 runs are sequential. Exactly one worker may be active for a run at a time.
- Workers are launched with `orc worker launch-next <run-id>` so the CLI, not the orchestrator, selects the runnable step.
- Each worker launch creates an attempt id that reports must include; the attempt is `starting` until process metadata is recorded.
- A valid worker report is terminal for the active attempt; after a short grace period, any still-running worker process is terminated.
- If a worker exits without a valid report, the launcher records a synthesized failed outcome; retry application is deferred to report-routing work.
- If a worker exceeds its configured timeout without a valid report, the launcher terminates it and records a synthesized failed timeout outcome; retry application is deferred to report-routing work.
- Retry counters are keyed by `status/result`, scoped to a step execution lineage, and reset whenever the workflow applies an `on:` transition, even when that transition targets the same step.
- If retries are exhausted, the run enters `blocked_for_human` unless the workflow routes the exhausted outcome elsewhere.
- If tests require network or approval, the worker reports blocked and the run stops.
- Direct filesystem report persistence is the v1 transport.
- A localhost HTTP report server is deferred until direct reports are proven insufficient.
- Logs are debugging artifacts and fallback evidence, not the primary integration surface.
- After the VCS/dirty-start slice, dirty working copy at start stops the run by default unless the workflow explicitly allows it.
- VCS inspection belongs to the VCS/dirty-start slice and prefers jj, then git, then no VCS.
- The VCS/dirty-start slice records pre-run and post-run VCS summaries.
- The VCS/dirty-start slice flags unexpected changed paths without reverting them.
- Beads remains external.
- Beads are optional in v1.
- The CLI may import read-only bead context when a bead id is provided.
- If an explicit bead id cannot be read, the run fails unless an explicit fallback task source is provided.
- The CLI supports local Markdown task context when beads are unavailable or not used.
- If no bead id or task file is provided, the CLI creates local Markdown task context from `--task` or `--task-stdin` before starting the run; interactive prompting is reserved for a later slice.
- Noninteractive run start must not prompt; it requires bead context or an explicit task source.
- The orchestrator does not write bead notes in v1.
- Ready-for-review summaries may include suggested bead notes for the human to apply manually.
- Worker agents may read bead context but may not update or close beads.
- The human closes beads directly after manual review.
- The orchestrator stops for destructive commands, invalid config, missing required task context, expensive architecture ambiguity, repeated worker failure, and scope expansion that should become separate task work.
- Follow-up work outside the current task is recorded in `.orc/runs/<run-id>/followups.md`.
- Valid worker reports with follow-up suggestions append to `followups.md`; the orchestrator may also use `orc run add-followup`.
- Workflow and agent config are user-owned. The orchestrator may propose changes but must not edit them during normal task execution.
- `orc init` supports `--yes` and `--dry-run`.
- `orc init` asks before creating or updating existing project instruction files.

## Module Design

- **Name**: Config Loader
- **Responsibility**: Load and validate project configuration, workflow definitions, role descriptors, and defaults.
- **Interface**: Accepts a project root and returns validated config objects, workflow graphs, agent descriptor metadata, and diagnostics for missing or invalid references.
- **Tested**: yes

- **Name**: Run Store
- **Responsibility**: Own durable run state under `.orc/runs`.
- **Interface**: Creates run directories, writes append-only events, writes latest status files, stores Markdown reports, stores follow-up tasks, stores logs, stores snapshots, and records rendered prompts. Returns structured run state to callers.
- **Tested**: yes

- **Name**: Workflow Engine
- **Responsibility**: Execute deterministic workflow state transitions.
- **Interface**: Accepts a workflow definition and current run state, evaluates retry policy before terminal transitions, then returns the next step, terminal ready-for-review state, blocked-for-human state, or validation error. Handles retry policy, step-lineage retry counters, and legal status/result transitions.
- **Tested**: yes

- **Name**: Worker Launcher
- **Responsibility**: Start Codex CLI worker processes.
- **Interface**: Current v1 public surface is `orc worker launch-next <run-id>`. It launches only the workflow-selected next step, creates an attempt id, renders the prompt, records prompt/log/process metadata, supervises the worker process group, and records launcher-synthesized terminal outcomes. Sandbox flags, additional readable-directory flags, and retry outcome routing are future report/routing surfaces rather than current launcher inputs.
- **Tested**: targeted integration coverage if practical

- **Name**: Report Command (future)
- **Responsibility**: Validate and persist structured worker reports in the follow-on report-routing slice.
- **Interface**: Not exposed by the current public CLI. The target surface accepts report fields from CLI flags or a JSON file, validates required fields, active attempt identity, and allowed status/result values, writes through the Run Store, and updates latest state.
- **Tested**: no; current tests cover launcher-synthesized no-report outcomes only.

- **Name**: Beads Context Reader
- **Responsibility**: Import optional read-only bead context for a run.
- **Interface**: Accepts a bead id and beads directory, runs read-only bead context retrieval, writes task context into the run store, and reports clear failure if an explicitly requested bead is unavailable or inaccessible.
- **Tested**: command construction and failure handling

- **Name**: Markdown Task Context Reader
- **Responsibility**: Import local Markdown task context for a run when beads are unavailable or not used.
- **Interface**: Accepts a Markdown task file path or CLI-created Markdown task content, validates that it is readable, snapshots its contents into the run store, and records the source path when one exists.
- **Tested**: yes

- **Name**: VCS Inspector
- **Responsibility**: Inspect working copy state before and after runs.
- **Interface**: Detects jj first, git second, or no VCS. Reports dirty status, captures pre/post summaries, and identifies observed changed paths.
- **Tested**: parser behavior and no-VCS behavior

- **Name**: Init Scaffolder
- **Responsibility**: Create initial project orchestration files.
- **Interface**: Creates config, the implementation workflow, role descriptors, ignored runtime directories, and optional project instruction updates. Supports `--yes` and `--dry-run`.
- **Tested**: dry-run planning and idempotency

- **Name**: Orchestrator Prompt Runtime
- **Responsibility**: Bridge deterministic run state and orchestrator-agent reasoning.
- **Interface**: Renders compact state updates and summary context for the orchestrator from current run state, report summaries, workflow context, task context, and VCS summaries. Records orchestrator-authored summaries through the Run Store.
- **Tested**: template rendering

- **Name**: Follow-Up Task Recorder
- **Responsibility**: Persist substantial out-of-scope findings without expanding the active run.
- **Interface**: Appends structured Markdown entries from valid reports or `orc run add-followup` to `.orc/runs/<run-id>/followups.md` and exposes them in summary context.
- **Tested**: yes

## Testing Decisions

- V1 tests prioritize externally observable CLI behavior, deterministic workflow
  routing, durable run-store state, and process supervision outcomes.
- Tests should verify persisted artifacts and user-visible results rather than
  internal implementation choreography.
- Detailed package commands, race triggers, and local workflow guidance live in
  [testing/local-test-workflows.md](testing/local-test-workflows.md) and
  [testing/strategy.md](testing/strategy.md).

## Out of Scope For V1

- Replacing beads or wrapping the full bead lifecycle.
- Automatically closing beads.
- MCP integration.
- Native Codex subagent execution.
- Codex custom agent symlink management.
- zellij layout rendering.
- zellij pane or tab control.
- Running multiple orchestrators in one checkout.
- Automatically creating jj workspaces.
- Requiring beads for every run.
- Automatically resolving conflicts.
- Automatically reverting unexpected changes.
- Remote or multi-machine orchestration.
- A web dashboard.
- A Go plugin system for workflows.
- Non-flake distribution.
- Migrating the current Bash prototype directly.
- Editing workflow or agent configuration automatically during a normal run.

## Open Questions

- **Owner**: User. **Question**: What exact final-summary format is most useful during manual review? **Suggested resolution path**: Start with sections for changes, tests, VCS summary, suggested task-system notes, risks, follow-ups, and review checklist.

- **Owner**: User. **Question**: Should `orc run next` remain inspect-only, or should it optionally launch the selected step? **Suggested resolution path**: Keep `run next` inspect-only in v1. `worker launch-next` performs launch. Add a convenience `run advance` command later if needed.

- **Owner**: User. **Question**: Should a later version support explicit bead note writing? **Suggested resolution path**: Keep v1 read-only. Reconsider note writing after the read-only workflow is stable.

## Further Notes

- The desired next step after this PRD is to create beads issues/tasks from the v1 scope.
- The system should bias toward boring, inspectable local state over hidden agent-to-agent conversation.
- Filesystem persistence is the primary reliability mechanism in v1.
- The user wants reduced cognitive load, not a fully opaque autonomous system. The orchestrator should make routine progress alone but stop cleanly when human judgment is needed.
