package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func executeRunAddFollowup(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runAddFollowupFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunAddFollowupHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runAddFollowupFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	var title, details string
	stringFlags := map[string]*string{
		"--title":   &title,
		"--details": &details,
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if target, ok := stringFlags[arg]; ok {
			if !assignFlagValue(args, &i, target) {
				return runAddFollowupFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
			continue
		}
		switch arg {
		case "-h", helpFlag, helpCommand:
			return printRunAddFollowupHelp(stdout)
		default:
			return runAddFollowupFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if strings.TrimSpace(title) == "" {
		return runAddFollowupFlagError(stderr, fmt.Errorf("--title is required"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := runstore.Open(root)
	if err != nil {
		return err
	}
	if _, err := store.RecordFollowup(runID, runstore.RecordFollowupRequest{
		Followup: runstore.Followup{
			Title:   title,
			Details: details,
		},
		Source: runstore.FollowupSourceOrchestrator,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run add-followup: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if _, err := fmt.Fprintf(stdout, "recorded follow-up for run %s\n", runID); err != nil {
		return err
	}
	return nil
}

func runAddFollowupFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run add-followup: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunAddFollowupHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
