package launcher

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestFinishAttemptWithCleanupContextReturnsNilOnSuccessfulFinish(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	loaded, attempt := prepareRunProcessAttempt(t, root, runID, "cleanup-finish-success")
	linkLauncherPromptAndLog(t, loaded.Store, runID, attempt.AttemptID)
	recordProcessForLauncherTest(t, loaded.Store, runID, attempt.AttemptID)

	finished, err := finishAttemptWithCleanupContext(context.Background(), loaded.Store, runID, runstore.FinishAttemptRequest{
		AttemptID: attempt.AttemptID,
		State:     runstore.AttemptStateMissingReport,
		Status:    workflow.ReportStatusFailed,
		Result:    resultMissingReport,
		ExitState: exitStateExited,
		Time:      fixedLauncherTime().Add(time.Second),
	})
	if err != nil {
		t.Fatalf("finishAttemptWithCleanupContext returned error: %v", err)
	}
	if finished.AttemptID != attempt.AttemptID || finished.State != runstore.AttemptStateMissingReport {
		t.Fatalf("finished attempt = %+v, want terminal missing_report attempt %q", finished, attempt.AttemptID)
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
	got, ok, err := runner.reportTerminalAttemptAfterWait(context.Background())
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
	}, attempt, promptrender.Result{Content: prompt}, fixedLauncherTime(), nil)
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
	loaded, err := loadLaunchContext(context.Background(), root, runID)
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
	}, attempt, promptrender.Result{Content: []byte("prompt\n")}, fixedLauncherTime(), nil)
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
		}, attempt, promptrender.Result{Content: []byte("prompt\n")}, fixedLauncherTime(), nil)
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
		}, attempt, promptrender.Result{Content: []byte("prompt\n")}, fixedLauncherTime(), nil)
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
