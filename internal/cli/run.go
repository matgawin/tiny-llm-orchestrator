package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/runinspect"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func newRunCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "run",
		Short:         "Manage orchestration runs",
		Long:          appName + " run manages orchestration runs.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.DisableSuggestions = true
	cmd.AddCommand(
		newRunAddFollowupCommand(stdout, stderr),
		newRunAdvanceCommand(stdout, stderr),
		newRunConfigCommand(stdout, stderr),
		newRunContinueCommand(stdout, stderr),
		newRunInspectCommand("next", "Inspect the next workflow action without launching it", stdout, stderr, runinspect.Next),
		newRunRefreshConfigCommand(stdout, stderr),
		newRunRecordSummaryCommand(stdout, stderr),
		newRunInspectCommand("show", "Show persisted run state", stdout, stderr, runinspect.Status),
		newRunSkipStepCommand(stdout, stderr),
		newRunStartCommand(stdin, stdout, stderr),
		newRunInspectCommand("status", "Show persisted run state", stdout, stderr, runinspect.Status),
		newRunInspectCommand("summary-context", "Render persisted review context", stdout, stderr, runinspect.SummaryContext),
	)
	return cmd
}

func newRunStartCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	var workflow, bead, fallbackTaskFile, taskFile, task string
	var taskStdin bool
	cmd := &cobra.Command{
		Use:   "start --workflow <name> (--bead <id> [--fallback-task-file <path>] | --task-file <path> | --task <markdown> | --task-stdin)",
		Short: "Start a run from explicit task context",
		Long: appName + ` run start creates a durable run from explicit task context.

Usage:
  ` + appName + ` run start --workflow <name> (--bead <id> [--fallback-task-file <path>] | --task-file <path> | --task <markdown> | --task-stdin)`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return runFlagError(cmd, stderr, "start", stableerr.Errorf("unexpected argument %q", args[0]))
			}
			return executeRunStart(runstartOptions(workflow, bead, fallbackTaskFile, taskFile, task, taskStdin, stdin), stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&workflow, "workflow", "", "Workflow to start")
	flags.StringVar(&bead, "bead", "", "Read bead context through bd without mutating beads")
	flags.StringVar(&fallbackTaskFile, "fallback-task-file", "", "Markdown task file to use if explicit bead lookup fails")
	flags.StringVar(&taskFile, "task-file", "", "Markdown task file to snapshot")
	flags.StringVar(&task, "task", "", "Inline Markdown task context")
	flags.BoolVar(&taskStdin, "task-stdin", false, "Read Markdown task context from stdin")
	setRunFlagError(cmd, stderr, "start")
	return cmd
}

func newRunAdvanceCommand(stdout, stderr io.Writer) *cobra.Command {
	maxSteps := launcher.DefaultAdvanceMaxSteps
	var once, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "advance <run-id>",
		Short: "Launch workflow-selected workers until a conservative stop",
		Long: appName + ` run advance launches workflow-selected worker attempts until the run reaches a normal completion point or needs operator attention.

Usage:
  ` + appName + ` run advance <run-id> [--max-steps N] [--once] [--json]

With --json, progress and launcher diagnostics are written to stderr so stdout contains only the final JSON object.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return runFlagError(cmd, stderr, "advance", stableerr.Errorf("requires <run-id>"))
			}
			if maxSteps < 1 {
				return runFlagError(cmd, stderr, "advance", stableerr.Errorf("--max-steps must be a positive integer"))
			}
			return executeRunAdvance(args[0], maxSteps, once, jsonOutput, stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.IntVar(&maxSteps, "max-steps", maxSteps, "Stop before launching another worker after N launched attempts")
	flags.BoolVar(&once, "once", false, "Launch at most one selected worker attempt")
	flags.BoolVar(&jsonOutput, "json", false, "Emit one JSON result object on stdout after the command stops")
	setRunFlagError(cmd, stderr, "advance")
	return cmd
}

func newRunContinueCommand(stdout, stderr io.Writer) *cobra.Command {
	var allowLoopCap, resolveBlock bool
	reason := trackedStringFlag{}
	cmd := &cobra.Command{
		Use:   "continue <run-id>",
		Short: "Continue after an explicit human-reviewed stop",
		Long: appName + ` run continue resumes a human-reviewed stopped run.

Usage:
  ` + appName + ` run continue <run-id> --allow-loop-cap
  ` + appName + ` run continue <run-id> --resolve-block --reason <text>`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("requires <run-id>"))
			}
			if allowLoopCap && resolveBlock {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("--resolve-block and --allow-loop-cap are mutually exclusive continuation modes"))
			}
			if len(reason.Values) > 1 {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("repeated --reason flags are ambiguous"))
			}
			if len(reason.Values) > 0 && !resolveBlock {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("--reason is only valid with --resolve-block"))
			}
			if resolveBlock && len(reason.Values) == 0 {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("--reason is required for --resolve-block"))
			}
			if !allowLoopCap && !resolveBlock {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("choose one continuation mode: --allow-loop-cap or --resolve-block --reason <text>"))
			}
			if resolveBlock && strings.TrimSpace(reason.Values[0]) == "" {
				return runFlagError(cmd, stderr, "continue", stableerr.Errorf("--reason is required for --resolve-block and must be non-empty after trimming"))
			}
			return executeRunContinue(args[0], allowLoopCap, resolveBlock, reason.Values, stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&allowLoopCap, "allow-loop-cap", false, "Explicitly allow one continuation after a hard workflow loop stop")
	flags.BoolVar(&resolveBlock, "resolve-block", false, "Retry a non-loop blocked_for_human step after human resolution")
	flags.Var(&reason, "reason", "Required human attestation for --resolve-block")
	setRunFlagError(cmd, stderr, "continue")
	return cmd
}

func newRunSkipStepCommand(stdout, stderr io.Writer) *cobra.Command {
	step := trackedStringFlag{}
	reason := trackedStringFlag{}
	cmd := &cobra.Command{
		Use:   "skip-step <run-id>",
		Short: "Skip the currently selected skippable workflow step",
		Long: appName + ` run skip-step records an explicit human decision to bypass the currently selected skippable workflow step.

Usage:
  ` + appName + ` run skip-step <run-id> --step <step-id> --reason <text>

The command only skips the current runnable select_step decision. It rejects active worker attempts, retry, wait, terminal decisions, terminal runs, non-skippable steps, and step ids other than the selected step.
The persisted outcome is the system-owned done/skipped transition declared by workflow config. Workers cannot report done/skipped, and this command does not imply review approval unless the workflow's done/skipped route says so.
JSON output and additional confirmation flags are not supported in v1.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return runFlagError(cmd, stderr, "skip-step", stableerr.Errorf("requires <run-id>"))
			}
			if len(step.Values) > 1 {
				return runFlagError(cmd, stderr, "skip-step", stableerr.Errorf("repeated --step flags are ambiguous"))
			}
			if len(reason.Values) > 1 {
				return runFlagError(cmd, stderr, "skip-step", stableerr.Errorf("repeated --reason flags are ambiguous"))
			}
			if len(step.Values) == 0 || strings.TrimSpace(step.Values[0]) == "" {
				return runFlagError(cmd, stderr, "skip-step", stableerr.Errorf("--step is required"))
			}
			if len(reason.Values) == 0 || strings.TrimSpace(reason.Values[0]) == "" {
				return runFlagError(cmd, stderr, "skip-step", stableerr.Errorf("--reason is required and must be non-empty after trimming"))
			}
			return executeRunSkipStep(args[0], step.Values, reason.Values, stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.Var(&step, "step", "Required workflow step id; must match the current select_step decision")
	flags.Var(&reason, "reason", "Required human reason; whitespace-only reasons are rejected")
	setRunFlagError(cmd, stderr, "skip-step")
	return cmd
}

func newRunAddFollowupCommand(stdout, stderr io.Writer) *cobra.Command {
	var title, details string
	cmd := &cobra.Command{
		Use:   "add-followup <run-id>",
		Short: "Record out-of-scope follow-up work",
		Long: appName + ` run add-followup records out-of-scope follow-up work for a run.

Usage:
  ` + appName + ` run add-followup <run-id> --title <title> [--details <markdown>]`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return runFlagError(cmd, stderr, "add-followup", stableerr.Errorf("requires <run-id>"))
			}
			if strings.TrimSpace(title) == "" {
				return runFlagError(cmd, stderr, "add-followup", stableerr.Errorf("--title is required"))
			}
			return executeRunAddFollowup(args[0], title, details, stdout, stderr)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "Follow-up title")
	flags.StringVar(&details, "details", "", "Optional Markdown details")
	setRunFlagError(cmd, stderr, "add-followup")
	return cmd
}

func newRunRecordSummaryCommand(stdout, stderr io.Writer) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "record-summary <run-id>",
		Short: "Record a final ready-for-review summary",
		Long: appName + ` run record-summary records a final ready-for-review summary for a ready run.

Usage:
  ` + appName + ` run record-summary <run-id> --file <path>`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return runFlagError(cmd, stderr, "record-summary", stableerr.Errorf("requires <run-id>"))
			}
			if strings.TrimSpace(file) == "" {
				return runFlagError(cmd, stderr, "record-summary", stableerr.Errorf("--file is required"))
			}
			return executeRunRecordSummary(args[0], file, stdout, stderr)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Markdown summary file to copy into the run store")
	setRunFlagError(cmd, stderr, "record-summary")
	return cmd
}

func newRunConfigCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "config <run-id>",
		Short:         "Inspect the current pinned config snapshot",
		Long:          appName + " run config prints the current pinned config snapshot metadata for an existing run.\n\nThe command reads the run snapshot metadata and refresh events from .orc/runs. It does not load live .orc config.\nOutput includes the current snapshot version, version directory, created time, manifest hash, source file count/hash summary, and refresh history.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRunConfig(args, stdout, stderr)
		},
	}
	setRunFlagError(cmd, stderr, "config")
	return cmd
}

func newRunRefreshConfigCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "refresh-config <run-id>",
		Short:         "Refresh an existing run to the current live .orc config",
		Long:          appName + " run refresh-config validates live .orc config and explicitly refreshes an existing run's pinned config snapshot.\n\nThe command rejects active attempts and incompatible workflow changes. There is no --force flag in v1.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRunRefreshConfig(args, stdout, stderr)
		},
	}
	setRunFlagError(cmd, stderr, "refresh-config")
	return cmd
}

func newRunInspectCommand(command, short string, stdout, stderr io.Writer, inspect func(context.Context, runinspect.Options) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:           command + " <run-id>",
		Short:         short,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRunInspect(command, args, stdout, stderr, inspect)
		},
	}
	setRunFlagError(cmd, stderr, command)
	return cmd
}

type trackedStringFlag struct {
	Values []string
}

func (f *trackedStringFlag) Set(value string) error {
	f.Values = append(f.Values, value)
	return nil
}

func (f *trackedStringFlag) String() string {
	if len(f.Values) == 0 {
		return ""
	}
	return f.Values[len(f.Values)-1]
}

func (f *trackedStringFlag) Type() string {
	return "string"
}

var _ pflag.Value = (*trackedStringFlag)(nil)

func setRunFlagError(cmd *cobra.Command, stderr io.Writer, command string) {
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return runFlagError(cmd, stderr, command, err)
	})
}

func runFlagError(cmd *cobra.Command, stderr io.Writer, command string, err error) error {
	prefix := appName + " run"
	if command != "" {
		prefix += " " + command
	}
	if _, writeErr := fmt.Fprintf(stderr, "%s: %v\n\n", prefix, err); writeErr != nil {
		return fmt.Errorf("run flag error: %w", writeErr)
	}
	cmd.SetOut(stderr)
	if usageErr := cmd.Usage(); usageErr != nil {
		return fmt.Errorf("run flag error: %w", usageErr)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
