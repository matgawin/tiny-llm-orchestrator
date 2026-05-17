package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runinspect"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func executeRunConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		if _, err := fmt.Fprintf(stderr, "%s run config: requires <run-id>\n", appName); err != nil {
			return fmt.Errorf("execute run config: %w", err)
		}

		return stableerr.Errorf("run config requires run id")
	}

	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute run config: %w", err)
	}

	opts := runinspect.Options{
		Root:   root,
		RunID:  args[0],
		Stdout: stdout,
	}
	if err := runinspect.Config(context.Background(), opts); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run config: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute run config: %w", writeErr)
		}

		return fmt.Errorf("execute run config: %w", err)
	}

	return nil
}
