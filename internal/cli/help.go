package cli

import (
	"fmt"
	"io"
)

func printProgressHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s progress sends optional live worker progress to the supervising listener.

Usage:
  %s progress <message>

The message may be one quoted argument or multiple positional words, which are joined with single spaces.
Use -- before a message word that starts with '-' because v1 has no progress flags other than help.

Environment:
  ORC_PROGRESS_SOCKET   Unix socket path for the live progress channel
  ORC_PROGRESS_TOKEN    Per-attempt progress token
  ORC_RUN_ID            Current run id
  ORC_STEP_ID           Current workflow step id
  ORC_ATTEMPT_ID        Current attempt id

If ORC_PROGRESS_SOCKET is unset or the socket listener is unavailable, this command warns on stderr and exits 0 so optional progress cannot fail worker execution.
If ORC_PROGRESS_SOCKET is set, the run id, step id, attempt id, and token environment variables are required.

Input:
  Empty, whitespace-only, and messages over 1000 bytes after listener sanitization are rejected.
  The listener trims surrounding whitespace, strips terminal control characters, and collapses embedded newlines and carriage returns to spaces before display.
  Rate-limited progress updates warn on stderr and exit 0.
  Invalid identity or token rejections are errors and exit non-zero.

This command is only live operator feedback. It does not create final worker reports, workflow outcomes, run events, Beads comments, or persisted status fields.
Use %s report --status <status> --result <result> for final worker outcome reporting.

Flags:
  -h, --help  Show command help
`, appName, appName, appName)

	return err
}

func printRunHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run manages orchestration runs.

Usage:
  %s run [command]

Available Commands:
  add-followup     Record out-of-scope follow-up work
  advance          Launch workflow-selected workers until a conservative stop
  config           Inspect the current pinned config snapshot
  continue         Continue after an explicit human-reviewed stop
  next             Inspect the next workflow action without launching it
  refresh-config   Refresh an existing run to the current live .orc config
  record-summary   Record a final ready-for-review summary
  show             Show persisted run state
  skip-step        Skip the currently selected skippable workflow step
  start            Start a run from explicit task context
  status           Show persisted run state
  summary-context  Render persisted review context

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunSkipStepHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run skip-step records an explicit human decision to bypass the currently selected skippable workflow step.

Usage:
  %s run skip-step <run-id> --step <step-id> --reason <text>

Flags:
      --step <step-id>  Required workflow step id; must match the current select_step decision
      --reason <text>   Required human reason; whitespace-only reasons are rejected
  -h, --help            Show command help

The command only skips the current runnable select_step decision. It rejects active worker attempts, retry, wait, terminal decisions, terminal runs, non-skippable steps, and step ids other than the selected step.
The persisted outcome is the system-owned done/skipped transition declared by workflow config. Workers cannot report done/skipped, and this command does not imply review approval unless the workflow's done/skipped route says so.
JSON output and additional confirmation flags are not supported in v1.
`, appName, appName)

	return err
}

func printRunRefreshConfigHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run refresh-config validates live .orc config and explicitly refreshes an existing run's pinned config snapshot.

Usage:
  %s run refresh-config <run-id>

The command rejects active attempts and incompatible workflow changes. There is no --force flag in v1.

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunConfigHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run config prints the current pinned config snapshot metadata for an existing run.

Usage:
  %s run config <run-id>

The command reads the run snapshot metadata and refresh events from .orc/runs. It does not load live .orc config.
Output includes the current snapshot version, version directory, created time, manifest hash, source file count/hash summary, and refresh history.

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunAdvanceHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run advance launches workflow-selected worker attempts until the run reaches a normal completion point or needs operator attention.

Usage:
  %s run advance <run-id> [--max-steps N] [--once] [--json]

Flags:
      --max-steps N  Stop before launching another worker after N launched attempts (default 20; must be positive)
      --once         Launch at most one selected worker attempt, then report the resulting state
      --json         Emit one JSON result object on stdout after the command stops
  -h, --help         Show command help

Stop reasons:
  ready_for_human        normal completion
  blocked_for_human      workflow terminal human handoff
  worker_blocked         launched worker reported blocked/*
  worker_failed          launched worker reported failed/*
  loop_soft_cap          workflow soft loop cap reached before launch
  loop_hard_cap          workflow hard loop cap blocked the run before launch
  max_steps_reached      max-step guard stopped before another launch
  active_attempt_exists  command started while a worker attempt was active
  error                  invalid state, config, launcher, or persistence error

Exit codes:
  0  ready_for_human or max_steps_reached
  1  worker_failed, active_attempt_exists, invalid state, config, launcher, or persistence error
  2  blocked_for_human, worker_blocked, loop_soft_cap, or loop_hard_cap

Existing worker diagnostics are preserved. With --json, progress and launcher diagnostics are written to stderr so stdout contains only the final JSON object.
`, appName, appName)

	return err
}

func printRunContinueHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run continue resumes a human-reviewed stopped run.

Usage:
  %s run continue <run-id> --allow-loop-cap
  %s run continue <run-id> --resolve-block --reason <text>

Flags:
      --allow-loop-cap  Explicitly allow one continuation after a hard workflow loop stop
      --resolve-block   Retry a non-loop blocked_for_human step after human resolution
      --reason <text>   Required human attestation for --resolve-block
  -h, --help            Show command help

The loop-cap override is for human-reviewed continuation after a hard workflow loop stop. It allows exactly one additional routing decision into the currently blocked workflow state.
The resolve-block mode is for non-loop blocked_for_human runs where a human fixed the external problem outside Orc. It records the trimmed reason and retries the blocked step without skipping workflow routing.
`, appName, appName, appName)

	return err
}

func printRunRecordSummaryHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run record-summary records a final ready-for-review summary for a ready run.

Usage:
  %s run record-summary <run-id> --file <path>

Flags:
      --file <path>  Markdown summary file to copy into the run store
  -h, --help         Show command help
`, appName, appName)

	return err
}

func printRunAddFollowupHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run add-followup records out-of-scope follow-up work for a run.

Usage:
  %s run add-followup <run-id> --title <title> [--details <markdown>]

Flags:
      --title <title>       Follow-up title
      --details <markdown>  Optional Markdown details
  -h, --help                Show command help
`, appName, appName)

	return err
}

func printRunStartHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run start creates a durable run from explicit task context.

Usage:
  %s run start --workflow <name> (--bead <id> [--fallback-task-file <path>] | --task-file <path> | --task <markdown> | --task-stdin)

Flags:
      --workflow <name>            Workflow to start
      --bead <id>                  Read bead context through bd without mutating beads
      --fallback-task-file <path>  Markdown task file to use if explicit bead lookup fails
      --task-file <path>           Markdown task file to snapshot
      --task <markdown>            Inline Markdown task context
      --task-stdin                 Read Markdown task context from stdin
  -h, --help                       Show command help
`, appName, appName)

	return err
}

func printReportHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s report validates and persists a worker report.

Usage:
  %s report --run <run-id> --step <step-id> --agent <agent-id> --attempt <attempt-id> --status <status> --result <result> --summary <summary> [flags]
  %s report --json-file <path>

Flags:
      --json-file <path>     Read report fields from a JSON file
      --run <run-id>         Run id
      --step <step-id>       Workflow step id
      --agent <agent-id>     Agent id
      --attempt <attempt-id> Attempt id
      --status <status>      Report status
      --result <result>      Report result
      --summary <summary>    Compact report summary
      --changed-path <path>  Changed path; repeatable
      --command <command>    Command run; repeatable
      --test <test>          Test run; repeatable
      --risk <risk>          Risk or caveat; repeatable
      --follow-up <title>    Follow-up suggestion title; repeatable
      --report-file <path>   Markdown detail file to copy into the run store
  -h, --help                 Show command help
`, appName, appName, appName)

	return err
}

func printInitHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s init scaffolds project-local Tiny Orc config in the current directory.

Usage:
  %s init [--dry-run | --yes]

Flags:
      --dry-run  Print planned changes without writing files
      --yes      Create missing scaffold files without prompts
  -h, --help     Show command help
`, appName, appName)

	return err
}
