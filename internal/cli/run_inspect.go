package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runinspect"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func executeRunInspect(command string, args []string, stdout, stderr io.Writer, inspect func(context.Context, runinspect.Options) error) error {
	if len(args) != 1 || args[0] == "" {
		if _, err := fmt.Fprintf(stderr, "%s run %s: requires <run-id>\n", appName, command); err != nil {
			return fmt.Errorf("execute run inspect: %w", err)
		}

		return stableerr.Errorf("run %s requires run id", command)
	}

	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute run inspect: %w", err)
	}

	opts := runinspect.Options{
		Root:   root,
		RunID:  args[0],
		Stdout: stdout,
	}
	if err := inspect(context.Background(), opts); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run %s: %v\n", appName, command, err); writeErr != nil {
			return fmt.Errorf("execute run inspect: %w", writeErr)
		}

		return err
	}

	return nil
}
