package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	launcherStatusDone  = workflow.ReportStatusDone
	launcherResultReady = "ready"
	launcherCodeStep    = "code"
)

type launchOutcome struct {
	result Result
	err    error
}

func seedLauncherAttempt(t *testing.T, store *runstore.Store, runID, attemptID string, timeout time.Duration, startedAt time.Time) runstore.Attempt {
	t.Helper()
	attempt, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         timeout,
		ReportExitGrace: 30 * time.Millisecond,
		Time:            startedAt,
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	return attempt
}

func seedProcessedLauncherAttempt(t *testing.T, store *runstore.Store, runID, attemptID, stepID, agentID string) runstore.Attempt {
	t.Helper()
	attempt, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          stepID,
		AgentID:         agentID,
		AttemptID:       attemptID,
		Timeout:         200 * time.Millisecond,
		ReportExitGrace: 30 * time.Millisecond,
		Time:            fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              os.Getpid(),
		ProcessStartTime: currentProcessStartTime(t),
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	return attempt
}

func createLauncherRun(t *testing.T, timeout string) (string, string) {
	t.Helper()
	return createLauncherRunWithOptions(t, timeout, launcherRunOptions{TaskContext: true})
}

func createLauncherRunWithoutTask(t *testing.T, timeout string) (string, string) {
	t.Helper()
	return createLauncherRunWithOptions(t, timeout, launcherRunOptions{})
}

type launcherRunOptions struct {
	TaskContext     bool
	TwoStep         bool
	Retries         map[string]int
	ReportExitGrace string
}

type commandWorkflowOptions struct {
	Timeout        string
	Kind           string
	Argv           []string
	ScriptPath     string
	ScriptArgs     []string
	Env            map[string]string
	AllowedResults string
	On             string
}

func createCommandLauncherRun(t *testing.T, opts commandWorkflowOptions) (string, string) {
	t.Helper()
	if opts.Timeout == "" {
		opts.Timeout = "200ms"
	}
	if opts.Kind == "" {
		opts.Kind = config.StepKindCommand
	}
	if opts.Kind == config.StepKindCommand && len(opts.Argv) == 0 {
		opts.Argv = []string{"sh", "-c", "true"}
	}
	root := t.TempDir()
	writeCommandLauncherProject(t, root, opts)
	store := openLauncherStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "launcher-run",
		Workflow:     "implementation",
		InitialState: "check",
		Time:         fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeLauncherConfigSnapshot(t, root, store, run.ID)
	return root, run.ID
}

func createLauncherRunWithOptions(t *testing.T, timeout string, opts launcherRunOptions) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeLauncherProject(t, root, timeout, opts)
	store := openLauncherStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "launcher-run",
		Workflow:     "implementation",
		InitialState: "plan",
		Time:         fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeLauncherConfigSnapshot(t, root, store, run.ID)
	if opts.TaskContext {
		if _, err := store.WriteArtifact(run.ID, runstore.Artifact{
			Kind:    runstore.KindTaskContext,
			Name:    "task",
			Content: []byte("# Task\n\nLaunch a worker.\n"),
			Time:    fixedLauncherTime(),
		}); err != nil {
			t.Fatalf("WriteArtifact task returned error: %v", err)
		}
	}
	return root, run.ID
}

func createLoopCapLauncherRun(t *testing.T, defaultCaps, workflowCaps string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeLoopCapLauncherProject(t, root, defaultCaps, workflowCaps)
	store := openLauncherStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "loop-cap-run",
		Workflow:     "implementation",
		InitialState: "plan",
		Time:         fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeLauncherConfigSnapshot(t, root, store, run.ID)
	if _, err := store.WriteArtifact(run.ID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte("# Task\n\nBreak the workflow loop.\n"),
		Time:    fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("WriteArtifact task returned error: %v", err)
	}
	return root, run.ID
}

func writeLauncherConfigSnapshot(t *testing.T, root string, store *runstore.Store, runID string) {
	t.Helper()
	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}
	snapshot, err := configsnapshot.BuildInitial(project, "implementation", fixedLauncherTime())
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}
	if err := store.WriteInitialConfigSnapshot(runID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
}

func writeLoopCapLauncherProject(t *testing.T, root, defaultCaps, workflowCaps string) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	writeLauncherRuntime(t, orcDir)
	var configYAML strings.Builder
	configYAML.WriteString("version: 1\n")
	if strings.TrimSpace(defaultCaps) != "" {
		configYAML.WriteString("defaults:\n  loop_caps:\n")
		for line := range strings.SplitSeq(strings.TrimRight(defaultCaps, "\n"), "\n") {
			configYAML.WriteString("    " + line + "\n")
		}
	}
	configYAML.WriteString("workflows:\n  implementation:\n    path: workflows/implementation.yaml\n")
	if strings.TrimSpace(workflowCaps) != "" {
		configYAML.WriteString("    loop_caps:\n")
		for line := range strings.SplitSeq(strings.TrimRight(workflowCaps, "\n"), "\n") {
			configYAML.WriteString("      " + line + "\n")
		}
	}
	configYAML.WriteString("agents:\n  planner: agents/planner.md\nruntimes:\n  codex: runtimes/codex.yaml\n")
	writeLauncherFile(t, filepath.Join(orcDir, "config.yaml"), configYAML.String())
	writeLauncherFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Test planner.\n---\n\nPlan.\n")
	writeLauncherFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 200ms
  report_exit_grace: 30ms
  runtime: codex
  retries: {}
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
      failed: [missing_report, timeout]
    on:
      done/ready: plan
      failed/missing_report: blocked_for_human
      failed/timeout: blocked_for_human
`)
}

func seedReportedLoopAttempt(t *testing.T, store *runstore.Store, runID, attemptID, consumeAttemptID string) {
	t.Helper()
	req := runstore.StartAttemptRequest{
		StepID:           "plan",
		AgentID:          "planner",
		AttemptID:        attemptID,
		Timeout:          200 * time.Millisecond,
		ReportExitGrace:  30 * time.Millisecond,
		Time:             fixedLauncherTime(),
		ConsumeAttemptID: consumeAttemptID,
	}
	if consumeAttemptID != "" {
		req.WorkflowStateEntry = runstore.WorkflowStateEntryRequest{
			State:         "plan",
			PreviousState: "plan",
			TriggerStatus: launcherStatusDone,
			TriggerResult: launcherResultReady,
		}
	}
	if _, _, err := store.StartAttempt(runID, req); err != nil {
		t.Fatalf("StartAttempt %s returned error: %v", attemptID, err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
		Time:    fixedLauncherTime().Add(250 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt %s returned error: %v", attemptID, err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedLauncherTime().Add(300 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt %s returned error: %v", attemptID, err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan",
		Content: []byte("log\n"),
		Time:    fixedLauncherTime().Add(350 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log %s returned error: %v", attemptID, err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedLauncherTime().Add(400 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptLog %s returned error: %v", attemptID, err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedLauncherTime().Add(500 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess %s returned error: %v", attemptID, err)
	}
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: attemptID,
			Status:    launcherStatusDone,
			Result:    launcherResultReady,
			Summary:   "Ready to continue.",
		},
		Time: fixedLauncherTime().Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptReport %s returned error: %v", attemptID, err)
	}
}

func writeLauncherProject(t *testing.T, root, timeout string, opts launcherRunOptions) {
	t.Helper()
	reportExitGrace := opts.ReportExitGrace
	if reportExitGrace == "" {
		reportExitGrace = "30ms"
	}
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		Timeout:          timeout,
		ReportExitGrace:  reportExitGrace,
		FailedResults:    []string{resultMissingReport, resultProcessError, resultTimeout},
		TwoStep:          opts.TwoStep,
		Retries:          opts.Retries,
	})
}

func writeCommandLauncherProject(t *testing.T, root string, opts commandWorkflowOptions) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	writeLauncherRuntime(t, orcDir)
	writeLauncherFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  coder: agents/coder.md\nruntimes:\n  codex: runtimes/codex.yaml\n")
	writeLauncherFile(t, filepath.Join(orcDir, "agents", "coder.md"), "---\nid: coder\nrole: coder\ndescription: Test coder.\n---\n\nCode.\n")
	var workflowYAML strings.Builder
	workflowYAML.WriteString("name: implementation\nstart: check\nexecution:\n  mode: sequential\ntask_context:\n  beads: optional\n  markdown_fallback: true\ndefaults:\n  timeout: " + opts.Timeout + "\n  report_exit_grace: 30ms\n  runtime: codex\n  retries: {}\nsteps:\n  check:\n    kind: " + opts.Kind + "\n")
	switch opts.Kind {
	case config.StepKindScript:
		workflowYAML.WriteString("    script:\n      path: " + strconv.Quote(opts.ScriptPath) + "\n")
		if len(opts.ScriptArgs) > 0 {
			workflowYAML.WriteString("      args: [")
			for i, arg := range opts.ScriptArgs {
				if i > 0 {
					workflowYAML.WriteString(", ")
				}
				workflowYAML.WriteString(strconv.Quote(arg))
			}
			workflowYAML.WriteString("]\n")
		}
	default:
		workflowYAML.WriteString("    command:\n      argv: [")
		for i, arg := range opts.Argv {
			if i > 0 {
				workflowYAML.WriteString(", ")
			}
			workflowYAML.WriteString(strconv.Quote(arg))
		}
		workflowYAML.WriteString("]\n")
	}
	if len(opts.Env) > 0 {
		workflowYAML.WriteString("    env:\n")
		keys := make([]string, 0, len(opts.Env))
		for key := range opts.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			workflowYAML.WriteString("      " + key + ": " + strconv.Quote(opts.Env[key]) + "\n")
		}
	}
	if opts.AllowedResults != "" {
		workflowYAML.WriteString(opts.AllowedResults)
	} else {
		workflowYAML.WriteString(`    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
`)
	}
	if opts.On != "" {
		workflowYAML.WriteString(opts.On)
	} else {
		workflowYAML.WriteString(`    on:
      done/passed: ready_for_human
      done/failed: code
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
`)
	}
	workflowYAML.WriteString(`  code:
    agent: coder
    allowed_results:
      done: [ready]
      failed: [missing_report, process_error, timeout]
    on:
      done/ready: ready_for_human
      failed/missing_report: blocked_for_human
      failed/process_error: blocked_for_human
      failed/timeout: blocked_for_human
`)
	writeLauncherFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), workflowYAML.String())
}

func appendLauncherSandboxConfig(t *testing.T, root string, requireForWorkers bool) {
	t.Helper()
	configPath := filepath.Join(root, ".orc", "config.yaml")
	content := string(readLauncherFile(t, configPath))
	require := "false"
	if requireForWorkers {
		require = "true"
	}
	writeLauncherFile(t, configPath, content+`sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  require_for_workers: `+require+`
`)
}

func writeLauncherRuntime(t *testing.T, orcDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(orcDir, "runtimes"), 0o750); err != nil {
		t.Fatalf("create runtimes dir: %v", err)
	}
	writeLauncherFile(t, filepath.Join(orcDir, "runtimes", "codex.yaml"), testutil.CodexRuntimeYAML())
}

func openLauncherStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func linkLauncherPromptAndLog(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	linkLauncherPromptAndLogNamed(t, store, runID, attemptID, "plan-"+attemptID)
}

func linkLauncherPromptAndLogNamed(t *testing.T, store *runstore.Store, runID, attemptID, name string) {
	t.Helper()
	_ = recordLauncherPromptNamed(t, store, runID, attemptID, name)
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    name,
		Content: []byte("log\n"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
}

func recordLauncherPrompt(t *testing.T, store *runstore.Store, runID, attemptID string) []byte {
	t.Helper()
	return recordLauncherPromptNamed(t, store, runID, attemptID, "plan-"+attemptID)
}

func recordLauncherPromptNamed(t *testing.T, store *runstore.Store, runID, attemptID, name string) []byte {
	t.Helper()
	prompt := []byte("prompt\n")
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    name,
		Content: prompt,
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	return prompt
}

func prepareRunProcessAttempt(t *testing.T, root, runID, attemptID string) (runcontext.Context, runstore.Attempt) {
	t.Helper()
	loaded, err := loadLaunchContext(context.Background(), root, runID)
	if err != nil {
		t.Fatalf("loadLaunchContext returned error: %v", err)
	}
	attempt, _, err := loaded.Store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         loaded.Workflow.Defaults.Timeout.Duration,
		ReportExitGrace: loaded.Workflow.Defaults.ReportExitGrace.Duration,
		Time:            fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	loaded.Run, err = loaded.Store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return loaded, attempt
}

type scheduledReadyReportProcess struct {
	AttemptID       string
	Timeout         string
	ReportExitGrace string
	ReportDelay     time.Duration
	Command         []string
}

type scheduledReadyReportProcessResult struct {
	Attempt runstore.Attempt
	Run     *runstore.Run
	Elapsed time.Duration
}

func runProcessWithScheduledReadyReport(t *testing.T, scenario scheduledReadyReportProcess) scheduledReadyReportProcessResult {
	t.Helper()
	opts := launcherRunOptions{TaskContext: true, ReportExitGrace: scenario.ReportExitGrace}
	root, runID := createLauncherRunWithOptions(t, scenario.Timeout, opts)
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, scenario.AttemptID)
	prompt := recordLauncherPrompt(t, loaded.Store, runID, attempt.AttemptID)
	waitForReport := scheduleReadyReportWhenActiveAfter(t, loaded.Store, runID, attempt.AttemptID, scenario.ReportDelay)

	started := time.Now()
	result, _, _, err := runProcess(context.Background(), loaded, Options{
		Root:    root,
		RunID:   runID,
		Command: scenario.Command,
		Time:    fixedLauncherTime(),
	}, attempt, promptrender.Result{Content: prompt}, fixedLauncherTime(), nil)
	elapsed := time.Since(started)
	waitForReport()
	if err != nil {
		t.Fatalf("runProcess returned error: %v", err)
	}
	if result.State != runstore.AttemptStateReported || result.Status != launcherStatusDone || result.Result != launcherResultReady {
		t.Fatalf("attempt = %+v, want valid report authoritative", result)
	}
	return scheduledReadyReportProcessResult{
		Attempt: result,
		Run:     loadLauncherRun(t, root, runID),
		Elapsed: elapsed,
	}
}

func scheduleReadyReportWhenActiveAfter(t *testing.T, store *runstore.Store, runID, attemptID string, delay time.Duration) func() {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			run, err := store.Load(runID)
			if err != nil {
				done <- err
				return
			}
			if run.Status.ActiveAttempt != nil && run.Status.ActiveAttempt.AttemptID == attemptID && run.Status.ActiveAttempt.State == runstore.AttemptStateActive {
				if delay > 0 {
					time.Sleep(delay)
				}
				err := recordReadyLauncherReport(store, run, attemptID)
				done <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		done <- stableerr.New("attempt did not become active")
	}()
	return func() {
		t.Helper()
		if err := <-done; err != nil {
			t.Fatalf("scheduled report failed: %v", err)
		}
	}
}

func recordReadyLauncherReport(store *runstore.Store, run *runstore.Run, attemptID string) error {
	_, _, err := store.RecordAttemptReport(run.ID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     run.ID,
			StepID:    run.Status.ActiveAttempt.StepID,
			AgentID:   run.Status.ActiveAttempt.AgentID,
			AttemptID: attemptID,
			Status:    launcherStatusDone,
			Result:    launcherResultReady,
			Summary:   "Reported while process is running.",
		},
		Time: fixedLauncherTime().Add(time.Second),
	})
	if err != nil {
		return fmt.Errorf("record ready launcher report: %w", err)
	}
	return nil
}

func assertLauncherWarning(t *testing.T, run *runstore.Run, attemptID, kind string) runstore.AttemptWarning {
	t.Helper()
	for _, warning := range run.Status.Warnings {
		if warning.AttemptID == attemptID && warning.Kind == kind {
			return warning
		}
	}
	t.Fatalf("warnings = %+v, want %s for attempt %s", run.Status.Warnings, kind, attemptID)
	return runstore.AttemptWarning{}
}

func assertLaunchNextBlocksWithoutRelaunch(t *testing.T, root, runID, attemptID string, expectedAttempts int) *runstore.Run {
	t.Helper()
	wantState := workflow.RunStatusBlockedForHuman
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime().Add(time.Minute),
	})
	if err == nil || !strings.Contains(err.Error(), "transitioned to "+wantState) {
		t.Fatalf("LaunchNext error = %v, want blocked transition", err)
	}
	if result.Attempt.AttemptID != attemptID {
		t.Fatalf("attempt = %+v, want original attempt %q", result.Attempt, attemptID)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.State != wantState {
		t.Fatalf("run state = %q, want %s", loaded.Status.State, wantState)
	}
	if got := len(loaded.Status.Attempts); got != expectedAttempts {
		t.Fatalf("attempt history len = %d, want no relaunch from %d attempts", got, expectedAttempts)
	}
	return loaded
}

func assertRetryLaunch(t *testing.T, root, runID, previousAttemptID string, retryAttempt runstore.Attempt, pair string, count int) {
	t.Helper()
	if retryAttempt.AttemptID == "" || retryAttempt.AttemptID == previousAttemptID {
		t.Fatalf("retry attempt id = %q, want new attempt distinct from %q", retryAttempt.AttemptID, previousAttemptID)
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := len(loaded.Status.Attempts); got != 2 {
		t.Fatalf("attempt history len = %d, want retry attempt", got)
	}
	if loaded.Status.Attempts[0].AttemptID != previousAttemptID {
		t.Fatalf("first attempt id = %q, want %q", loaded.Status.Attempts[0].AttemptID, previousAttemptID)
	}
	if loaded.Status.Attempts[0].SupersededBy != retryAttempt.AttemptID {
		t.Fatalf("first attempt superseded_by = %q, want %q", loaded.Status.Attempts[0].SupersededBy, retryAttempt.AttemptID)
	}
	if loaded.Status.RetryLineage == nil || loaded.Status.RetryLineage.Counts[pair] != count {
		t.Fatalf("retry lineage = %+v, want %s count %d", loaded.Status.RetryLineage, pair, count)
	}
}

func recordProcessForLauncherTest(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	startIdentity, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("processStartIdentity returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcessContext(context.Background(), runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              os.Getpid(),
		ProcessStartTime: startIdentity,
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcessContext returned error: %v", err)
	}
}

func holdLauncherRunLock(t *testing.T, store *runstore.Store, runID string) (<-chan struct{}, chan<- struct{}, <-chan error) {
	t.Helper()
	run, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(filepath.Join(run.Path, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			done <- err
			return
		}
		defer func() {
			_ = file.Close()
		}()
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
			done <- err
			return
		}
		close(locked)
		<-release
		done <- syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}()
	return locked, release, done
}

func runCanceledWhileLauncherRunLockHeld(t *testing.T, store *runstore.Store, runID string, cancel context.CancelFunc, beforeCancel, operation func()) {
	t.Helper()
	locked, release, lockDone := holdLauncherRunLock(t, store, runID)
	<-locked
	lockWait := observeLauncherRunLockWait(t, runID)
	go operation()
	waitForLauncherRunLockWaiter(t, lockWait)
	if beforeCancel != nil {
		beforeCancel()
	}
	cancel()
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("held lock returned error: %v", err)
	}
}

func observeLauncherRunLockWait(t *testing.T, runID string) <-chan struct{} {
	t.Helper()
	waiting := make(chan struct{})
	var once sync.Once
	cleanup := runstore.SetRunLockWaitObserverForTest(func(lockName string) {
		if lockName == runID {
			once.Do(func() {
				close(waiting)
			})
		}
	})
	t.Cleanup(cleanup)
	return waiting
}

func waitForLauncherRunLockWaiter(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("launcher goroutine did not reach the run-lock wait path")
	}
}

func loadLauncherRun(t *testing.T, root, runID string) *runstore.Run {
	t.Helper()
	run, err := openLauncherStore(t, root).Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return run
}

func readLauncherArtifact(t *testing.T, root, runID string, ref runstore.ArtifactRef) []byte {
	t.Helper()
	content, err := openLauncherStore(t, root).ReadArtifact(runID, ref)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	return content
}

func assertLogArtifactContains(t *testing.T, root string, run *runstore.Run, attemptID, stream, want string) {
	t.Helper()
	for _, ref := range run.Status.Artifacts {
		if ref.Kind != runstore.KindLog || !strings.Contains(ref.Name, attemptID+"-"+stream) {
			continue
		}
		content := readLauncherArtifact(t, root, run.ID, ref)
		if !strings.Contains(string(content), want) {
			t.Fatalf("%s log %s = %q, want %q", stream, ref.Path, content, want)
		}
		return
	}
	t.Fatalf("no %s log artifact found for attempt %s in %+v", stream, attemptID, run.Status.Artifacts)
}

func allLauncherLogs(t *testing.T, root, runID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".orc", "runs", runID, "logs", "*.log"))
	if err != nil {
		t.Fatalf("glob launcher logs: %v", err)
	}
	var out strings.Builder
	for _, path := range matches {
		out.Write(readLauncherFile(t, path))
		out.WriteByte('\n')
	}
	return out.String()
}

func writeLauncherFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeLauncherExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create executable dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o750); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func readLauncherFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func assertContainsAll(t *testing.T, output string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func fixedLauncherTime() time.Time {
	return time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
}

func currentProcessStartTime(t *testing.T) string {
	t.Helper()
	startTime, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("processStartIdentity returned error: %v", err)
	}
	return startTime
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", string(content), err)
	}
	return pid
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func descendantSleeperCommand(pidPath, sleepSeconds string) []string {
	script := "sh -c 'echo $$ > " + shellQuote(pidPath) + "; trap \"\" TERM; sleep " + sleepSeconds + "' & wait"
	return []string{"sh", "-c", script}
}

func directExitWithDescendantCommand(pidPath, exitCode string) []string {
	quotedPIDPath := shellQuote(pidPath)
	script := "sh -c 'echo $$ > " + quotedPIDPath + "; trap \"\" TERM; sleep 30' & " +
		"while [ ! -s " + quotedPIDPath + " ]; do sleep 0.01; done; exit " + exitCode
	return []string{"sh", "-c", script}
}

func envWithoutPath(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			continue
		}
		out = append(out, item)
	}
	return out
}
