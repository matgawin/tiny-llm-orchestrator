package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"tiny-llm-orchestrator/orc/internal/runskip"
)

func executeRunSkipStep(runID string, stepValues, reasonValues []string, stdout, stderr io.Writer) error {
	stepID := stepValues[0]
	reason := strings.TrimSpace(reasonValues[0])
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
