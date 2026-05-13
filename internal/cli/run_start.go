package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runstart"
)

func runstartOptions(workflow, bead, fallbackTaskFile, taskFile, task string, taskStdin bool, stdin io.Reader) runstart.Options {
	return runstart.Options{
		Workflow:         workflow,
		BeadID:           bead,
		FallbackTaskFile: fallbackTaskFile,
		TaskFile:         taskFile,
		TaskText:         task,
		TaskStdin:        taskStdin,
		Stdin:            stdin,
	}
}

func executeRunStart(opts runstart.Options, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts.Root = root
	result, err := runstart.Start(context.Background(), opts)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run start: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if _, err := fmt.Fprintf(stdout, "started run %s\n", result.RunID); err != nil {
		return err
	}
	return nil
}
