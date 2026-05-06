package launcher

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	launcherStatusDone  = "done"
	launcherResultReady = "ready"
)

func TestLaunchNextPersistsPromptLogAndMissingReportAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	var stdout bytes.Buffer
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Stdout:  &stdout,
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}
	if result.Attempt.State != runstore.AttemptStateMissingReport ||
		result.Attempt.Status != "failed" ||
		result.Attempt.Result != "missing_report" {
		t.Fatalf("attempt = %+v, want failed/missing_report", result.Attempt)
	}
	if result.Attempt.PromptRef == nil || result.Attempt.LogRef == nil {
		t.Fatalf("attempt refs = prompt %+v log %+v, want both recorded", result.Attempt.PromptRef, result.Attempt.LogRef)
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	assertContainsAll(t, string(logContent), []string{
		"# Tiny Orc Worker Prompt\n",
		"- run_id: `launcher-run`\n",
		"- step_id: `plan`\n",
		"- agent_id: `planner`\n",
		"- attempt_id: `" + result.Attempt.AttemptID + "`\n",
	})
	if !strings.Contains(stdout.String(), "launched attempt "+result.Attempt.AttemptID) {
		t.Fatalf("stdout = %q, want launched attempt", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized", loaded.Status.ActiveAttempt)
	}
	if got := len(loaded.Status.Attempts); got != 1 {
		t.Fatalf("attempt history len = %d, want 1", got)
	}
}

func TestLaunchNextRoutesExhaustedSynthesizedFailureToBlocked(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	first, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("first LaunchNext returned error: %v", err)
	}

	assertLaunchNextBlocksWithoutRelaunch(t, root, runID, first.Attempt.AttemptID, 1)
}

func TestLaunchNextRoutesExhaustedTimeoutToBlocked(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	first, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "sleep 1"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("first LaunchNext returned error: %v", err)
	}
	if first.Attempt.State != runstore.AttemptStateTimedOut || first.Attempt.Result != resultTimeout {
		t.Fatalf("first attempt = %+v, want failed/timeout", first.Attempt)
	}

	assertLaunchNextBlocksWithoutRelaunch(t, root, runID, first.Attempt.AttemptID, 1)
}

func TestLaunchNextRetriesThenExhaustsSynthesizedFailure(t *testing.T) {
	root, runID := createLauncherRunWithOptions(t, "200ms", launcherRunOptions{
		TaskContext: true,
		Retries:     map[string]int{"failed/missing_report": 1},
	})
	first, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("first LaunchNext returned error: %v", err)
	}
	second, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime().Add(time.Second),
	})
	if err != nil {
		t.Fatalf("retry LaunchNext returned error: %v", err)
	}
	assertRetryLaunch(t, root, runID, first.Attempt.AttemptID, second.Attempt, "failed/missing_report", 1)

	loaded := assertLaunchNextBlocksWithoutRelaunch(t, root, runID, second.Attempt.AttemptID, 2)
	if loaded.Status.RetryLineage == nil || loaded.Status.RetryLineage.Counts["failed/missing_report"] != 1 {
		t.Fatalf("terminal retry lineage = %+v, want exhausted count preserved", loaded.Status.RetryLineage)
	}
}

func TestLaunchNextRetriesSynthesizedProcessError(t *testing.T) {
	root, runID := createLauncherRunWithOptions(t, "200ms", launcherRunOptions{
		TaskContext: true,
		Retries:     map[string]int{"failed/process_error": 1},
	})
	first, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null; exit 7"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("first LaunchNext returned error: %v", err)
	}
	second, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(time.Second),
	})
	if err != nil {
		t.Fatalf("retry LaunchNext returned error: %v", err)
	}
	assertRetryLaunch(t, root, runID, first.Attempt.AttemptID, second.Attempt, "failed/process_error", 1)
	if first.Attempt.Result != runstore.AttemptResultProcessError {
		t.Fatalf("first result = %q, want process_error", first.Attempt.Result)
	}
}

func TestLaunchNextRecordsProcessErrorForNonzeroExit(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null; exit 7"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateProcessError || result.Attempt.Result != "process_error" {
		t.Fatalf("attempt = %+v, want process_error", result.Attempt)
	}
	if result.Attempt.ExitCode == nil || *result.Attempt.ExitCode != 7 {
		t.Fatalf("exit code = %+v, want 7", result.Attempt.ExitCode)
	}
}

func TestWorkerRunnerUsesTerminalReportInsteadOfSynthesizedFinish(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "reported-attempt")
	linkLauncherPromptAndLogNamed(t, loaded.Store, runID, attempt.AttemptID, "plan")
	recordProcessForLauncherTest(t, loaded.Store, runID, attempt.AttemptID)
	reported, _, err := loaded.Store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: attempt.AttemptID,
			Status:    launcherStatusDone,
			Result:    launcherResultReady,
			Summary:   "Plan is ready.",
		},
		Time: fixedLauncherTime().Add(time.Second),
	})
	if err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	runner := workerRunner{loaded: loaded, attempt: attempt}
	got, ok, err := runner.reportTerminalAttemptAfterWait()
	if err != nil {
		t.Fatalf("reportTerminalAttemptAfterWait returned error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want terminal report detected")
	}
	if got.AttemptID != reported.AttemptID || got.State != runstore.AttemptStateReported {
		t.Fatalf("terminal attempt = %+v, want reported attempt %+v", got, reported)
	}
}

func TestRunProcessTerminatesWorkerAfterReportExitGrace(t *testing.T) {
	result := runProcessWithScheduledReadyReport(t, scheduledReadyReportProcess{
		AttemptID: "reported-grace",
		Timeout:   "5s",
		Command:   []string{"sh", "-c", "cat >/dev/null; sleep 5"},
	})
	assertLauncherWarning(t, result.Run, result.Attempt.AttemptID, warningKindPostReportGraceTerminated)
}

func TestRunProcessReportExitGraceOutlivesOriginalWorkflowTimeout(t *testing.T) {
	result := runProcessWithScheduledReadyReport(t, scheduledReadyReportProcess{
		AttemptID:       "reported-near-timeout",
		Timeout:         "200ms",
		ReportExitGrace: "250ms",
		ReportDelay:     120 * time.Millisecond,
		Command:         []string{"sh", "-c", "cat >/dev/null; sleep 5"},
	})
	if result.Elapsed < 300*time.Millisecond {
		t.Fatalf("elapsed = %s, want report-exit grace to outlive original workflow timeout", result.Elapsed)
	}
	assertLauncherWarning(t, result.Run, result.Attempt.AttemptID, warningKindPostReportGraceTerminated)
}

func TestRunProcessRecordsWarningForNonzeroExitAfterReport(t *testing.T) {
	result := runProcessWithScheduledReadyReport(t, scheduledReadyReportProcess{
		AttemptID:       "reported-nonzero",
		Timeout:         "5s",
		ReportExitGrace: "1s",
		Command:         []string{"sh", "-c", "cat >/dev/null; sleep 0.05; exit 7"},
	})
	warning := assertLauncherWarning(t, result.Run, result.Attempt.AttemptID, warningKindPostReportProcessExit)
	if warning.ExitCode == nil || *warning.ExitCode != 7 {
		t.Fatalf("warning exit_code = %+v, want 7", warning.ExitCode)
	}
	if !warning.Time.After(result.Attempt.StartedAt) {
		t.Fatalf("warning time = %s, want after attempt start %s", warning.Time, result.Attempt.StartedAt)
	}
}

func TestLaunchNextRoutesReportedOutcomeToNextWorkerStep(t *testing.T) {
	root, runID := createLauncherRunWithOptions(t, "200ms", launcherRunOptions{TaskContext: true, TwoStep: true})
	store := openLauncherStore(t, root)
	attempt := seedProcessedLauncherAttempt(t, store, runID, "reported-plan", "plan", "planner")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    attempt.StepID,
			AgentID:   attempt.AgentID,
			AttemptID: attempt.AttemptID,
			Status:    launcherStatusDone,
			Result:    launcherResultReady,
			Summary:   "Plan is ready.",
		},
		Time: fixedLauncherTime().Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}
	if result.Attempt.StepID != "code" || result.Attempt.AgentID != "coder" {
		t.Fatalf("attempt = %+v, want launched code/coder", result.Attempt)
	}
	if result.Attempt.State != runstore.AttemptStateMissingReport {
		t.Fatalf("attempt state = %q, want synthesized missing_report after code worker exits", result.Attempt.State)
	}
	loaded, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 1 {
		t.Fatalf("plan count = %d, want initial count 1", got)
	}
	if got := loaded.Status.WorkflowLoop.Counts["code"]; got != 1 {
		t.Fatalf("code count = %d, want selected next-state count 1", got)
	}
	entries := loaded.Status.WorkflowLoop.Entries
	if got := entries[len(entries)-1]; got.State != "code" || got.PreviousState != "plan" || got.TriggerStatus != launcherStatusDone || got.TriggerResult != launcherResultReady {
		t.Fatalf("last workflow entry = %+v, want code after plan done/ready", got)
	}
}

func TestLaunchNextRetriesReportedRetryStepOutcome(t *testing.T) {
	root, runID := createLauncherRunWithOptions(t, "200ms", launcherRunOptions{TaskContext: true, Retries: map[string]int{"done/ready": 1}})
	store := openLauncherStore(t, root)
	attempt := seedProcessedLauncherAttempt(t, store, runID, "reported-retry", "plan", "planner")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    attempt.StepID,
			AgentID:   attempt.AgentID,
			AttemptID: attempt.AttemptID,
			Status:    launcherStatusDone,
			Result:    launcherResultReady,
			Summary:   "Plan is ready.",
		},
		Time: fixedLauncherTime().Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	assertRetryLaunch(t, root, runID, attempt.AttemptID, result.Attempt, "done/ready", 1)
	loaded, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 1 {
		t.Fatalf("plan count = %d, want retry to preserve initial workflow count 1", got)
	}
}

func TestLaunchNextWarnsAndContinuesAtWorkflowLoopSoftCap(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 2\nhard: 4\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	var stdout bytes.Buffer
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched || result.SoftCap == nil {
		t.Fatalf("result = %+v, want launched with soft-cap warning", result)
	}
	if !strings.Contains(stdout.String(), "warning: workflow loop soft cap reached for workflow implementation state plan at count 3 (soft 2, hard 4)") {
		t.Fatalf("stdout = %q, want soft-cap warning", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 3 {
		t.Fatalf("plan count = %d, want soft-cap entry count 3", got)
	}
	if got := loaded.Status.WorkflowLoop.SoftCapWarnings; len(got) != 1 || got[0].Count != 3 || got[0].TriggerStatus != launcherStatusDone || got[0].TriggerResult != launcherResultReady {
		t.Fatalf("soft cap warnings = %+v, want one threshold warning with trigger", got)
	}
}

func TestLaunchNextBlocksBeforeWorkflowLoopHardCapIncrement(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("LaunchNext error = %v, want hard-cap block reason", err)
	}
	if result.Attempt.AttemptID != "attempt-2" {
		t.Fatalf("result attempt = %+v, want latest triggering attempt", result.Attempt)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.State != workflow.RunStatusBlockedForHuman {
		t.Fatalf("run state = %q, want blocked_for_human", loaded.Status.State)
	}
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 2 {
		t.Fatalf("plan count = %d, want hard cap to leave count at 2", got)
	}
	if got := len(loaded.Status.Attempts); got != 2 {
		t.Fatalf("attempt count = %d, want no new attempt", got)
	}
	block := loaded.Status.WorkflowLoop.HardCapBlock
	if block == nil || block.BlockedState != "plan" || block.CurrentCount != 2 || block.ProspectiveCount != 3 || block.Reason != runstore.WorkflowLoopHardCapReason {
		t.Fatalf("hard cap block = %+v, want blocked plan prospective count 3", block)
	}
}

func TestLaunchNextBypassesDisabledWorkflowLoopCaps(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: false\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatalf("Launched = false, want disabled caps to continue")
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.HardCapBlock; got != nil {
		t.Fatalf("hard cap block = %+v, want none", got)
	}
}

func TestLaunchNextUsesWorkflowSpecificLoopCapOverride(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 10\nhard: 20\n", "enabled: true\nsoft: 1\nhard: 2\n")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	_, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("LaunchNext error = %v, want workflow override hard cap", err)
	}
}

func TestRunProcessZeroExitNonReaderWithLargePromptRecordsMissingReport(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "large-prompt-non-reader")
	prompt := bytes.Repeat([]byte("x"), 2*1024*1024)
	promptRef, err := loaded.Store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan-large-prompt-non-reader",
		Content: prompt,
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := loaded.Store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: promptRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	loaded.Run = loadLauncherRun(t, root, runID)

	result, _, launched, err := runProcess(context.Background(), loaded, Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "exit 0"},
		Time:    fixedLauncherTime(),
	}, attempt, prompt, fixedLauncherTime())
	if err != nil {
		t.Fatalf("runProcess returned error: %v", err)
	}
	if !launched {
		t.Fatal("Launched = false, want true")
	}
	if result.State != runstore.AttemptStateMissingReport || result.Result != resultMissingReport {
		t.Fatalf("attempt = %+v, want missing_report despite unread large stdin", result)
	}
}

func TestLaunchNextResolvesCommandFromWorkerEnvPATHRelativeToProjectRoot(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	binDir := filepath.Join(root, "worker-bin")
	if err := os.Mkdir(binDir, 0o750); err != nil {
		t.Fatalf("mkdir worker bin: %v", err)
	}
	workerPath := filepath.Join(binDir, "env-worker")
	writeLauncherFile(t, workerPath, "#!/bin/sh\ncat >/dev/null\nprintf 'env-path-worker\\n'\n")
	if err := os.Chmod(workerPath, 0o750); err != nil {
		t.Fatalf("chmod worker: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"env-worker"},
		Env:     append(envWithoutPath(os.Environ()), "PATH=worker-bin"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	if !strings.Contains(string(logContent), "env-path-worker") {
		t.Fatalf("log = %q, want worker from Options.Env PATH", string(logContent))
	}
}

func TestResolveWorkerExecutableDoesNotFallbackWhenEnvOmitsPATH(t *testing.T) {
	_, err := resolveWorkerExecutable("sh", []string{"HOME=/tmp"}, t.TempDir())
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("resolveWorkerExecutable error = %v, want exec.ErrNotFound", err)
	}
}

func TestNewWorkerCommandUsesAbsoluteHelperPath(t *testing.T) {
	cmd, releaseExec, err := newWorkerCommand([]string{"sh", "-c", "true"}, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatalf("newWorkerCommand returned error: %v", err)
	}
	defer func() {
		_ = releaseExec(false)
	}()
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("helper path = %q, want absolute path", cmd.Path)
	}
}

func TestExecHelperClosesHandshakeFDBeforeWorkerExec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd inheritance assertion uses linux procfs")
	}
	cmd, releaseExec, err := newWorkerCommand([]string{"sh", "-c", "test ! -e /proc/$$/fd/3"}, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatalf("newWorkerCommand returned error: %v", err)
	}
	defer func() {
		_ = releaseExec(false)
	}()
	cmd.Env = append(filteredExecEnv(os.Environ()), cmd.Env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start returned error: %v", err)
	}
	if releaseErr := releaseExec(true); releaseErr != nil {
		t.Fatalf("releaseExec returned error: %v", releaseErr)
	}
	err = cmd.Wait()
	if err != nil {
		t.Fatalf("worker found inherited fd 3: %v\n%s", err, output.String())
	}
}

func TestAmbientExecHelperEnvDoesNotBypassNormalInvocation(t *testing.T) {
	if os.Getenv("ORC_LAUNCHER_AMBIENT_HELPER_TEST") == "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAmbientExecHelperEnvDoesNotBypassNormalInvocation")
	cmd.Env = append(os.Environ(),
		execHelperEnv+"=ambient-user-value",
		"ORC_LAUNCHER_AMBIENT_HELPER_TEST=1",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test binary with ambient helper env returned error: %v\n%s", err, output)
	}
}

func TestLaunchNextRecordsTimeout(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "sleep 1"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateTimedOut || result.Attempt.Result != resultTimeout {
		t.Fatalf("attempt = %+v, want timeout", result.Attempt)
	}
}

func TestLaunchNextStreamsLogBeforeWorkerExits(t *testing.T) {
	root, runID := createLauncherRun(t, "500ms")
	done := make(chan error, 1)
	go func() {
		_, err := LaunchNext(context.Background(), Options{
			Root:    root,
			RunID:   runID,
			Command: []string{"sh", "-c", "printf 'before-exit\\n'; sleep 0.2"},
			Time:    fixedLauncherTime(),
		})
		done <- err
	}()

	eventually(t, time.Second, func() bool {
		return strings.Contains(allLauncherLogs(t, root, runID), "before-exit")
	})
	loadedWhileRunning := loadLauncherRun(t, root, runID)
	if loadedWhileRunning.Status.ActiveAttempt == nil || loadedWhileRunning.Status.ActiveAttempt.LogRef == nil {
		t.Fatalf("active attempt = %+v, want log ref before worker exits", loadedWhileRunning.Status.ActiveAttempt)
	}
	if err := <-done; err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
}

func TestLaunchNextTerminalizesPromptRenderFailure(t *testing.T) {
	root, runID := createLauncherRunWithoutTask(t, "200ms")
	var stdout bytes.Buffer

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime(),
		Stdout:  &stdout,
	})
	if err == nil {
		t.Fatal("LaunchNext returned nil error, want prompt render failure")
	}
	if result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStatePromptRenderFail {
		t.Fatalf("attempt = %+v, want prompt render process_error", result.Attempt)
	}
	if !strings.Contains(stdout.String(), "result: failed/process_error") {
		t.Fatalf("stdout = %q, want terminal launch result", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized prompt failure", loaded.Status.ActiveAttempt)
	}
}

func TestLaunchNextTerminalizesProcessStartFailure(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{filepath.Join(root, "missing-worker")},
		Time:    fixedLauncherTime(),
	})
	if err == nil {
		t.Fatal("LaunchNext returned nil error, want process start failure")
	}
	if result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStateStartFailed ||
		result.Attempt.LogRef == nil {
		t.Fatalf("attempt = %+v, want process_error start failure with log", result.Attempt)
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	if !strings.Contains(string(logContent), "missing-worker") {
		t.Fatalf("log = %q, want missing-worker start error", string(logContent))
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized start failure", loaded.Status.ActiveAttempt)
	}
}

func TestLaunchNextTerminalizesEmptyCommand(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{""},
		Time:    fixedLauncherTime(),
	})
	if err == nil {
		t.Fatal("LaunchNext returned nil error, want empty command failure")
	}
	if result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStateInvalidCommand {
		t.Fatalf("attempt = %+v, want invalid command process_error", result.Attempt)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized empty command", loaded.Status.ActiveAttempt)
	}
}

func TestLaunchNextTimeoutTerminatesWorkerProcessGroup(t *testing.T) {
	root, runID := createLauncherRun(t, "80ms")
	childPIDPath := filepath.Join(root, "child.pid")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: descendantSleeperCommand(childPIDPath, "5"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateTimedOut || result.Attempt.Result != resultTimeout {
		t.Fatalf("attempt = %+v, want timeout", result.Attempt)
	}
	childPID := readPIDFile(t, childPIDPath)
	eventually(t, time.Second, func() bool {
		_, err := processStartIdentity(childPID)
		return err != nil
	})
}

func TestLaunchNextDirectExitTerminatesWorkerProcessGroupDescendants(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exitCode   string
		wantState  string
		wantResult string
	}{
		{name: "zero", exitCode: "0", wantState: runstore.AttemptStateMissingReport, wantResult: resultMissingReport},
		{name: "nonzero", exitCode: "7", wantState: runstore.AttemptStateProcessError, wantResult: resultProcessError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, runID := createLauncherRun(t, "5s")
			childPIDPath := filepath.Join(root, "direct-exit-child-"+tc.name+".pid")

			result, err := LaunchNext(context.Background(), Options{
				Root:    root,
				RunID:   runID,
				Command: directExitWithDescendantCommand(childPIDPath, tc.exitCode),
				Time:    fixedLauncherTime(),
			})
			if err != nil {
				t.Fatalf("LaunchNext returned error: %v", err)
			}
			if result.Attempt.State != tc.wantState || result.Attempt.Result != tc.wantResult {
				t.Fatalf("attempt = %+v, want %s/%s", result.Attempt, tc.wantState, tc.wantResult)
			}
			childPID := readPIDFile(t, childPIDPath)
			eventually(t, time.Second, func() bool {
				_, err := processStartIdentity(childPID)
				return err != nil
			})
		})
	}
}

func TestLaunchNextCancellationTerminatesWorkerProcessGroupAsProcessError(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	childPIDPath := filepath.Join(root, "cancel-child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan launchOutcome, 1)
	go func() {
		result, err := LaunchNext(ctx, Options{
			Root:    root,
			RunID:   runID,
			Command: descendantSleeperCommand(childPIDPath, "5"),
			Time:    fixedLauncherTime(),
		})
		done <- launchOutcome{result: result, err: err}
	}()
	eventually(t, time.Second, func() bool {
		_, err := os.Stat(childPIDPath)
		return err == nil
	})
	childPID := readPIDFile(t, childPIDPath)
	cancel()

	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("LaunchNext error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.Attempt.State != runstore.AttemptStateProcessError ||
		outcome.result.Attempt.Result != resultProcessError ||
		outcome.result.Attempt.ExitState != exitStateCanceled {
		t.Fatalf("attempt = %+v, want cancellation process_error not timeout", outcome.result.Attempt)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want cleared after cancellation", loaded.Status.ActiveAttempt)
	}
	eventually(t, time.Second, func() bool {
		_, err := processStartIdentity(childPID)
		return err != nil
	})
}

func TestLaunchNextParentDeadlineRecordsProcessErrorNotTimeout(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	childPIDPath := filepath.Join(root, "parent-deadline-child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	result, err := LaunchNext(ctx, Options{
		Root:    root,
		RunID:   runID,
		Command: descendantSleeperCommand(childPIDPath, "5"),
		Time:    fixedLauncherTime(),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LaunchNext error = %v, want context deadline", err)
	}
	if result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStateCanceled {
		t.Fatalf("attempt = %+v, want parent deadline process_error not timeout", result.Attempt)
	}
	childPID := readPIDFile(t, childPIDPath)
	eventually(t, time.Second, func() bool {
		_, err := processStartIdentity(childPID)
		return err != nil
	})
}

func TestLaunchNextCancellationBeforeStartAttemptDoesNotCreateAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	store := openLauncherStore(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan launchOutcome, 1)
	var stdout bytes.Buffer
	runCanceledWhileLauncherRunLockHeld(t, store, runID, cancel, nil, func() {
		result, err := LaunchNext(ctx, Options{
			Root:    root,
			RunID:   runID,
			Command: []string{"sh", "-c", "cat"},
			Time:    fixedLauncherTime(),
			Stdout:  &stdout,
		})
		done <- launchOutcome{result: result, err: err}
	})
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("LaunchNext error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.Attempt.AttemptID != "" {
		t.Fatalf("attempt = %+v, want no attempt", outcome.result.Attempt)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no launch result", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil || len(loaded.Status.Attempts) != 0 {
		t.Fatalf("attempt state = active %+v history %+v, want no attempt", loaded.Status.ActiveAttempt, loaded.Status.Attempts)
	}
}

func TestLaunchNextCancellationWhileStartAttemptBlockedDoesNotCreateAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	store := openLauncherStore(t, root)
	loaded, err := loadLaunchContext(root, runID)
	if err != nil {
		t.Fatalf("loadLaunchContext returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan launchOutcome, 1)
	runCanceledWhileLauncherRunLockHeld(t, store, runID, cancel, nil, func() {
		step := loaded.Workflow.Steps["plan"]
		attempt, _, err := loaded.Store.StartAttemptContext(ctx, runID, runstore.StartAttemptRequest{
			StepID:          "plan",
			AgentID:         step.Agent,
			AttemptID:       "blocked-start-attempt",
			Timeout:         loaded.Workflow.Defaults.Timeout.Duration,
			ReportExitGrace: loaded.Workflow.Defaults.ReportExitGrace.Duration,
			Time:            fixedLauncherTime(),
		})
		done <- launchOutcome{result: Result{Attempt: attempt}, err: err}
	})
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("StartAttemptContext error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.Attempt.AttemptID != "" {
		t.Fatalf("attempt = %+v, want no attempt", outcome.result.Attempt)
	}
	finalRun := loadLauncherRun(t, root, runID)
	if finalRun.Status.ActiveAttempt != nil || len(finalRun.Status.Attempts) != 0 {
		t.Fatalf("attempt state = active %+v history %+v, want no attempt", finalRun.Status.ActiveAttempt, finalRun.Status.Attempts)
	}
}

func TestRunProcessCancellationBeforeLogSetupTerminalizesWithoutSpawn(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "cancel-before-log")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	markerPath := filepath.Join(root, "spawned-before-log")

	result, _, launched, err := runProcess(ctx, loaded, Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "touch " + shellQuote(markerPath)},
		Time:    fixedLauncherTime(),
	}, attempt, []byte("prompt\n"), fixedLauncherTime())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runProcess error = %v, want context.Canceled", err)
	}
	if result.ExitState != exitStateCanceled || result.LogRef != nil {
		t.Fatalf("attempt = %+v, want canceled without log", result)
	}
	if launched {
		t.Fatal("Launched = true, want false before log setup")
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("spawn marker stat err = %v, want not spawned", statErr)
	}
}

func TestRunProcessCancellationBeforeStartTerminalizesWithoutSpawn(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "cancel-before-start")
	ctx, cancel := context.WithCancel(context.Background())
	markerPath := filepath.Join(root, "spawned-before-start")
	done := make(chan launchOutcome, 1)
	runCanceledWhileLauncherRunLockHeld(t, loaded.Store, runID, cancel, nil, func() {
		result, _, launched, err := runProcess(ctx, loaded, Options{
			Root:    root,
			RunID:   runID,
			Command: []string{"sh", "-c", "touch " + shellQuote(markerPath)},
			Time:    fixedLauncherTime(),
		}, attempt, []byte("prompt\n"), fixedLauncherTime())
		done <- launchOutcome{result: Result{Attempt: result, Launched: launched}, err: err}
	})
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("runProcess error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.Attempt.ExitState != exitStateCanceled || outcome.result.Attempt.LogRef == nil {
		t.Fatalf("attempt = %+v, want canceled with log", outcome.result.Attempt)
	}
	if outcome.result.Launched {
		t.Fatal("Launched = true, want false before worker exec")
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("spawn marker stat err = %v, want not spawned", statErr)
	}
}

func TestRunProcessCancellationWhileProcessMetadataBlockedDoesNotReleaseWorkerExec(t *testing.T) {
	root, runID := createLauncherRun(t, "5s")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "cancel-during-metadata")
	promptRef, err := loaded.Store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan-cancel-during-metadata",
		Content: []byte("prompt\n"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := loaded.Store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: promptRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	loaded.Run = loadLauncherRun(t, root, runID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	markerPath := filepath.Join(root, "exec-after-canceled-metadata")
	done := make(chan launchOutcome, 1)

	runCanceledWhileLauncherRunLockHeld(t, loaded.Store, runID, cancel, func() {
		if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
			t.Fatalf("spawn marker stat err = %v, want worker not execed while process metadata is blocked", statErr)
		}
	}, func() {
		result, _, launched, err := runProcess(ctx, loaded, Options{
			Root:    root,
			RunID:   runID,
			Command: []string{"sh", "-c", "touch " + shellQuote(markerPath)},
			Time:    fixedLauncherTime(),
		}, attempt, []byte("prompt\n"), fixedLauncherTime())
		done <- launchOutcome{result: Result{Attempt: result, Launched: launched}, err: err}
	})
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("runProcess error = %v, want context.Canceled", outcome.err)
	}
	if outcome.result.Launched {
		t.Fatal("Launched = true, want false when canceled before helper release")
	}
	if outcome.result.Attempt.State != runstore.AttemptStateProcessError || outcome.result.Attempt.ExitState != exitStateCanceled {
		t.Fatalf("attempt = %+v, want canceled process_error", outcome.result.Attempt)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("spawn marker stat err = %v, want worker not execed after cancellation", statErr)
	}
	finalRun := loadLauncherRun(t, root, runID)
	if finalRun.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized cancellation", finalRun.Status.ActiveAttempt)
	}
}

func TestLaunchNextRefusesFreshPIDLessStartingAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "starting-attempt",
		Timeout:         200 * time.Millisecond,
		ReportExitGrace: 30 * time.Millisecond,
		Time:            time.Now().UTC(),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	_, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err == nil || !strings.Contains(err.Error(), "already has starting attempt") {
		t.Fatalf("LaunchNext error = %v, want starting attempt refusal", err)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.State != runstore.AttemptStateStarting {
		t.Fatalf("active attempt = %+v, want still-starting attempt", loaded.Status.ActiveAttempt)
	}
}

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

func TestLaunchNextRecoversStalePIDLessStartingAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	seedLauncherAttempt(t, store, runID, "stale-starting-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())

	var stdout bytes.Buffer
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Recovered || result.Attempt.ExitState != exitStateUnknown {
		t.Fatalf("result = %+v, want stale starting recovery", result)
	}
	if !strings.Contains(stdout.String(), "recovered attempt "+result.Attempt.AttemptID) {
		t.Fatalf("stdout = %q, want recovered attempt output", stdout.String())
	}
}

func TestLaunchNextRecoveryPreservesExistingLogRef(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	started := seedLauncherAttempt(t, store, runID, "stale-logged-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan-stale-logged-attempt",
		Content: []byte("partial log\n"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: started.AttemptID,
		LogRef:    logRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.LogRef == nil || *result.Attempt.LogRef != logRef {
		t.Fatalf("recovered log ref = %+v, want %+v", result.Attempt.LogRef, logRef)
	}
}

func TestLaunchNextRefusesLiveActiveAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "active-attempt", 200*time.Millisecond, time.Now().UTC())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              os.Getpid(),
		ProcessStartTime: currentProcessStartTime(t),
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	_, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err == nil || !strings.Contains(err.Error(), "already has active attempt") {
		t.Fatalf("LaunchNext error = %v, want active attempt refusal", err)
	}
}

func TestLaunchNextRecoversWhenPIDIdentityDoesNotMatch(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "reused-pid-attempt", 200*time.Millisecond, fixedLauncherTime())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              os.Getpid(),
		ProcessStartTime: "1",
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Recovered || result.Attempt.ExitState != "unknown" {
		t.Fatalf("result = %+v, want recovered unknown for mismatched process identity", result)
	}
}

func TestLauncherStoreRejectsMissingPIDIdentity(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "missing-identity-attempt", 200*time.Millisecond, fixedLauncherTime())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)
	_, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID: attempt.AttemptID,
		PID:       os.Getpid(),
		Time:      fixedLauncherTime(),
	})
	if err == nil || !strings.Contains(err.Error(), "process_start_time is required") {
		t.Fatalf("RecordAttemptProcess error = %v, want missing process_start_time", err)
	}
}

func TestLaunchNextRecoversExpiredLiveAttemptAsTimeout(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "expired-live-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker process: %v", err)
	}
	t.Cleanup(func() {
		terminateProcessGroup(cmd.Process.Pid)
		_, _ = cmd.Process.Wait()
	})
	processStartTime, err := processStartIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("processStartIdentity returned error: %v", err)
	}
	active, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              cmd.Process.Pid,
		ProcessStartTime: processStartTime,
		Time:             fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Recovered || result.Attempt.State != runstore.AttemptStateTimedOut || result.Attempt.Result != resultTimeout {
		t.Fatalf("result = %+v, want recovered timeout", result)
	}
	if active.LogRef == nil || result.Attempt.LogRef == nil || *result.Attempt.LogRef != *active.LogRef {
		t.Fatalf("recovered log ref = %+v, want preserved %+v", result.Attempt.LogRef, active.LogRef)
	}
	_, _ = cmd.Process.Wait()
	eventually(t, time.Second, func() bool {
		_, err := processStartIdentity(cmd.Process.Pid)
		return err != nil
	})
}

func TestLaunchNextRecoversUnverifiableActiveAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	seedLauncherAttempt(t, store, runID, "orphaned-attempt", 200*time.Millisecond, fixedLauncherTime())

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Recovered || result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != "process_error" || result.Attempt.ExitState != "unknown" {
		t.Fatalf("result = %+v, want recovered process_error unknown", result)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want recovered terminal attempt", loaded.Status.ActiveAttempt)
	}
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

func writeLoopCapLauncherProject(t *testing.T, root, defaultCaps, workflowCaps string) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	var config strings.Builder
	config.WriteString("version: 1\n")
	if strings.TrimSpace(defaultCaps) != "" {
		config.WriteString("defaults:\n  loop_caps:\n")
		for line := range strings.SplitSeq(strings.TrimRight(defaultCaps, "\n"), "\n") {
			config.WriteString("    " + line + "\n")
		}
	}
	config.WriteString("workflows:\n  implementation:\n    path: workflows/implementation.yaml\n")
	if strings.TrimSpace(workflowCaps) != "" {
		config.WriteString("    loop_caps:\n")
		for line := range strings.SplitSeq(strings.TrimRight(workflowCaps, "\n"), "\n") {
			config.WriteString("      " + line + "\n")
		}
	}
	config.WriteString("agents:\n  planner: agents/planner.md\n")
	writeLauncherFile(t, filepath.Join(orcDir, "config.yaml"), config.String())
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
  retries: {}
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
    on:
      done/ready: plan
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
		FailedResults:    []string{"missing_report", "process_error", "timeout"},
		TwoStep:          opts.TwoStep,
		Retries:          opts.Retries,
	})
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
	loaded, err := loadLaunchContext(root, runID)
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
	}, attempt, prompt, fixedLauncherTime())
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
		done <- errors.New("attempt did not become active")
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
	return err
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
