package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

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
	root := newRootCommand(stdin, stdout, stderr)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil && isRootRoutingError(err) {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           appName,
		Short:         "Tiny LLM Orchestrator control plane",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if stdin != nil {
		cmd.SetIn(stdin)
	}
	cmd.DisableSuggestions = true
	cmd.CompletionOptions.DisableDefaultCmd = true

	cmd.AddCommand(
		newInitCommand(stdin, stdout, stderr),
		newProgressCommand(stdout, stderr),
		newReportCommand(stdout, stderr),
		newRunCommand(stdin, stdout, stderr),
		newSandboxCommand(stdin, stdout, stderr),
		newWorkerCommand(stdout, stderr),
		legacyCommand("version", "Print version information", func(args []string) error {
			_, err := fmt.Fprintf(stdout, "%s %s\n", appName, version)
			return err
		}),
	)

	return cmd
}

func legacyCommand(use, short string, run func([]string) error) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(args)
		},
	}
}

func isRootRoutingError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.HasPrefix(message, "unknown command ") || strings.HasPrefix(message, "unknown flag: ")
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
