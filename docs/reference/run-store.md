# Run Store Reference

## Purpose

Provide the v1 on-disk contract for durable run state under `.orc/runs`.

## Audience

Contributors who need exact file names, event fields, status fields, and artifact paths for run persistence.

## Read This When

- You are changing `internal/runstore`.
- You are wiring future CLI commands to persisted run state.
- You need to inspect a run directory by hand.

## Reader Task Pages

- [run-store-layout.md](run-store-layout.md): slug normalization, generated and explicit run IDs, directory layout, and filesystem safety
- [run-store-events.md](run-store-events.md): append-only event log rules, caller events, and v1 event payloads
- [run-store-status-artifacts.md](run-store-status-artifacts.md): `status.json` materialization, attempt history, artifact references, and artifact paths
- [run-store-operations.md](run-store-operations.md): operational locking and commit rules, API families, attempt lifecycle preconditions, and log append API

## Related Docs

- [configuration.md](configuration.md)
- [../architecture/system-overview.md](../architecture/system-overview.md)
