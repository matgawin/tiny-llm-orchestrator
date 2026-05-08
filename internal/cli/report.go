package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"tiny-llm-orchestrator/orc/internal/report"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

func executeReport(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printReportHelp(stdout)
	}
	opts := report.Options{}
	stringFlags := map[string]*string{
		"--json-file":   &opts.JSONFile,
		"--run":         &opts.Report.RunID,
		"--step":        &opts.Report.StepID,
		"--agent":       &opts.Report.AgentID,
		"--attempt":     &opts.Report.AttemptID,
		"--status":      &opts.Report.Status,
		"--result":      &opts.Report.Result,
		"--summary":     &opts.Report.Summary,
		"--report-file": &opts.ReportFile,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if target, ok := stringFlags[arg]; ok {
			if !assignFlagValue(args, &i, target) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
			continue
		}
		switch arg {
		case "-h", helpFlag, helpCommand:
			return printReportHelp(stdout)
		case "--changed-path":
			if !appendFlagValue(args, &i, &opts.Report.ChangedPaths) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "--command":
			if !appendFlagValue(args, &i, &opts.Report.Commands) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "--test":
			if !appendFlagValue(args, &i, &opts.Report.Tests) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "--risk":
			if !appendFlagValue(args, &i, &opts.Report.Risks) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "--follow-up":
			var title string
			if !assignFlagValue(args, &i, &title) {
				return reportFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
			opts.Report.Followups = append(opts.Report.Followups, runstore.Followup{Title: title})
		default:
			return reportFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	opts.Root = root
	result, err := report.Submit(context.Background(), opts)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s report: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if result.Ignored {
		_, err = fmt.Fprintf(stdout, "ignored report for run %s\n", result.RunID)
		return err
	}
	if _, err := fmt.Fprintf(stdout, "recorded report for run %s attempt %s\n", result.RunID, result.Attempt.AttemptID); err != nil {
		return err
	}
	return nil
}

func reportFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s report: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printReportHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}
