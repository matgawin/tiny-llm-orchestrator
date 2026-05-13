package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

func newInitCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	opts := initconfig.Options{
		Stdin:  stdin,
		Stdout: stdout,
	}
	cmd := &cobra.Command{
		Use:           "init",
		Short:         "Scaffold project-local Tiny Orc config",
		Long:          initHelpLong(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) > 0 {
				return initFlagError(cmd, stderr, fmt.Errorf("unexpected argument %q", args[0]))
			}
			return executeInit(opts, stderr)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&opts.DryRun, "dry-run", false, "Print planned changes without writing files")
	flags.BoolVar(&opts.Yes, "yes", false, "Create missing scaffold files without prompts")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return initFlagError(cmd, stderr, err)
	})
	return cmd
}

func executeInit(opts initconfig.Options, stderr io.Writer) error {
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

func initFlagError(cmd *cobra.Command, stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s init: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	cmd.SetOut(stderr)
	if usageErr := cmd.Usage(); usageErr != nil {
		return usageErr
	}
	return fmt.Errorf("%s init: %w", appName, err)
}
