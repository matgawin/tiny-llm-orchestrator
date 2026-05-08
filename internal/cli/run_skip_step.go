package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"tiny-llm-orchestrator/orc/internal/runskip"
)

func executeRunSkipStep(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runSkipStepFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunSkipStepHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runSkipStepFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	var stepID, reason string
	stepSet := false
	reasonSet := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--step":
			if stepSet {
				return runSkipStepFlagError(stderr, fmt.Errorf("repeated --step flags are ambiguous"))
			}
			if !assignFlagValue(args, &i, &stepID) {
				return runSkipStepFlagError(stderr, fmt.Errorf("--step requires a value"))
			}
			stepSet = true
		case "--reason":
			if reasonSet {
				return runSkipStepFlagError(stderr, fmt.Errorf("repeated --reason flags are ambiguous"))
			}
			if !assignFlagValue(args, &i, &reason) {
				return runSkipStepFlagError(stderr, fmt.Errorf("--reason requires a value"))
			}
			reasonSet = true
		case "-h", helpFlag, helpCommand:
			return printRunSkipStepHelp(stdout)
		default:
			if value, ok := strings.CutPrefix(arg, "--step="); ok {
				if stepSet {
					return runSkipStepFlagError(stderr, fmt.Errorf("repeated --step flags are ambiguous"))
				}
				stepID = value
				stepSet = true
				continue
			}
			if value, ok := strings.CutPrefix(arg, "--reason="); ok {
				if reasonSet {
					return runSkipStepFlagError(stderr, fmt.Errorf("repeated --reason flags are ambiguous"))
				}
				reason = value
				reasonSet = true
				continue
			}
			return runSkipStepFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if !stepSet || strings.TrimSpace(stepID) == "" {
		return runSkipStepFlagError(stderr, fmt.Errorf("--step is required"))
	}
	reason = strings.TrimSpace(reason)
	if !reasonSet || reason == "" {
		return runSkipStepFlagError(stderr, fmt.Errorf("--reason is required and must be non-empty after trimming"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := runskip.Skip(context.Background(), runskip.Options{
		Root:   root,
		RunID:  runID,
		StepID: stepID,
		Reason: reason,
		Source: "cli",
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run skip-step: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return printRunSkipStepResult(stdout, result, reason)
}

func runSkipStepFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run skip-step: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunSkipStepHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}

func printRunSkipStepResult(w io.Writer, result runskip.Result, reason string) error {
	if _, err := fmt.Fprintf(w, "skipped step %s for run %s\n", result.StepID, result.RunID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "reason: %s\n", reason); err != nil {
		return err
	}
	if result.Status.State == "running" && len(result.Status.WorkflowLoop.Entries) > 0 {
		entry := result.Status.WorkflowLoop.Entries[len(result.Status.WorkflowLoop.Entries)-1]
		_, err := fmt.Fprintf(w, "next selected step: %s\n", entry.State)
		return err
	}
	_, err := fmt.Fprintf(w, "run status: %s\n", result.Status.State)
	return err
}
