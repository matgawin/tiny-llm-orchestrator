package cli

import (
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

const (
	appName        = "orc"
	defaultVersion = "dev"
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
	case "-h", "--help", "help":
		return printHelp(stdout)
	case "version":
		if _, err := fmt.Fprintf(stdout, "%s %s\n", appName, version); err != nil {
			return err
		}
		return nil
	case "init":
		return executeInit(args[1:], stdin, stdout, stderr)
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
		case "-h", "--help", "help":
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

func printHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s is the Tiny LLM Orchestrator control plane.

Usage:
  %s [command]

Available Commands:
  help        Show command help
  init        Scaffold project-local Tiny Orc config
  version     Print version information

Flags:
  -h, --help  Show command help
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
