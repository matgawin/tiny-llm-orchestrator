# Features

## Purpose

Index the durable behavior docs for the repository's main feature areas.

## Audience

Contributors documenting or changing user-visible or orchestration-facing behavior.

## Read This When

- You need stable behavior docs rather than implementation wiring docs.
- You need the canonical durable doc for a product-facing behavior area.

## Related Docs

- [../architecture/README.md](../architecture/README.md)
- [../operations/README.md](../operations/README.md)
- [../reference/README.md](../reference/README.md)

## Current Feature Areas

- Run start, task context capture, and dirty-start VCS policy: [run-start.md](run-start.md)
- Run inspection commands and config snapshot inspection: [run-inspection.md](run-inspection.md)
- Summary context rendering: [summary-context.md](summary-context.md)
- Final summary recording: [final-summary-recording.md](final-summary-recording.md)
- Follow-up recording: [follow-up-recording.md](follow-up-recording.md)
- Worker report command and report persistence: report contract in [worker-prompt-rendering.md](worker-prompt-rendering.md#report-contract), persistence contract in
  [../reference/run-store-events.md](../reference/run-store-events.md#v1-event-types)
- Live worker progress: [live-worker-progress.md](live-worker-progress.md)
- Worker prompt rendering: [worker-prompt-rendering.md](worker-prompt-rendering.md)
- Worker launching: [worker-launching.md](worker-launching.md)
- Sandbox command execution: [sandbox-run.md](sandbox-run.md)
- Run-store behavior: [../reference/run-store.md](../reference/run-store.md)
- CLI command behavior: [../../README.md](../../README.md) and `internal/cli`
- `.orc` config schema and validation: [../reference/configuration-project.md](../reference/configuration-project.md)
- Workflow behavior: package boundaries in [../architecture/system-overview.md](../architecture/system-overview.md). Workflow file schema lives in [../reference/configuration-workflows.md](../reference/configuration-workflows.md); transition rules live in [../reference/workflow-engine.md](../reference/workflow-engine.md).

Add a dedicated feature doc here when another behavior area grows beyond what belongs in the root README or reference docs.
