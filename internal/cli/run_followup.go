package cli

import (
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func executeRunAddFollowup(runID, title, details string, stdout, stderr io.Writer) error {
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
