package launcher

import (
	"context"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

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

func TestLaunchNextRetriesResolvedHumanBlockAfterReload(t *testing.T) {
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

	store := openLauncherStore(t, root)
	if _, _, err := store.ResolveHumanBlock(runID, "fixed worker command input", fixedLauncherTime().Add(2*time.Second)); err != nil {
		t.Fatalf("ResolveHumanBlock returned error: %v", err)
	}

	retry, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("retry LaunchNext returned error: %v", err)
	}
	if retry.Attempt.AttemptID == first.Attempt.AttemptID {
		t.Fatalf("retry attempt id = %q, want new attempt", retry.Attempt.AttemptID)
	}
	if retry.Attempt.StepID != first.Attempt.StepID {
		t.Fatalf("retry step = %q, want %q", retry.Attempt.StepID, first.Attempt.StepID)
	}
	loaded, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.Continued != nil {
		t.Fatalf("continued marker = %+v, want cleared after launch", loaded.Status.Continued)
	}
	if got := len(loaded.Status.Attempts); got != 2 {
		t.Fatalf("attempt count = %d, want original plus retry", got)
	}
	if got := loaded.Status.WorkflowLoop.Counts[first.Attempt.StepID]; got != 2 {
		t.Fatalf("%s workflow-loop count = %d, want initial plus continued retry", first.Attempt.StepID, got)
	}
	entries := loaded.Status.WorkflowLoop.Entries
	if got := entries[len(entries)-1]; got.State != first.Attempt.StepID ||
		got.PreviousState != first.Attempt.StepID ||
		got.TriggerStatus != first.Attempt.Status ||
		got.TriggerResult != first.Attempt.Result ||
		got.Count != 2 ||
		!got.Repeated {
		t.Fatalf("last workflow-loop entry = %+v, want continued retry entry from blocked attempt", got)
	}
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
	if result.Attempt.State != runstore.AttemptStateProcessError || result.Attempt.Result != resultProcessError {
		t.Fatalf("attempt = %+v, want process_error", result.Attempt)
	}
	if result.Attempt.ExitCode == nil || *result.Attempt.ExitCode != 7 {
		t.Fatalf("exit code = %+v, want 7", result.Attempt.ExitCode)
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
