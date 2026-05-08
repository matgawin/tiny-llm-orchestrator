package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func executeRunContinue(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runContinueFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunContinueHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runContinueFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	allowLoopCap := false
	resolveBlock := false
	var reasons []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--allow-loop-cap":
			allowLoopCap = true
		case "--resolve-block":
			resolveBlock = true
		case "--reason":
			var reason string
			if !assignFlagValue(args, &i, &reason) {
				return runContinueFlagError(stderr, fmt.Errorf("--reason requires a value"))
			}
			reasons = append(reasons, reason)
		case "-h", helpFlag, helpCommand:
			return printRunContinueHelp(stdout)
		default:
			if value, ok := strings.CutPrefix(arg, "--reason="); ok {
				reasons = append(reasons, value)
				continue
			}
			return runContinueFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if allowLoopCap && resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("--resolve-block and --allow-loop-cap are mutually exclusive continuation modes"))
	}
	if len(reasons) > 1 {
		return runContinueFlagError(stderr, fmt.Errorf("repeated --reason flags are ambiguous"))
	}
	if len(reasons) > 0 && !resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("--reason is only valid with --resolve-block"))
	}
	if resolveBlock && len(reasons) == 0 {
		return runContinueFlagError(stderr, fmt.Errorf("--reason is required for --resolve-block"))
	}
	if !allowLoopCap && !resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("choose one continuation mode: --allow-loop-cap or --resolve-block --reason <text>"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := runstore.Open(root)
	if err != nil {
		return err
	}
	if resolveBlock {
		reason := strings.TrimSpace(reasons[0])
		if reason == "" {
			return runContinueFlagError(stderr, fmt.Errorf("--reason is required for --resolve-block and must be non-empty after trimming"))
		}
		status, event, err := store.ResolveHumanBlock(runID, reason, time.Time{})
		if err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
				return writeErr
			}
			return err
		}
		if status.Continued == nil {
			return fmt.Errorf("run %q resolve-block continuation was not persisted", runID)
		}
		_, err = fmt.Fprintf(stdout, "continued run %s after human-resolved block; retrying step %s from attempt %s at event %d\n", runID, status.Continued.ResolvedStepID, status.Continued.ResolvedAttemptID, event.Sequence)
		return err
	}
	status, _, err := store.AllowWorkflowLoopHardCap(runID, "allow_loop_cap", time.Time{})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil {
		return fmt.Errorf("run %q loop-cap override was not persisted", runID)
	}
	_, err = fmt.Fprintf(stdout, "continued run %s after workflow loop hard cap; allowed one entry into %s at count %d\n", runID, override.TargetState, override.CountAfterOverride)
	return err
}

func runContinueFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunContinueHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
