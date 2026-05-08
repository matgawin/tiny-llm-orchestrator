package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"tiny-llm-orchestrator/orc/internal/runsummary"
)

func executeRunRecordSummary(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunRecordSummaryHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	var file string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--file":
			if !assignFlagValue(args, &i, &file) {
				return runRecordSummaryFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "-h", helpFlag, helpCommand:
			return printRunRecordSummaryHelp(stdout)
		default:
			return runRecordSummaryFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if strings.TrimSpace(file) == "" {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("--file is required"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := runsummary.Record(context.Background(), runsummary.Options{
		Root:  root,
		RunID: runID,
		File:  file,
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run record-summary: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if _, err := fmt.Fprintf(stdout, "recorded final summary for run %s at %s\n", result.RunID, result.SummaryRef.Path); err != nil {
		return err
	}
	return nil
}

func runRecordSummaryFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run record-summary: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunRecordSummaryHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
