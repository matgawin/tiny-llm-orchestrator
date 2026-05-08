package cli

import (
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

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
