package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/initconfig"
	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/runinspect"
	"tiny-llm-orchestrator/orc/internal/runstart"
)

const (
	appName        = "orc"
	defaultVersion = "dev"
	helpFlag       = "--help"
	helpCommand    = "help"
)

var version = defaultVersion

// Execute runs the orc command with explicit output streams for deterministic
// tests. Commands that need stdin should use ExecuteWithInput.
func Execute(args []string, stdout, stderr io.Writer) error {
	return ExecuteWithInput(args, nil, stdout, stderr)
}

// ExecuteWithInput runs the orc command with explicit streams.
func ExecuteWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printHelp(stdout)
	}

	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printHelp(stdout)
	case "version":
		if _, err := fmt.Fprintf(stdout, "%s %s\n", appName, version); err != nil {
			return err
		}
		return nil
	case "init":
		return executeInit(args[1:], stdin, stdout, stderr)
	case "run":
		return executeRun(args[1:], stdin, stdout, stderr)
	case "worker":
		return executeWorker(args[1:], stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "%s: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

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

func executeInit(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts := initconfig.Options{
		Stdin:  stdin,
		Stdout: stdout,
	}
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		case "-h", helpFlag, helpCommand:
			return printInitHelp(stdout)
		default:
			if _, err := fmt.Fprintf(stderr, "%s init: unknown flag %q\n\n", appName, arg); err != nil {
				return err
			}
			if err := printInitHelp(stderr); err != nil {
				return err
			}
			return fmt.Errorf("unknown init flag: %s", arg)
		}
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts.Root = root
	if err := initconfig.Run(opts); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s init: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}

func executeRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printRunHelp(stdout)
	}
	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printRunHelp(stdout)
	case "start":
		return executeRunStart(args[1:], stdin, stdout, stderr)
	case "status":
		return executeRunInspect("status", args[1:], stdout, stderr, runinspect.Status)
	case "next":
		return executeRunInspect("next", args[1:], stdout, stderr, runinspect.Next)
	default:
		if _, err := fmt.Fprintf(stderr, "%s run: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printRunHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown run command: %s", args[0])
	}
}

func executeRunInspect(command string, args []string, stdout, stderr io.Writer, inspect func(context.Context, runinspect.Options) error) error {
	if len(args) != 1 || args[0] == "" {
		if _, err := fmt.Fprintf(stderr, "%s run %s: requires <run-id>\n", appName, command); err != nil {
			return err
		}
		return fmt.Errorf("run %s requires run id", command)
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
	if err := inspect(context.Background(), opts); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run %s: %v\n", appName, command, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}

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

func assignFlagValue(args []string, index *int, target *string) bool {
	next := *index + 1
	if next >= len(args) || args[next] == "" {
		return false
	}
	*index = next
	*target = args[next]
	return true
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

func printHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s is the Tiny LLM Orchestrator control plane.

Usage:
  %s [command]

Available Commands:
  help        Show command help
  init        Scaffold project-local Tiny Orc config
  run         Manage orchestration runs
  worker      Launch and supervise worker attempts
  version     Print version information

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printWorkerHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s worker launches and supervises worker attempts.

Usage:
  %s worker [command]

Available Commands:
  launch-next  Launch the workflow-selected worker for a run

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run manages orchestration runs.

Usage:
  %s run [command]

Available Commands:
  next        Inspect the next workflow action without launching it
  start       Start a run from explicit task context
  status      Show persisted run state

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunStartHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run start creates a durable run from explicit task context.

Usage:
  %s run start --workflow <name> (--bead <id> [--fallback-task-file <path>] | --task-file <path> | --task <markdown> | --task-stdin)

Flags:
      --workflow <name>            Workflow to start
      --bead <id>                  Read bead context through bd without mutating beads
      --fallback-task-file <path>  Markdown task file to use if explicit bead lookup fails
      --task-file <path>           Markdown task file to snapshot
      --task <markdown>            Inline Markdown task context
      --task-stdin                 Read Markdown task context from stdin
  -h, --help                       Show command help
`, appName, appName)

	return err
}

func printInitHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s init scaffolds project-local Tiny Orc config in the current directory.

Usage:
  %s init [--dry-run | --yes]

Flags:
      --dry-run  Print planned changes without writing files
      --yes      Create missing scaffold files without prompts
  -h, --help     Show command help
`, appName, appName)

	return err
}
