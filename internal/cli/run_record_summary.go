package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runsummary"
)

func executeRunRecordSummary(runID, file string, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute run record summary: %w", err)
	}
	result, err := runsummary.Record(context.Background(), runsummary.Options{
		Root:  root,
		RunID: runID,
		File:  file,
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run record-summary: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute run record summary: %w", writeErr)
		}
		return fmt.Errorf("execute run record summary: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "recorded final summary for run %s at %s\n", result.RunID, result.SummaryRef.Path); err != nil {
		return fmt.Errorf("execute run record summary: %w", err)
	}
	return nil
}
