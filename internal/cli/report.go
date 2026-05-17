package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"tiny-llm-orchestrator/orc/internal/report"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func newReportCommand(stdout, stderr io.Writer) *cobra.Command {
	opts := report.Options{}

	var followupTitles []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Validate and persist a worker report",
		Long: appName + ` report validates and persists a worker report.

Use a JSON report file or direct report flags. JSON input is validated with strict unknown-field rejection.`,
		Example: appName + ` report --run <run-id> --step <step-id> --agent <agent-id> --attempt <attempt-id> --status <status> --result <result> --summary <summary>
` + appName + ` report --json-file <path>`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return reportFlagError(cmd, stderr, stableerr.Errorf("unexpected argument %q", args[0]))
			}

			if len(args) == 0 && noReportFlagsChanged(cmd) {
				return cmd.Help()
			}

			opts.Report.Followups = opts.Report.Followups[:0]
			for _, title := range followupTitles {
				opts.Report.Followups = append(opts.Report.Followups, runstore.Followup{Title: title})
			}

			return executeReport(opts, stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.JSONFile, "json-file", "", "Read report fields from a JSON file")
	flags.StringVar(&opts.Report.RunID, "run", "", "Run id")
	flags.StringVar(&opts.Report.StepID, "step", "", "Workflow step id")
	flags.StringVar(&opts.Report.AgentID, "agent", "", "Agent id")
	flags.StringVar(&opts.Report.AttemptID, "attempt", "", "Attempt id")
	flags.StringVar(&opts.Report.Status, "status", "", "Report status")
	flags.StringVar(&opts.Report.Result, "result", "", "Report result")
	flags.StringVar(&opts.Report.Summary, "summary", "", "Compact report summary")
	flags.StringArrayVar(&opts.Report.ChangedPaths, "changed-path", nil, "Changed path; repeatable")
	flags.StringArrayVar(&opts.Report.Commands, "command", nil, "Command run; repeatable")
	flags.StringArrayVar(&opts.Report.Tests, "test", nil, "Test run; repeatable")
	flags.StringArrayVar(&opts.Report.Risks, "risk", nil, "Risk or caveat; repeatable")
	flags.StringArrayVar(&followupTitles, "follow-up", nil, "Follow-up suggestion title; repeatable")
	flags.StringVar(&opts.ReportFile, "report-file", "", "Markdown detail file to copy into the run store")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return reportFlagError(cmd, stderr, err)
	})

	return cmd
}

func noReportFlagsChanged(cmd *cobra.Command) bool {
	return cmd.Flags().NFlag() == 0
}

func executeReport(opts report.Options, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("execute report: %w", err)
	}

	opts.Root = root

	result, err := report.Submit(context.Background(), opts)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s report: %v\n", appName, err); writeErr != nil {
			return fmt.Errorf("execute report: %w", writeErr)
		}

		return fmt.Errorf("execute report: %w", err)
	}

	if result.Ignored {
		_, err = fmt.Fprintf(stdout, "ignored report for run %s\n", result.RunID)
		if err != nil {
			return fmt.Errorf("execute report: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintf(stdout, "recorded report for run %s attempt %s\n", result.RunID, result.Attempt.AttemptID); err != nil {
		return fmt.Errorf("execute report: %w", err)
	}

	return nil
}

func reportFlagError(cmd *cobra.Command, stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s report: %v\n\n", appName, err); writeErr != nil {
		return fmt.Errorf("report flag error: %w", writeErr)
	}

	cmd.SetOut(stderr)

	if helpErr := cmd.Usage(); helpErr != nil {
		return fmt.Errorf("report flag error: %w", helpErr)
	}

	return fmt.Errorf("%s report: %w", appName, err)
}
