package cli

import (
	"errors"
	"fmt"
	"io"

	"tiny-llm-orchestrator/orc/internal/sandbox"
)

const (
	appName        = "orc"
	defaultVersion = "dev"
	helpFlag       = "--help"
	helpCommand    = "help"
)

var version = defaultVersion

// Execute runs the orc command with explicit output streams for deterministic
// tests. Commands that need stdin should use ExecuteWithInput.
func Execute(args []string, stdout, stderr io.Writer) error {
	return ExecuteWithInput(args, nil, stdout, stderr)
}

// ExecuteWithInput runs the orc command with explicit streams.
func ExecuteWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printHelp(stdout)
	}

	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printHelp(stdout)
	case "version":
		if _, err := fmt.Fprintf(stdout, "%s %s\n", appName, version); err != nil {
			return err
		}
		return nil
	case "init":
		return executeInit(args[1:], stdin, stdout, stderr)
	case "progress":
		return executeProgress(args[1:], stdout, stderr)
	case "run":
		return executeRun(args[1:], stdin, stdout, stderr)
	case "sandbox":
		return executeSandbox(args[1:], stdin, stdout, stderr)
	case "worker":
		return executeWorker(args[1:], stdout, stderr)
	case "report":
		return executeReport(args[1:], stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "%s: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

// ExitCode returns the process exit code represented by err.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var cliExit exitError
	if errors.As(err, &cliExit) {
		return cliExit.Code
	}
	var sandboxExit sandbox.ExitError
	if errors.As(err, &sandboxExit) {
		return sandboxExit.Code
	}
	return 1
}

type exitError struct {
	Code int
	Err  error
}

func (e exitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

func (e exitError) Unwrap() error {
	return e.Err
}
