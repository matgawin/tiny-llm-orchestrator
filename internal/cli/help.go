package cli

import (
	"fmt"
)

func progressHelpLong() string {
	return fmt.Sprintf(`%s progress sends optional live worker progress to the supervising listener.

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
Use %s report --status <status> --result <result> for final worker outcome reporting.`, appName, appName)
}

func initHelpLong() string {
	return fmt.Sprintf("%s init scaffolds project-local Tiny Orc config in the current directory.", appName)
}

func timeLeftHelpLong() string {
	return fmt.Sprintf(`%s time-left prints deadline guidance for the current worker attempt.

By default it reads ORC_RUN_ID and ORC_ATTEMPT_ID from the worker environment.
If ORC_STEP_ID is set, it must match the persisted attempt step.
For debugging, pass --run <run-id> and --attempt <attempt-id>.

Output includes deadline, elapsed time, remaining time, timeout, phase, and action.
Use --json from hooks. NORMAL stays quiet in hooks; NARROW, WRAP_UP, and REPORT_NOW can be shown as short directives.
Final worker outcomes still go through %s report.`, appName, appName)
}
