package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/launcher"
)

func executeRunAdvance(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runAdvanceFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunAdvanceHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runAdvanceFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	opts := launcher.AdvanceOptions{
		RunID:    runID,
		MaxSteps: launcher.DefaultAdvanceMaxSteps,
	}
	jsonOutput := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--once":
			opts.Once = true
		case "--json":
			jsonOutput = true
		case "--max-steps":
			var raw string
			if !assignFlagValue(args, &i, &raw) {
				return runAdvanceFlagError(stderr, fmt.Errorf("--max-steps requires a value"))
			}
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				return runAdvanceFlagError(stderr, fmt.Errorf("--max-steps must be a positive integer"))
			}
			opts.MaxSteps = value
		case "-h", helpFlag, helpCommand:
			return printRunAdvanceHelp(stdout)
		default:
			if value, ok := strings.CutPrefix(arg, "--max-steps="); ok {
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < 1 {
					return runAdvanceFlagError(stderr, fmt.Errorf("--max-steps must be a positive integer"))
				}
				opts.MaxSteps = parsed
				continue
			}
			return runAdvanceFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return err
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
			return encodeErr
		}
	} else {
		if writeErr := printAdvanceResult(stdout, result); writeErr != nil {
			return writeErr
		}
	}
	if err != nil && result.ExitCode == 1 {
		if _, writeErr := fmt.Fprintf(stderr, "%s run advance: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if result.ExitCode != 0 {
		return exitError{Code: result.ExitCode, Err: err}
	}
	return nil
}

func runAdvanceFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run advance: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunAdvanceHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
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
		return err
	}
	if _, err := fmt.Fprintf(w, "launched attempts: %d\n", len(result.LaunchedAttempts)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "final status: %s\n", result.FinalStatus); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "final decision: %s\n", result.FinalDecision); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "stop reason: %s\n", result.StopReason); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "exit code: %d\n", result.ExitCode); err != nil {
		return err
	}
	if result.Error != "" {
		_, err := fmt.Fprintf(w, "error: %s\n", result.Error)
		return err
	}
	return nil
}
