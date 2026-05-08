package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/launcher"
)

func executeWorker(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printWorkerHelp(stdout)
	}
	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printWorkerHelp(stdout)
	case "launch-next":
		return executeWorkerLaunchNext(args[1:], stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "%s worker: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printWorkerHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown worker command: %s", args[0])
	}
}

func executeWorkerLaunchNext(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		if _, err := fmt.Fprintf(stderr, "%s worker launch-next: requires <run-id>\n", appName); err != nil {
			return err
		}
		return fmt.Errorf("worker launch-next requires run id")
	}
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
		RunID:  args[0],
		Stdout: stdout,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s worker launch-next: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}
