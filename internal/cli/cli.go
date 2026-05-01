package cli

import (
	"fmt"
	"io"
)

const (
	appName        = "orc"
	defaultVersion = "dev"
)

var version = defaultVersion

// Execute runs the orc command with explicit streams for deterministic tests.
func Execute(args []string, stdout, stderr io.Writer) error {
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

func printHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s is the Tiny LLM Orchestrator control plane.

Usage:
  %s [command]

Available Commands:
  help        Show command help
  version     Print version information

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}
