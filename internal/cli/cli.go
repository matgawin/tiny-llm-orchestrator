package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/sandbox"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const (
	appName        = "orc"
	defaultVersion = "dev"
	helpFlag       = "--help"
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
		newCompletionCommand(stdout, stderr),
		newInitCommand(stdin, stdout, stderr),
		newProgressCommand(stdout, stderr),
		newReportCommand(stdout, stderr),
		newRunCommand(stdin, stdout, stderr),
		newSandboxCommand(stdin, stdout, stderr),
		newWorkerCommand(stdout, stderr),
		newVersionCommand(stdout),
	)

	return cmd
}

func newVersionCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print version information",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(stdout, "%s %s\n", appName, version)
			return err
		},
	}
}

func newCompletionCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <shell>",
		Short: "Generate shell completion scripts",
		Long: appName + ` completion generates shell completion scripts.

Supported shells are bash, zsh, fish, and powershell.`,
		Args: completionShellArgs(stderr),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(stdout)
			case "zsh":
				return cmd.Root().GenZshCompletion(stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletion(stdout)
			default:
				return completionShellError(cmd, stderr, stableerr.Errorf("unsupported shell %q", args[0]))
			}
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return cmd
}

func completionShellArgs(stderr io.Writer) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 && args[0] != "" {
			return nil
		}
		if len(args) == 0 || args[0] == "" {
			return completionShellError(cmd, stderr, stableerr.Errorf("requires <shell>"))
		}
		return completionShellError(cmd, stderr, stableerr.Errorf("accepts exactly one <shell>"))
	}
}

func completionShellError(cmd *cobra.Command, stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s completion: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	cmd.SetOut(stderr)
	if usageErr := cmd.Usage(); usageErr != nil {
		return usageErr
	}
	return fmt.Errorf("%s completion: %w", appName, err)
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
