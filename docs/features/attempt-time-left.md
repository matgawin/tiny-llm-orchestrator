# Attempt Time Left

## Command

`orc time-left` prints deadline guidance for one worker attempt. The command
loads persisted run state from `.orc/runs`. Root lookup uses this order:

1. `--root <path>`
2. `ORC_PROJECT_ROOT`
3. current working directory

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

- `ORC_PROJECT_ROOT`: project root used by the launcher.
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

## Codex PostToolUse Hook

Codex hook installation is user-owned. Orc does not install or update Codex
hook files.

Put the hook script at this project-local path:

```text
.codex/hooks/orc_time_left_post_tool_use.sh
```

Add the Codex hook to `$CODEX_HOME/config.toml`. If `CODEX_HOME` is not set,
use `~/.codex/config.toml`.

```toml
[[hooks.PostToolUse]]
matcher = ".*"

[[hooks.PostToolUse.hooks]]
type = "command"
command = '''bash -lc 'if [ -n "${ORC_PROJECT_ROOT:-}" ]; then bash "$ORC_PROJECT_ROOT/.codex/hooks/orc_time_left_post_tool_use.sh"; fi' '''
timeout = 3
additionalContextLimit = 500
```

Use this script content:

```bash
#!/usr/bin/env bash
set -u

for name in \
  ORC_PROJECT_ROOT \
  ORC_RUN_ID \
  ORC_STEP_ID \
  ORC_ATTEMPT_ID \
  ORC_ATTEMPT_STARTED_AT \
  ORC_ATTEMPT_DEADLINE \
  ORC_ATTEMPT_TIMEOUT
do
  if [ -z "${!name:-}" ]; then
    exit 0
  fi
done

if ! output=$(cd "$ORC_PROJECT_ROOT" && orc time-left --json 2>/dev/null); then
  exit 0
fi

phase=$(printf '%s\n' "$output" | sed -n 's/.*"phase"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
remaining=$(printf '%s\n' "$output" | sed -n 's/.*"remaining"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

case "$phase" in
  NORMAL|"")
    exit 0
    ;;
  NARROW)
    action="stop expanding scope"
    ;;
  WRAP_UP)
    action="stop implementing new behavior and run at most one cheap check"
    ;;
  REPORT_NOW)
    action="submit orc report now or report blocked/blocked now if blocked"
    ;;
  *)
    action="inspect attempt deadline state"
    ;;
esac

printf '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"Orc deadline phase: %s. Time remaining: %s. %s."}}\n' \
  "$phase" "${remaining:-unknown}" "$action"
```

Codex command hooks run from the session cwd. The script uses
`ORC_PROJECT_ROOT` as the stable project root and runs `orc time-left --json`
from that directory.

The hook stays quiet outside Orc runtime, when any required Orc environment
variable is missing, for `NORMAL`, and for command failures. For `NARROW`,
`WRAP_UP`, or `REPORT_NOW`, the hook emits Codex hook JSON with
`hookSpecificOutput.additionalContext`.

The hook must not use `decision = "block"`, `continue = false`, stderr
feedback, `orc report`, workflow commands, or writes to `.orc/runs`.

Timeout handling stays unchanged. If a worker misses the deadline, Orc records
the existing timeout outcome path for that step.
