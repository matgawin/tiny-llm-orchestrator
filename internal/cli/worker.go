package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func newWorkerCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Launch and supervise worker attempts",
		Long:  appName + " worker launches and supervises worker attempts.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newWorkerLaunchNextCommand(stdout, stderr))
	return cmd
}

func newWorkerLaunchNextCommand(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "launch-next <run-id>",
		Short: "Launch the workflow-selected worker for a run",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "" {
				return nil
			}
			if len(args) == 0 || args[0] == "" {
				if _, err := fmt.Fprintf(stderr, "%s worker launch-next: requires <run-id>\n", appName); err != nil {
					return err
				}
				return stableerr.Errorf("worker launch-next requires run id")
			}
			if _, err := fmt.Fprintf(stderr, "%s worker launch-next: accepts exactly one <run-id>\n", appName); err != nil {
				return err
			}
			return stableerr.Errorf("worker launch-next accepts exactly one run id")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeWorkerLaunchNext(args[0], stdout, stderr)
		},
	}
}

func executeWorkerLaunchNext(runID string, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restoreSignals := context.AfterFunc(ctx, stop)
	defer restoreSignals()
	if _, err := launcher.LaunchNext(ctx, launcher.Options{
		Root:   root,
		RunID:  runID,
		Stdout: stdout,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s worker launch-next: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}
