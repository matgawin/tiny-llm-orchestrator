# Attempt Time Left

## Command

`orc time-left` prints deadline guidance for one worker attempt. The command
uses the current project root and loads persisted run state from `.orc/runs`.
It computes the deadline from `status.json` attempt data:

```text
deadline = started_at + timeout
```

Default worker mode reads these environment variables:

- `ORC_RUN_ID`
- `ORC_ATTEMPT_ID`

If `ORC_STEP_ID` is set, `orc time-left` checks that it matches the persisted
attempt `step_id`. Without worker environment variables, pass explicit ids:

```bash
orc time-left --run <run-id> --attempt <attempt-id>
```

Human output includes `deadline`, `elapsed`, `remaining`, `timeout`, `phase`,
and `action`. It also prints `run`, `step`, `agent`, `attempt`, and
`started_at` for troubleshooting. `--json` prints the same fields as JSON for
hooks:

```bash
orc time-left --json
```

If neither worker environment nor `--run` and `--attempt` identify an attempt,
the command fails and names the required inputs.

## Worker Environment

Worker launches inject the attempt deadline environment for agent, command, and
script steps:

- `ORC_ATTEMPT_STARTED_AT`: attempt `started_at` as UTC RFC3339Nano.
- `ORC_ATTEMPT_DEADLINE`: `started_at + timeout` as UTC RFC3339Nano.
- `ORC_ATTEMPT_TIMEOUT`: attempt timeout as the Go duration string persisted in
  the Run Store, such as `30m0s`.

The values are derived from the persisted attempt. Orc does not add deadline
fields to the run-store schema.

## Phases

`orc time-left` computes phase from remaining time with fixed v1 thresholds:

| Phase | Condition | Action |
| --- | --- | --- |
| `NORMAL` | More than 10 minutes remain | Continue scoped work |
| `NARROW` | 10 minutes or less remain | Stop expanding scope |
| `WRAP_UP` | 5 minutes or less remain | Stop implementing new behavior and run at most one cheap check |
| `REPORT_NOW` | 2 minutes or less remain, or the deadline expired | Submit `orc report` now or report `blocked/blocked` now if blocked |

For timeouts shorter than a threshold, the same remaining-time checks apply.
The phase moves only toward higher urgency as the deadline approaches.

## Codex PostToolUse Hook Contract

A Codex PostToolUse hook can run:

```bash
orc time-left --json
```

The hook must stay quiet when `phase` is `NORMAL`. For `NARROW`, `WRAP_UP`, or
`REPORT_NOW`, the hook can inject the `action` string as a short directive to
the worker. Hooks must not create reports, change run state, advance workflows,
or synthesize successful outcomes.

Timeout handling stays unchanged. If a worker misses the deadline, Orc records
the existing timeout outcome path for that step.
