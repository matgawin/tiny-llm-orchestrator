package cli

import (
	"fmt"
	"io"

	"tiny-llm-orchestrator/orc/internal/runinspect"
)

func executeRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printRunHelp(stdout)
	}
	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printRunHelp(stdout)
	case "add-followup":
		return executeRunAddFollowup(args[1:], stdout, stderr)
	case "advance":
		return executeRunAdvance(args[1:], stdout, stderr)
	case "continue":
		return executeRunContinue(args[1:], stdout, stderr)
	case "start":
		return executeRunStart(args[1:], stdin, stdout, stderr)
	case "show":
		return executeRunInspect("show", args[1:], stdout, stderr, runinspect.Status)
	case "skip-step":
		return executeRunSkipStep(args[1:], stdout, stderr)
	case "status":
		return executeRunInspect("status", args[1:], stdout, stderr, runinspect.Status)
	case "next":
		return executeRunInspect("next", args[1:], stdout, stderr, runinspect.Next)
	case "record-summary":
		return executeRunRecordSummary(args[1:], stdout, stderr)
	case "summary-context":
		return executeRunInspect("summary-context", args[1:], stdout, stderr, runinspect.SummaryContext)
	default:
		if _, err := fmt.Fprintf(stderr, "%s run: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printRunHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown run command: %s", args[0])
	}
}
