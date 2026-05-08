package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/runstart"
)

func executeRunStart(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts := runstart.Options{
		Stdin: stdin,
	}
	stringFlags := map[string]*string{
		"--workflow":           &opts.Workflow,
		"--bead":               &opts.BeadID,
		"--fallback-task-file": &opts.FallbackTaskFile,
		"--task-file":          &opts.TaskFile,
		"--task":               &opts.TaskText,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if target, ok := stringFlags[arg]; ok {
			if !assignFlagValue(args, &i, target) {
				return runStartFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
			continue
		}
		switch arg {
		case "-h", helpFlag, helpCommand:
			return printRunStartHelp(stdout)
		case "--task-stdin":
			opts.TaskStdin = true
		default:
			return runStartFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
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

func runStartFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run start: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunStartHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
