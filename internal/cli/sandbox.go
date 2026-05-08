package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/sandbox"
)

func executeSandbox(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printSandboxHelp(stdout)
	}
	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printSandboxHelp(stdout)
	case "run":
		return executeSandboxRun(args[1:], stdin, stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "%s sandbox: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printSandboxHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown sandbox command: %s", args[0])
	}
}

func executeSandboxRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
			return printSandboxRunHelp(stdout)
		}
		if _, err := fmt.Fprintf(stderr, "%s sandbox run: unexpected argument %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printSandboxRunHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unexpected sandbox run argument: %s", args[0])
	}
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
