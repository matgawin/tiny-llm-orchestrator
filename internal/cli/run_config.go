package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runinspect"
)

func executeRunConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand) {
		return printRunConfigHelp(stdout)
	}
	if len(args) != 1 || args[0] == "" {
		if _, err := fmt.Fprintf(stderr, "%s run config: requires <run-id>\n", appName); err != nil {
			return err
		}
		return fmt.Errorf("run config requires run id")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts := runinspect.Options{
		Root:   root,
		RunID:  args[0],
		Stdout: stdout,
	}
	if err := runinspect.Config(context.Background(), opts); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run config: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}
