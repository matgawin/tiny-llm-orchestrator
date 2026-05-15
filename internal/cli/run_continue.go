package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func executeRunContinue(runID string, allowLoopCap, resolveBlock bool, reasons []string, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute run continue: %w", err)
	}
	store, err := runstore.Open(root)
	if err != nil {
		return fmt.Errorf("execute run continue: %w", err)
	}
	if resolveBlock {
		reason := strings.TrimSpace(reasons[0])
		status, event, err := store.ResolveHumanBlock(runID, reason, time.Time{})
		if err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
				return fmt.Errorf("execute run continue: %w", writeErr)
			}
			return fmt.Errorf("execute run continue: %w", err)
		}
		if status.Continued == nil {
			return stableerr.Errorf("run %q resolve-block continuation was not persisted", runID)
		}
		_, err = fmt.Fprintf(stdout, "continued run %s after human-resolved block; retrying step %s from attempt %s at event %d\n", runID, status.Continued.ResolvedStepID, status.Continued.ResolvedAttemptID, event.Sequence)
		if err != nil {
			return fmt.Errorf("execute run continue: %w", err)
		}
		return nil
	}
	status, _, err := store.AllowWorkflowLoopHardCap(runID, "allow_loop_cap", time.Time{})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute run continue: %w", writeErr)
		}
		return fmt.Errorf("execute run continue: %w", err)
	}
	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil {
		return stableerr.Errorf("run %q loop-cap override was not persisted", runID)
	}
	_, err = fmt.Fprintf(stdout, "continued run %s after workflow loop hard cap; allowed one entry into %s at count %d\n", runID, override.TargetState, override.CountAfterOverride)
	if err != nil {
		return fmt.Errorf("execute run continue: %w", err)
	}
	return nil
}
