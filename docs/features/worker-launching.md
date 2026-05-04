# Worker Launching

## Purpose

Define how `orc worker launch-next <run-id>` starts and tracks a workflow-selected worker attempt.

## Audience

Contributors changing worker process launch, active-attempt state, no-report outcomes, or launcher-facing CLI behavior.

## Read This When

- You are changing `orc worker launch-next`.
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
orc worker launch-next <run-id>
```

The v1 public command has no flags. It launches only the workflow-selected step
for the run. `orc run next` remains read-only inspection and never launches a
worker.

## Launch Contract

The launcher loads project config, loads the run, and asks the workflow engine
for the next decision. A worker launches only for `select_step`. Terminal runs
and runs with an active attempt do not launch a replacement worker in the same
command.

Each launch creates a `starting` attempt before rendering the worker prompt.
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
attempt metadata that was persisted. The worker command is:

```bash
codex --ask-for-approval never exec --skip-git-repo-check -
```

The command runs from the project root, resolves `codex` from the effective
worker environment, and receives the rendered prompt on stdin. In the Nix
development shell, `codex` is the repo wrapper that adds the Beads directory
before invoking the underlying Codex binary.

## Logs

Worker stdout and stderr stream into one combined run-store `log` artifact while
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

## No-Report Outcomes

This slice does not implement valid report authority. Until `orc report`
exists, worker process completion is interpreted as a synthesized failed
outcome:

- exit code `0`: `failed/missing_report`
- nonzero exit: `failed/process_error`
- timeout before a valid report exists: `failed/timeout`

The launcher records these outcomes on the attempt. It does not apply workflow
retry policy or launch a retry attempt in this slice; synthesized failure
routing belongs to the follow-on workflow retry integration. Until that routing
exists, `launch-next` refuses to relaunch after a synthesized terminal outcome
so retry accounting cannot be bypassed manually.

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
