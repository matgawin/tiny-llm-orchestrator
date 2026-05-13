package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/runinspect"
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) > 0 {
				return runFlagError(cmd, stderr, "start", fmt.Errorf("unexpected argument %q", args[0]))
			}
			return executeRunStart(runStartArgs(workflow, bead, fallbackTaskFile, taskFile, task, taskStdin), stdin, stdout, stderr)
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

func runStartArgs(workflow, bead, fallbackTaskFile, taskFile, task string, taskStdin bool) []string {
	var args []string
	appendStringFlag := func(name, value string) {
		if value != "" {
			args = append(args, name, value)
		}
	}
	appendStringFlag("--workflow", workflow)
	appendStringFlag("--bead", bead)
	appendStringFlag("--fallback-task-file", fallbackTaskFile)
	appendStringFlag("--task-file", taskFile)
	appendStringFlag("--task", task)
	if taskStdin {
		args = append(args, "--task-stdin")
	}
	return args
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) != 1 || args[0] == "" {
				return runAdvanceFlagError(stderr, fmt.Errorf("requires <run-id>"))
			}
			advanceArgs := []string{args[0], "--max-steps", strconv.Itoa(maxSteps)}
			if once {
				advanceArgs = append(advanceArgs, "--once")
			}
			if jsonOutput {
				advanceArgs = append(advanceArgs, "--json")
			}
			return executeRunAdvance(advanceArgs, stdout, stderr)
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) != 1 || args[0] == "" {
				return runContinueFlagError(stderr, fmt.Errorf("requires <run-id>"))
			}
			continueArgs := []string{args[0]}
			if allowLoopCap {
				continueArgs = append(continueArgs, "--allow-loop-cap")
			}
			if resolveBlock {
				continueArgs = append(continueArgs, "--resolve-block")
			}
			for _, value := range reason.Values {
				continueArgs = append(continueArgs, "--reason", value)
			}
			return executeRunContinue(continueArgs, stdout, stderr)
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) != 1 || args[0] == "" {
				return runSkipStepFlagError(stderr, fmt.Errorf("requires <run-id>"))
			}
			skipArgs := []string{args[0]}
			for _, value := range step.Values {
				skipArgs = append(skipArgs, "--step", value)
			}
			for _, value := range reason.Values {
				skipArgs = append(skipArgs, "--reason", value)
			}
			return executeRunSkipStep(skipArgs, stdout, stderr)
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) != 1 || args[0] == "" {
				return runAddFollowupFlagError(stderr, fmt.Errorf("requires <run-id>"))
			}
			followupArgs := []string{args[0]}
			if title != "" {
				followupArgs = append(followupArgs, "--title", title)
			}
			if details != "" {
				followupArgs = append(followupArgs, "--details", details)
			}
			return executeRunAddFollowup(followupArgs, stdout, stderr)
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
			if len(args) != 1 || args[0] == "" {
				return runRecordSummaryFlagError(stderr, fmt.Errorf("requires <run-id>"))
			}
			summaryArgs := []string{args[0]}
			if file != "" {
				summaryArgs = append(summaryArgs, "--file", file)
			}
			return executeRunRecordSummary(summaryArgs, stdout, stderr)
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
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
			if len(args) == 1 && args[0] == helpCommand {
				return cmd.Help()
			}
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
		return writeErr
	}
	cmd.SetOut(stderr)
	if usageErr := cmd.Usage(); usageErr != nil {
		return usageErr
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
