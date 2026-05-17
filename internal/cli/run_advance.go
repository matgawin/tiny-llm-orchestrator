package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/launcher"
)

func executeRunAdvance(runID string, maxSteps int, once, jsonOutput bool, stdout, stderr io.Writer) error {
	opts := launcher.AdvanceOptions{
		RunID:    runID,
		MaxSteps: maxSteps,
		Once:     once,
	}

	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute run advance: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restoreSignals := context.AfterFunc(ctx, stop)
	defer restoreSignals()

	opts.Root = root
	if jsonOutput {
		opts.Stdout = stderr
		opts.Progress = stderr
	} else {
		opts.Stdout = stdout
		opts.Progress = stdout
	}

	result, err := launcher.Advance(ctx, opts)
	if jsonOutput {
		if encodeErr := json.NewEncoder(stdout).Encode(advanceJSON(result)); encodeErr != nil {
			return fmt.Errorf("execute run advance: %w", encodeErr)
		}
	} else {
		if writeErr := printAdvanceResult(stdout, result); writeErr != nil {
			return writeErr
		}
	}

	if err != nil && result.ExitCode == 1 {
		if _, writeErr := fmt.Fprintf(stderr, "%s run advance: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute run advance: %w", writeErr)
		}

		return fmt.Errorf("execute run advance: %w", err)
	}

	if result.ExitCode != 0 {
		return exitError{Code: result.ExitCode, Err: err}
	}

	return nil
}

type advanceJSONResult struct {
	RunID            string                    `json:"run_id"`
	LaunchedAttempts []launcher.AdvanceAttempt `json:"launched_attempts"`
	FinalStatus      string                    `json:"final_status"`
	FinalDecision    string                    `json:"final_decision"`
	StopReason       string                    `json:"stop_reason"`
	ExitCode         int                       `json:"exit_code"`
	Error            string                    `json:"error,omitempty"`
}

func advanceJSON(result launcher.AdvanceResult) advanceJSONResult {
	return advanceJSONResult{
		RunID:            result.RunID,
		LaunchedAttempts: result.LaunchedAttempts,
		FinalStatus:      result.FinalStatus,
		FinalDecision:    result.FinalDecision,
		StopReason:       result.StopReason,
		ExitCode:         result.ExitCode,
		Error:            result.Error,
	}
}

func printAdvanceResult(w io.Writer, result launcher.AdvanceResult) error {
	if _, err := fmt.Fprintf(w, "advanced run %s\n", result.RunID); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if _, err := fmt.Fprintf(w, "launched attempts: %d\n", len(result.LaunchedAttempts)); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if _, err := fmt.Fprintf(w, "final status: %s\n", result.FinalStatus); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if _, err := fmt.Fprintf(w, "final decision: %s\n", result.FinalDecision); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if _, err := fmt.Fprintf(w, "stop reason: %s\n", result.StopReason); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if _, err := fmt.Fprintf(w, "exit code: %d\n", result.ExitCode); err != nil {
		return fmt.Errorf("print advance result: %w", err)
	}

	if result.Error != "" {
		_, err := fmt.Fprintf(w, "error: %s\n", result.Error)
		if err != nil {
			return fmt.Errorf("print advance result: %w", err)
		}

		return nil
	}

	return nil
}
