# Live Worker Progress

## Purpose

Define the v1 worker-authored live progress contract for operator feedback
while Orc supervises a workflow run.

## Audience

Contributors changing progress transport, worker launch supervision, CLI
output, worker prompts, or run persistence boundaries.

## Read This When

- You are implementing or reviewing `orc progress`.
- You are changing `orc run advance` or `orc worker launch-next` supervision
  output.
- You need to distinguish live worker progress from final worker reports.
- You are checking whether progress data may be persisted or routed.

## Related Docs

- [worker-launching.md](worker-launching.md)
- [worker-prompt-rendering.md](worker-prompt-rendering.md)
- [../reference/run-store.md](../reference/run-store.md)
- [../reference/workflow-engine.md](../reference/workflow-engine.md)
- [../future-work.md](../future-work.md)

## Contract Summary

Workers may send live, non-persistent progress messages with:

```bash
orc progress <message>
```

`orc progress` is the only v1 worker-authored live progress command. It is a
separate operator-feedback surface from final worker reports. It must not be
modeled as `orc report status`, `orc report --status`, or any other report
variant because `orc report --status/--result` belongs to final worker
outcomes.

Live progress messages are not workflow outcomes. They do not satisfy report
requirements, affect workflow routing, change retry behavior, alter loop-cap
behavior, write Beads state, contribute to summary context, or appear in final
advance summaries. They also do not change active-attempt handling or the
requirement that a worker eventually submits a final report.

`orc run next <run-id>` is inspect-only and never creates a listener or
launches a worker. `orc run advance <run-id>` supervises one or more selected
attempts and owns a listener for each active attempt. `orc worker launch-next
<run-id>` supervises exactly one selected attempt and owns that attempt's
listener. `orc progress <message>` is worker-authored live feedback sent to the
current supervising listener.

## CLI Shape

`orc progress` accepts one or more positional words after the command and joins
them with single spaces into the raw message. These forms are equivalent:

```bash
orc progress analyzing code
orc progress "analyzing code"
```

`orc progress -h` and `orc progress --help` show help. No other flags are part
of v1.

The command sends raw CLI message text to the listener. The command does not
sanitize before sending because the listener owns sanitization, display safety,
and size validation.

If no live progress channel is available, `orc progress` warns on stderr and
exits 0 so progress reporting cannot fail the worker task.

Invalid local input is still an error: missing messages, empty messages after
sanitization, oversized messages, unknown flags, and invalid required identity
environment when a socket is present exit nonzero with actionable help.

Quote messages when shell punctuation or spacing matters:

```bash
orc progress "testing parser edge cases"
orc progress "blocked: missing API token"
```

Without quotes, multiple positional words are joined with single spaces.

## Transport

V1 uses newline-delimited JSON over a Unix stream socket. Linux and macOS are
the supported v1 platforms.

The request JSON object has exactly these fields:

- `run_id`
- `step_id`
- `attempt_id`
- `token`
- `message`

The response JSON object has:

- `status`: one of `accepted`, `dropped`, or `rejected`
- optional `error`

HTTP, TCP, loopback servers, remote streaming, filesystem polling, port
allocation, and Windows support are deferred. V1 keeps the transport local to
the supervising process, avoids network-exposed surfaces and port management,
and avoids durable-state polling for data that must not be persisted.

## Listener Ownership

The live progress listener is owned by the supervising launch command. Both
`orc run advance` and `orc worker launch-next` are listener owners so normal
advancement and one-attempt manual supervision have the same progress behavior.

The socket path lives in a per-listener temporary directory created with `0700`
permissions. It is not under durable run state. The listener removes the socket
and temporary directory when it closes.

For v1, a listener accepts only the currently registered run id, step id,
attempt id, and token. Requests still include all identity fields so the
protocol can support future parallel supervision without changing payload
shape.

## Environment

Launchers inject discovery and identity through these environment variables:

- `ORC_PROGRESS_SOCKET`
- `ORC_PROGRESS_TOKEN`
- `ORC_RUN_ID`
- `ORC_STEP_ID`
- `ORC_ATTEMPT_ID`

Agent workers, command steps, and script steps inherit the same progress
environment and may call `orc progress <message>`. Worker prompts should
mention `orc progress <message>` and these variables only as needed for
troubleshooting; normal workers should not pass them manually.

`ORC_PROGRESS_TOKEN` is per attempt. The launcher generates it with
cryptographic randomness, at least 128 bits of entropy, encoded as printable
text. Same-user socket permissions are not sufficient without this token.
Every socket request must include the token.

## Sanitization And Limits

The listener sanitizes before display and before size validation.
Accepted message text is a single sanitized line. The listener sanitizes by:

- trimming surrounding whitespace
- stripping terminal control characters
- collapsing embedded newlines and carriage returns to spaces

Empty sanitized messages are invalid input. The maximum size is 1000 UTF-8
bytes after sanitization, excluding the display prefix. Oversized messages are
invalid input and make `orc progress` exit nonzero with an actionable error.

Messages are free-form text, not an enum. V1 does not introduce structured
lifecycle states such as analyzing, editing, or testing.

## Rate Limiting

The listener rate limits live progress per attempt to 1 accepted message per
second with burst 3.

Messages over the limit receive response status `dropped`. A dropped response
makes `orc progress` warn on stderr and exit 0. Dropped messages are not
displayed and are not failures of the worker task.

## Output

Accepted progress messages are displayed with this exact prefix format:

```text
[<step-id> <attempt-id>] <message>
```

In human output mode, accepted progress messages are displayed on stdout.

For `orc run advance --json`, stdout remains reserved for the final JSON object
only. Live progress messages are displayed on stderr in JSON mode.

`accepted` means the listener displayed or queued the sanitized message for
display. `dropped` means the listener rate-limited the message. `rejected`
means invalid token, identity, protocol, or input.

Sender behavior is:

- `accepted`: `orc progress` exits 0.
- `dropped`: `orc progress` warns on stderr and exits 0.
- `rejected`: `orc progress` exits nonzero with an actionable error.
- no available channel before reaching a listener: `orc progress` warns on
  stderr and exits 0.

Operator example in human mode:

```text
$ orc run advance 20260507T233024Z-implementation-main-a0p-3-897111
[code 20260507T233051Z-code-abc123] analyzing code paths
[code 20260507T233051Z-code-abc123] beginning focused tests
advanced run 20260507T233024Z-implementation-main-a0p-3-897111
...
```

Operator example in JSON mode:

```text
$ orc run advance 20260507T233024Z-implementation-main-a0p-3-897111 --json
stderr: [code 20260507T233051Z-code-abc123] analyzing code paths
stdout: {"run_id":"20260507T233024Z-implementation-main-a0p-3-897111",...}
```

Worker example:

```bash
orc progress "starting analysis"
# ...perform the work...
orc progress "beginning tests"
orc report --run 20260507T233024Z-implementation-main-a0p-3-897111 --step code --agent coder --attempt 20260507T233051Z-code-abc123 --status done --result ready --summary "Implemented scoped change."
```

The progress calls are optional live updates. The final `orc report` remains
required by the worker prompt's report contract and is the only outcome that
drives workflow routing.

## Non-Persistence

Live progress is not persisted as run events, artifacts, `status.json` fields,
summary-context entries, final-summary fields, Beads comments, workflow outcomes,
or routing inputs. It is acceptable if progress text appears incidentally in
existing worker stdout/stderr logs because those logs capture the worker
process streams.

The final `advance` summary does not include the last live progress message in
v1.

## Deferred Work

These items are explicitly out of scope for v1:

- persisted progress history
- TUI or web UI
- full log streaming
- cross-run broadcast
- remote transport
- Windows support
- structured lifecycle states
- automatic summarization
- Beads integration
