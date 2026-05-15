package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/sandbox"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func newSandboxCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Run configured commands through bubblewrap",
		Long:  appName + " sandbox runs configured commands through bubblewrap.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newSandboxRunCommand(stdin, stdout, stderr))
	return cmd
}

func newSandboxRunCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run sandbox.command.argv from .orc/config.yaml through bwrap",
		Long:  appName + " sandbox run launches the configured sandbox command through the system bwrap binary.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			if _, err := fmt.Fprintf(stderr, "%s sandbox run: unexpected argument %q\n\n", appName, args[0]); err != nil {
				return err
			}
			return stableerr.Errorf("unexpected sandbox run argument: %s", args[0])
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeSandboxRun(stdin, stdout, stderr)
		},
	}
}

func executeSandboxRun(stdin io.Reader, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restoreSignals := context.AfterFunc(ctx, stop)
	defer restoreSignals()
	if err := sandbox.Run(ctx, sandbox.Options{
		Root:   root,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s sandbox run: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}
