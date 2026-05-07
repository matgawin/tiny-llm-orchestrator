package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/initconfig"
	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/report"
	"tiny-llm-orchestrator/orc/internal/runinspect"
	"tiny-llm-orchestrator/orc/internal/runstart"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/runsummary"
	"tiny-llm-orchestrator/orc/internal/sandbox"
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
	case "sandbox":
		return executeSandbox(args[1:], stdin, stdout, stderr)
	case "worker":
		return executeWorker(args[1:], stdout, stderr)
	case "report":
		return executeReport(args[1:], stdout, stderr)
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

// ExitCode returns the process exit code represented by err.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var sandboxExit sandbox.ExitError
	if errors.As(err, &sandboxExit) {
		return sandboxExit.Code
	}
	return 1
}

func executeSandbox(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printSandboxHelp(stdout)
	}
	switch args[0] {
	case "-h", helpFlag, helpCommand:
		return printSandboxHelp(stdout)
	case "run":
		return executeSandboxRun(args[1:], stdin, stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "%s sandbox: unknown command %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printSandboxHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown sandbox command: %s", args[0])
	}
}

func executeSandboxRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
			return printSandboxRunHelp(stdout)
		}
		if _, err := fmt.Fprintf(stderr, "%s sandbox run: unexpected argument %q\n\n", appName, args[0]); err != nil {
			return err
		}
		if err := printSandboxRunHelp(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unexpected sandbox run argument: %s", args[0])
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restoreSignals := context.AfterFunc(ctx, stop)
	defer restoreSignals()
	if err := sandbox.Run(ctx, sandbox.Options{
		Root:   root,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s sandbox run: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	return nil
}

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
	case "add-followup":
		return executeRunAddFollowup(args[1:], stdout, stderr)
	case "continue":
		return executeRunContinue(args[1:], stdout, stderr)
	case "start":
		return executeRunStart(args[1:], stdin, stdout, stderr)
	case "show":
		return executeRunInspect("show", args[1:], stdout, stderr, runinspect.Status)
	case "status":
		return executeRunInspect("status", args[1:], stdout, stderr, runinspect.Status)
	case "next":
		return executeRunInspect("next", args[1:], stdout, stderr, runinspect.Next)
	case "record-summary":
		return executeRunRecordSummary(args[1:], stdout, stderr)
	case "summary-context":
		return executeRunInspect("summary-context", args[1:], stdout, stderr, runinspect.SummaryContext)
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

func executeRunContinue(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runContinueFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunContinueHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runContinueFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	allowLoopCap := false
	resolveBlock := false
	var reasons []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--allow-loop-cap":
			allowLoopCap = true
		case "--resolve-block":
			resolveBlock = true
		case "--reason":
			var reason string
			if !assignFlagValue(args, &i, &reason) {
				return runContinueFlagError(stderr, fmt.Errorf("--reason requires a value"))
			}
			reasons = append(reasons, reason)
		case "-h", helpFlag, helpCommand:
			return printRunContinueHelp(stdout)
		default:
			if value, ok := strings.CutPrefix(arg, "--reason="); ok {
				reasons = append(reasons, value)
				continue
			}
			return runContinueFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if allowLoopCap && resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("--resolve-block and --allow-loop-cap are mutually exclusive continuation modes"))
	}
	if len(reasons) > 1 {
		return runContinueFlagError(stderr, fmt.Errorf("repeated --reason flags are ambiguous"))
	}
	if len(reasons) > 0 && !resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("--reason is only valid with --resolve-block"))
	}
	if resolveBlock && len(reasons) == 0 {
		return runContinueFlagError(stderr, fmt.Errorf("--reason is required for --resolve-block"))
	}
	if !allowLoopCap && !resolveBlock {
		return runContinueFlagError(stderr, fmt.Errorf("choose one continuation mode: --allow-loop-cap or --resolve-block --reason <text>"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := runstore.Open(root)
	if err != nil {
		return err
	}
	if resolveBlock {
		reason := strings.TrimSpace(reasons[0])
		if reason == "" {
			return runContinueFlagError(stderr, fmt.Errorf("--reason is required for --resolve-block and must be non-empty after trimming"))
		}
		status, event, err := store.ResolveHumanBlock(runID, reason, time.Time{})
		if err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
				return writeErr
			}
			return err
		}
		if status.Continued == nil {
			return fmt.Errorf("run %q resolve-block continuation was not persisted", runID)
		}
		_, err = fmt.Fprintf(stdout, "continued run %s after human-resolved block; retrying step %s from attempt %s at event %d\n", runID, status.Continued.ResolvedStepID, status.Continued.ResolvedAttemptID, event.Sequence)
		return err
	}
	status, _, err := store.AllowWorkflowLoopHardCap(runID, "allow_loop_cap", time.Time{})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil {
		return fmt.Errorf("run %q loop-cap override was not persisted", runID)
	}
	_, err = fmt.Fprintf(stdout, "continued run %s after workflow loop hard cap; allowed one entry into %s at count %d\n", runID, override.TargetState, override.CountAfterOverride)
	return err
}

func executeRunRecordSummary(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunRecordSummaryHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	var file string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--file":
			if !assignFlagValue(args, &i, &file) {
				return runRecordSummaryFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
		case "-h", helpFlag, helpCommand:
			return printRunRecordSummaryHelp(stdout)
		default:
			return runRecordSummaryFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if strings.TrimSpace(file) == "" {
		return runRecordSummaryFlagError(stderr, fmt.Errorf("--file is required"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := runsummary.Record(context.Background(), runsummary.Options{
		Root:  root,
		RunID: runID,
		File:  file,
	})
	if err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run record-summary: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if _, err := fmt.Fprintf(stdout, "recorded final summary for run %s at %s\n", result.RunID, result.SummaryRef.Path); err != nil {
		return err
	}
	return nil
}

func executeRunAddFollowup(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runAddFollowupFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	if args[0] == "-h" || args[0] == helpFlag || args[0] == helpCommand {
		return printRunAddFollowupHelp(stdout)
	}
	runID := args[0]
	if runID == "" {
		return runAddFollowupFlagError(stderr, fmt.Errorf("requires <run-id>"))
	}
	var title, details string
	stringFlags := map[string]*string{
		"--title":   &title,
		"--details": &details,
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if target, ok := stringFlags[arg]; ok {
			if !assignFlagValue(args, &i, target) {
				return runAddFollowupFlagError(stderr, fmt.Errorf("%s requires a value", arg))
			}
			continue
		}
		switch arg {
		case "-h", helpFlag, helpCommand:
			return printRunAddFollowupHelp(stdout)
		default:
			return runAddFollowupFlagError(stderr, fmt.Errorf("unknown flag %q", arg))
		}
	}
	if strings.TrimSpace(title) == "" {
		return runAddFollowupFlagError(stderr, fmt.Errorf("--title is required"))
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := runstore.Open(root)
	if err != nil {
		return err
	}
	if _, err := store.RecordFollowup(runID, runstore.RecordFollowupRequest{
		Followup: runstore.Followup{
			Title:   title,
			Details: details,
		},
		Source: runstore.FollowupSourceOrchestrator,
	}); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%s run add-followup: %v\n", appName, err); writeErr != nil {
			return writeErr
		}
		return err
	}
	if _, err := fmt.Fprintf(stdout, "recorded follow-up for run %s\n", runID); err != nil {
		return err
	}
	return nil
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

func appendFlagValue(args []string, index *int, target *[]string) bool {
	var value string
	if !assignFlagValue(args, index, &value) {
		return false
	}
	*target = append(*target, value)
	return true
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

func runStartFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run start: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunStartHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}

func runAddFollowupFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run add-followup: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunAddFollowupHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}

func runContinueFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run continue: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunContinueHelp(stderr); helpErr != nil {
		return helpErr
	}
	return err
}

func runRecordSummaryFlagError(stderr io.Writer, err error) error {
	if _, writeErr := fmt.Fprintf(stderr, "%s run record-summary: %v\n\n", appName, err); writeErr != nil {
		return writeErr
	}
	if helpErr := printRunRecordSummaryHelp(stderr); helpErr != nil {
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
  report      Validate and persist a worker report
  run         Manage orchestration runs
  sandbox     Run configured commands through bubblewrap
  worker      Launch and supervise worker attempts
  version     Print version information

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printSandboxHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s sandbox runs configured commands through bubblewrap.

Usage:
  %s sandbox [command]

Available Commands:
  run  Run sandbox.command.argv from .orc/config.yaml through bwrap

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printSandboxRunHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s sandbox run launches the configured sandbox command through the system bwrap binary.

Usage:
  %s sandbox run

The sandbox command must be declared as sandbox.command.argv in .orc/config.yaml.
orc sandbox run is Linux-only, requires bubblewrap on PATH, and refuses to run the command unsandboxed.
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
  add-followup     Record out-of-scope follow-up work
  continue         Continue after an explicit human-reviewed stop
  next             Inspect the next workflow action without launching it
  record-summary   Record a final ready-for-review summary
  show             Show persisted run state
  start            Start a run from explicit task context
  status           Show persisted run state
  summary-context  Render persisted review context

Flags:
  -h, --help  Show command help
`, appName, appName)

	return err
}

func printRunContinueHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run continue resumes a human-reviewed stopped run.

Usage:
  %s run continue <run-id> --allow-loop-cap
  %s run continue <run-id> --resolve-block --reason <text>

Flags:
      --allow-loop-cap  Explicitly allow one continuation after a hard workflow loop stop
      --resolve-block   Retry a non-loop blocked_for_human step after human resolution
      --reason <text>   Required human attestation for --resolve-block
  -h, --help            Show command help

The loop-cap override is for human-reviewed continuation after a hard workflow loop stop. It allows exactly one additional routing decision into the currently blocked workflow state.
The resolve-block mode is for non-loop blocked_for_human runs where a human fixed the external problem outside Orc. It records the trimmed reason and retries the blocked step without skipping workflow routing.
`, appName, appName, appName)

	return err
}

func printRunRecordSummaryHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run record-summary records a final ready-for-review summary for a ready run.

Usage:
  %s run record-summary <run-id> --file <path>

Flags:
      --file <path>  Markdown summary file to copy into the run store
  -h, --help         Show command help
`, appName, appName)

	return err
}

func printRunAddFollowupHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s run add-followup records out-of-scope follow-up work for a run.

Usage:
  %s run add-followup <run-id> --title <title> [--details <markdown>]

Flags:
      --title <title>       Follow-up title
      --details <markdown>  Optional Markdown details
  -h, --help                Show command help
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

func printReportHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `%s report validates and persists a worker report.

Usage:
  %s report --run <run-id> --step <step-id> --agent <agent-id> --attempt <attempt-id> --status <status> --result <result> --summary <summary> [flags]
  %s report --json-file <path>

Flags:
      --json-file <path>     Read report fields from a JSON file
      --run <run-id>         Run id
      --step <step-id>       Workflow step id
      --agent <agent-id>     Agent id
      --attempt <attempt-id> Attempt id
      --status <status>      Report status
      --result <result>      Report result
      --summary <summary>    Compact report summary
      --changed-path <path>  Changed path; repeatable
      --command <command>    Command run; repeatable
      --test <test>          Test run; repeatable
      --risk <risk>          Risk or caveat; repeatable
      --follow-up <title>    Follow-up suggestion title; repeatable
      --report-file <path>   Markdown detail file to copy into the run store
  -h, --help                 Show command help
`, appName, appName, appName)

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
