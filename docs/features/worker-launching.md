# Worker Launching

## Purpose

Define how `orc run advance <run-id>` and `orc worker launch-next <run-id>`
start and track workflow-selected worker attempts.

## Audience

Contributors changing worker process launch, active-attempt state, no-report outcomes, or launcher-facing CLI behavior.

## Read This When

- You are changing `orc run advance` or `orc worker launch-next`.
- You need to know how launcher state is persisted.
- You are wiring report validation or retry routing to active attempts.

## Related Docs

- [worker-prompt-rendering.md](worker-prompt-rendering.md)
- [run-inspection.md](run-inspection.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../reference/workflow-engine.md](../reference/workflow-engine.md)
- [../reference/configuration.md](../reference/configuration.md)

## Command Shape

```bash
orc run advance <run-id> [--max-steps N] [--once] [--json]
orc worker launch-next <run-id>
```

For normal operator-driven execution, prefer `orc run advance <run-id>`. It is
a conservative loop around the same launcher path as `launch-next`: it
evaluates the next workflow action, launches the selected worker attempt, waits
for the launcher to finish supervising that attempt, records the resulting
outcome through the normal launcher/report path, and repeats until a stop
condition is reached. `orc worker launch-next <run-id>` remains the one-step
primitive with no flags; use it for deliberate manual routing or debugging one
attempt at a time. `orc run next` remains read-only inspection and never
launches a worker.

`orc run advance` defaults to `--max-steps 20`. The value must be a positive
integer, and the guard stops before launching another worker with stop reason
`max_steps_reached`. `--once` may be combined with `--max-steps`, but it still
limits the command to one launched worker attempt. No dry-run mode exists for
`advance` in v1; use `orc run next <run-id>` for inspection.

By default, `advance` prints concise human-readable progress and a final
summary. With `--json`, stdout contains one final JSON object with `run_id`,
`launched_attempts`, `final_status`, `final_decision`, `stop_reason`,
`exit_code`, and optional `error`; each launched attempt includes `step_id`,
`agent_id`, `attempt_id`, `status`, `result`, and `state` when known. Progress
and launcher diagnostics are written to stderr in JSON mode so stdout remains
machine-readable.

## Sandbox Inheritance

Workers are sandboxed by process inheritance when the top-level
Codex/orchestrator session was started with `orc sandbox run`. In that flow,
`orc sandbox run` starts the configured command inside bubblewrap, and worker
processes launched by `orc run advance <run-id>` or `orc worker launch-next
<run-id>` remain inside the same sandbox.

Repositories may opt in to an enforcement guard with
`sandbox.require_for_workers: true`. When enabled, both `orc run advance` and
`orc worker launch-next` refuse to launch unless `ORC_SANDBOX=1` is present and
`ORC_SANDBOX_ROOT` matches the current repository root. This guard is useful
for repositories that expect Codex yolo mode to be used only inside the Orc
bubblewrap wrapper. It is disabled by default so existing non-sandbox worker
workflows remain usable. Failure messages tell the operator to restart the
orchestrator with
`orc sandbox run`.

## Launch Contract

The launcher loads project config, loads the run, and asks the workflow engine
for the next decision. `select_step` and `retry_step` are executable decisions.
Terminal runs do not launch. Runs with an active attempt refuse a second worker
until that active attempt terminalizes or is recovered.

For `select_step` decisions, the launcher checks the workflow's effective loop
caps against the persisted workflow state-entry counters before starting the
worker. Disabled caps bypass this policy. A soft-cap hit at prospective count
`soft + 1` records one advisory event for that workflow state, prints a clear
warning, and still launches the selected worker. A hard-cap hit at prospective
count `hard + 1` records a hard-cap event, leaves the target state's persisted
count at `hard`, and moves the run to `blocked_for_human` with reason
`loop_hard_cap_reached` instead of starting another worker. Retry decisions and
terminal or human-handoff states do not trigger loop-cap enforcement.
After human review, `orc run continue <run-id> --allow-loop-cap` records a
one-shot override for the currently blocked target state and returns the run to
`running`. The next matching launch consumes that override, starts the selected
worker, and increments the target state's count to the previously blocked
prospective count. The override does not raise configured caps or reset loop
counters; a later hard-cap hit requires another explicit continue command.

For non-loop `blocked_for_human` runs, `orc run continue <run-id>
--resolve-block --reason <text>` records a human attestation that the external
blocker was resolved outside Orc and returns the same run to `running`. This
mode retries the same step that produced the blocked terminal outcome. It does
not skip the step, select an arbitrary next step, prove the blocker is fixed,
create a new run, clear retry lineage, or reset workflow-loop counters. The
next `orc run advance <run-id>` or `orc worker launch-next <run-id>` starts a
new attempt for that blocked step and clears the continuation marker so the old
blocked outcome is not re-consumed. The retry still records the normal
workflow-loop entry and count for selecting that step again, with the resolved
blocked attempt as the trigger.
Use `--allow-loop-cap`, not `--resolve-block`, when the run has an active
workflow-loop hard-cap block. Start a separate workflow when the run is not in
`blocked_for_human` or no terminal blocked attempt can be resolved.

`orc run advance` stops conservatively:

- `ready_for_human`: normal successful stop, exit code 0.
- `blocked_for_human`: workflow terminal human handoff, exit code 2.
- `worker_blocked`: a launched worker reported `blocked/*`, exit code 2.
- `worker_failed`: a launched worker reported `failed/*`, exit code 1.
- `loop_soft_cap`: the workflow soft loop cap was reached before another
  launch, exit code 2.
- `loop_hard_cap`: the workflow hard loop cap blocked the run before another
  launch, exit code 2.
- `max_steps_reached`: the max-step guard stopped before another launch, exit
  code 0.
- `active_attempt_exists`: the command started while a worker attempt was
  active, exit code 1.
- `error`: invalid state, invalid config, launcher error, or persistence error,
  exit code 1.

Reviewer `changes_requested` outcomes are ordinary workflow outcomes. If the
workflow routes back to code and no cap, block, failure, or guard stops the run,
`advance` continues through the routed code/test/review cycle. `advance` does
not call `orc run continue --allow-loop-cap`, resolve human blocks, skip steps,
write Beads comments, or close Beads issues.

Agent launches create a `starting` attempt before rendering the worker prompt.
The attempt becomes `active` only after process metadata is recorded. The
attempt records:

- run id
- step id
- agent id
- attempt id
- timeout
- report-exit grace
- prompt artifact reference when rendering succeeds
- process id when the process starts
- process start-time identity used to detect PID reuse on recovery
- log artifact reference when the durable log destination is created

The launcher renders the prompt through `internal/promptrender` using the same
attempt metadata that was persisted. Outside a verified Orc sandbox, the
default agent worker command is:

```bash
codex --ask-for-approval never exec --skip-git-repo-check -
```

When the repository has sandbox config and the launcher verifies
`ORC_SANDBOX=1` plus a canonical `ORC_SANDBOX_ROOT` matching the current
repository root, the default agent worker command is:

```bash
codex --dangerously-bypass-approvals-and-sandbox exec --skip-git-repo-check -
```

This sandbox-only yolo default relies on the inherited outer bubblewrap sandbox
as the isolation boundary. Explicit launcher command overrides are preserved
unchanged. Missing, invalid, or mismatched sandbox markers keep the normal
default unless `sandbox.require_for_workers: true` is enabled, in which case
the launch is refused before command selection.

The command runs from the project root, resolves `codex` from the effective
worker environment, and receives the rendered prompt on stdin. In the Nix
development shell, `codex` is the repo wrapper that adds the Beads directory
before invoking the underlying Codex binary.

Command and script steps use the same selected-step, active-attempt, retry,
timeout, and persisted status lifecycle, but they execute a deterministic local
foreground process instead of launching an LLM worker. `orc run next` remains
read-only for these steps and prints that the deterministic step was selected
without executing it. `orc run advance <run-id>` executes selected command or
script steps as part of its loop; `orc worker launch-next <run-id>` executes
one selected command or script.

Command steps pass `command.argv` directly to `exec` with no shell
interpretation. Script steps execute the configured repository-relative script
path plus args directly. Both kinds run from the repository root unless a
repo-relative `cwd` is configured, inherit the launcher environment, apply
configured `env` overrides, and run with closed stdin. They are bounded,
non-interactive foreground executions, not daemon, watcher, background-job, or
general async job-runner steps.

Orc writes command/script reports itself; subprocesses do not call
`orc report`. Exit mapping is fixed in v1:

- exit code 0: `done/passed`
- nonzero exit: `done/failed`
- workflow timeout: `failed/timeout`
- spawn or setup error: `failed/process_error`

The generated outcome must be declared in the step's `allowed_results`; normal
workflow evaluation rejects undeclared outcomes instead of silently routing.

## Logs

Agent worker stdout and stderr stream into one combined run-store `log` artifact while
the process runs:

- The launcher records the log artifact and links it from the starting attempt
  before process start.
- For log artifacts, `artifact.written` means the durable destination has been
  reserved; content continues to append until the worker exits or cleanup
  completes.
- The same artifact reference remains linked from the terminal attempt, so
  partial logs are durable context if the launcher exits before the worker
  finishes.
- After the attempt terminalizes, the log remains readable but is no longer
  appendable through the run-store streaming append API.

Command and script steps persist stdout and stderr as separate full `log`
artifacts whose names include the step id, attempt id, and stream identity.
Failure report summaries include bounded stream tails, labeled as stdout and
stderr. The default displayed tail is the last 100 lines or 12 KiB, whichever
is smaller; full streams remain in the run artifacts for inspection and later
prompt or summary context.

## No-Report Outcomes

Worker process completion is interpreted as a synthesized failed outcome when
no valid report has already terminalized the attempt:

- exit code `0`: `failed/missing_report`
- nonzero exit: `failed/process_error`
- timeout before a valid report exists: `failed/timeout`

The launcher records these outcomes on the attempt and feeds them through the
same workflow `status/result` routing model as reported outcomes. If retry
policy remains for the outcome pair, the next `launch-next` invocation records
retry routing metadata and starts the replacement attempt. If retries are
exhausted, that `launch-next` invocation applies the configured `on:`
transition, commonly `blocked_for_human`. The `attempt.started` routing fields
are specified in [run-store.md](../reference/run-store.md).

## Post-Report Process Cleanup

Valid reported outcomes remain authoritative for routing. If a valid report is
persisted while the worker process is still running, the launcher starts the
configured `report_exit_grace` timer from the persisted report observation. A
worker that keeps running beyond that grace is terminated as process-management
cleanup. If the worker exits nonzero after a valid report, the launcher records
a warning event with the exit code.

## Supervision

Process cleanup targets the worker process group, not only the direct child
process. The launcher starts workers in an owned process group and terminates
that group when the direct child exits, when the workflow timeout expires, or
when the parent context is canceled. This prevents wrapper-spawned descendants
from continuing after the launcher records a terminal attempt.

Parent context cancellation uses the same process-group cleanup path, but it is
not the same outcome as a workflow timeout. The current v1 launcher records
non-timeout cancellation through the generic process-error path. The public
`orc worker launch-next` command derives that launcher context from `SIGINT`
and `SIGTERM`, so interrupting the CLI reaches worker process-group cleanup.
If cancellation arrives after helper process metadata is recorded but before the
helper is released to exec the worker command, the launcher terminalizes the
attempt as canceled and does not release the worker exec.

## Platform Support

Process identity and restart recovery currently require Linux procfs. On
non-Linux platforms, the v1 launcher reports worker supervision as unsupported
instead of recording unverifiable process metadata.

## Restart Recovery

When `launch-next` finds a `starting` attempt without process metadata, it
refuses recovery while the attempt is still within its configured timeout. This
prevents a concurrent `launch-next` from terminalizing a legitimate in-flight
launcher between `attempt.started` and process metadata persistence.

If a PID-less `starting` attempt is older than its recorded timeout, the
launcher records recovery as `failed/process_error` with `exit_state=unknown`.
No replacement worker launches in that recovery command.

When `launch-next` finds an active attempt, it checks the recorded timeout
before treating a live process as authoritative. If `started_at + timeout` has
already passed, the launcher terminates the recorded process group and records
the attempt as recovered `failed/timeout`, preserving the log reference. If the
attempt has not expired, it checks the recorded process id and process
start-time identity when available. If both still match a live process, launch
is refused because v1 workflows allow only one active attempt per run.

If the active attempt cannot be verified, including when the PID exists but does
not match the recorded identity, the launcher records a deterministic recovery
outcome:

```text
failed/process_error
exit_state=unknown
```

The recovery terminalizes the active attempt and does not launch a replacement
worker in the same command.
