package runinspect

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runconfigrefresh"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/vcs"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const forbiddenLegacyPendingWorkerOutcome = "pending_worker_outcome"

func TestStatusShowsNewRunSelectedStartStep(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte("# Task\n"),
		Time:    fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	var stdout bytes.Buffer
	if err := Status(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	output := stdout.String()
	assertContainsAll(t, "status", output, []string{
		"run: inspect-run\n",
		"state: running\n",
		"workflow: implementation\n",
		"selected_step: plan\n",
		"active_attempt: none\n",
		"recent_reports:\n  none\n",
		"artifacts:\n  - task_context: task/context.md\n",
	})
}

func TestNextShowsSelectedStepWithoutLaunching(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	before := snapshotRunDir(t, root, runID)

	var stdout bytes.Buffer
	if err := Next(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Next returned error: %v", err)
	}

	output := stdout.String()
	assertContainsAll(t, "next", output, []string{
		"decision: select_step\n",
		"selected_step: plan\n",
		"agent: planner\n",
		"launch: not launched\n",
	})
	after := snapshotRunDir(t, root, runID)
	if !maps.Equal(before, after) {
		t.Fatalf("Next mutated run directory:\nbefore: %+v\nafter: %+v", before, after)
	}
}

func TestNextUsesPinnedWorkflowAfterLiveMutation(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	writeInspectWorkflow(t, root, `name: implementation
start: review
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  runtime: codex
  retries: {}
steps:
  review:
    agent: reviewer
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`)

	var stdout bytes.Buffer
	if err := Next(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Next returned error after live workflow mutation: %v", err)
	}
	output := stdout.String()
	assertContainsAll(t, "next", output, []string{
		"decision: select_step\n",
		"selected_step: plan\n",
		"agent: planner\n",
	})
	if strings.Contains(output, "selected_step: review") {
		t.Fatalf("Next used live workflow mutation:\n%s", output)
	}
}

func TestConfigShowsSnapshotMetadataAndRefreshHistoryWithoutLiveConfig(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	workflowContent := string(readFile(t, workflowPath))
	workflowContent = strings.Replace(workflowContent, "timeout: 30m", "timeout: 45m", 1)
	writeInspectFile(t, workflowPath, workflowContent)

	if _, err := runconfigrefresh.Refresh(context.Background(), runconfigrefresh.Options{
		Root:   root,
		RunID:  runID,
		Source: "test",
		Time:   fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	writeInspectFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	var stdout bytes.Buffer
	if err := Config(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Config returned error after live config mutation: %v", err)
	}

	output := stdout.String()
	assertContainsAll(t, "config", output, []string{
		"run: inspect-run\n",
		"workflow: implementation\n",
		"current_config_snapshot:\n",
		"  version: 2\n",
		"  version_dir: 000002\n",
		"  resolved: config/000002/resolved.json\n",
		"  manifest: config/000002/manifest.json\n",
		"  created_at: 2026-05-03T12:01:00Z\n",
		"  manifest_hash: sha256:",
		"  source_files: ",
		"  source_hash: sha256:",
		"refresh_history:\n",
		"  - sequence: ",
		"    version: 000001 -> 000002\n",
		"    source: \"test\"\n",
	})
}

func TestConfigShowsEmptyRefreshHistory(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)

	var stdout bytes.Buffer
	if err := Config(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Config returned error: %v", err)
	}

	assertContainsAll(t, "config", stdout.String(), []string{
		"current_config_snapshot:\n",
		"  version: 1\n",
		"  version_dir: 000001\n",
		"refresh_history:\n  none\n",
	})
}

func TestStatusAndSummaryContextShowSkippedStepOnce(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte("# Task\n"),
		Time:    fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	if _, _, err := store.RecordStepSkip(runID, runstore.RecordStepSkipRequest{
		StepID: "plan",
		Reason: "not worth another review",
		Time:   fixedTime().Add(2 * time.Minute),
	}, func(runstore.Status) (runstore.StepSkipTransition, error) {
		return runstore.StepSkipTransition{
			State: workflow.RunStatusReadyForHuman,
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         workflow.RunStatusReadyForHuman,
				PreviousState: "plan",
				TriggerStatus: "done",
				TriggerResult: "skipped",
			},
		}, nil
	}); err != nil {
		t.Fatalf("RecordStepSkip returned error: %v", err)
	}

	var statusOut bytes.Buffer
	if err := Status(context.Background(), Options{Root: root, RunID: runID, Stdout: &statusOut}); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	assertContainsAll(t, "status", statusOut.String(), []string{
		"skipped_steps:\n",
		"  - step_id: plan\n",
		"    status: done\n",
		"    result: skipped\n",
		"    reason: \"not worth another review\"\n",
	})

	var summaryOut bytes.Buffer
	if err := SummaryContext(context.Background(), Options{Root: root, RunID: runID, Stdout: &summaryOut}); err != nil {
		t.Fatalf("SummaryContext returned error: %v", err)
	}
	wording := "step plan skipped by human decision: not worth another review"
	if got := strings.Count(summaryOut.String(), wording); got != 1 {
		t.Fatalf("summary skip wording count = %d, want 1\n%s", got, summaryOut.String())
	}
}

func TestStatusAndNextShowActiveAttempt(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-active",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"selected_step: none\n",
		"active_attempt: attempt-active\n",
	})
	assertContainsAll(t, "next", next, []string{
		"decision: wait_active_attempt\n",
		"active_attempt: attempt-active\n",
		"selected_step: plan\n",
		"agent: planner\n",
		"launch: already active\n",
	})
}

func TestNextRoutesExhaustedLauncherOutcome(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordMissingReportAttempt(t, store, runID, "attempt-missing-report")

	status, next := inspectStatusAndNext(t, root, runID)
	statusOnly := []string{
		"selected_step: none\n",
		"active_attempt: none\n",
	}
	terminalOutcome := []string{
		"terminal_reason: blocked_for_human\n",
		"last_outcome: failed/missing_report\n",
		"last_attempt: attempt-missing-report\n",
		"retry_exhausted: failed/missing_report\n",
		"retry_count: 0/0\n",
		"transition: failed/missing_report -> blocked_for_human\n",
	}
	assertContainsAll(t, "status", status, statusOnly)
	assertBothOutputsContain(t, status, next, terminalOutcome)
	assertContainsAll(t, "next", next, []string{
		"decision: terminal\n",
		"launch: no worker should launch\n",
	})
}

func TestStatusAndNextShowInvalidReportOutcome(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordLaunchedAttempt(t, store, runID, "attempt-invalid-report")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateInvalidReport,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-invalid-report",
			Status:    "failed",
			Result:    runstore.AttemptResultInvalidReport,
			Summary:   "report schema invalid: missing summary",
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	status, next := inspectStatusAndNext(t, root, runID)
	assertBothOutputsContain(t, status, next, []string{
		"terminal_reason: blocked_for_human\n",
		"last_outcome: failed/invalid_report\n",
		"last_attempt: attempt-invalid-report\n",
		"invalid_report_reason: report schema invalid: missing summary\n",
		"retry_exhausted: failed/invalid_report\n",
		"retry_count: 0/0\n",
		"transition: failed/invalid_report -> blocked_for_human\n",
	})
}

func TestStatusAndNextShowExhaustedRetryCountFromWorkflowPolicy(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		FailedResults:    []string{"missing_report", "process_error", "timeout", "invalid_report"},
		Retries:          map[string]int{"failed/missing_report": 2},
	})
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordRetriedMissingReportAttempt(t, store, runID, 2)

	status, next := inspectStatusAndNext(t, root, runID)
	assertBothOutputsContain(t, status, next, []string{
		"retry_exhausted: failed/missing_report\n",
		"retry_count: 2/2\n",
	})
}

func TestNextRoutesValidReportedOutcome(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordLaunchedAttempt(t, store, runID, "attempt-reported")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-reported",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"state: ready_for_human\n",
		"selected_step: none\n",
		"terminal_reason: ready_for_human\n",
		"review_state: ready_for_human; no more workers should launch\n",
	})
	assertContainsAll(t, "next", next, []string{
		"state: ready_for_human\n",
		"decision: terminal\n",
		"terminal_reason: ready_for_human\n",
		"launch: no worker should launch\n",
		"review_state: ready_for_human; no more workers should launch\n",
	})
	if strings.Contains(next, forbiddenLegacyPendingWorkerOutcome) {
		t.Fatalf("next output contains legacy pending worker outcome for valid report:\n%s", next)
	}
}

func TestStatusRoutesValidReportedOutcomeToBlockedForHuman(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordLaunchedAttempt(t, store, runID, "attempt-reported")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-reported",
			Status:    "blocked",
			Result:    "blocked",
			Summary:   "Blocked for human input.",
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"state: blocked_for_human\n",
		"terminal_reason: blocked_for_human\n",
		"human_attention: blocked_for_human; report details not available\n",
	})
	assertContainsAll(t, "next", next, []string{
		"state: blocked_for_human\n",
		"decision: terminal\n",
		"terminal_reason: blocked_for_human\n",
		"human_attention: blocked_for_human; report details not available\n",
	})
}

func TestNextRoutesValidReportedOutcomeToNextStep(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		BlockedResults:   []string{"blocked"},
		TwoStep:          true,
	})
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordLaunchedAttempt(t, store, runID, "attempt-reported")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-reported",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	_, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "next", next, []string{
		"decision: select_step\n",
		"selected_step: code\n",
		"agent: coder\n",
		"launch: not launched\n",
	})
	if strings.Contains(next, forbiddenLegacyPendingWorkerOutcome) {
		t.Fatalf("next output contains legacy pending worker outcome for valid report:\n%s", next)
	}
}

func TestNextShowsReportedRetryStepOutcome(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		BlockedResults:   []string{"blocked"},
		Retries:          map[string]int{"done/ready": 1},
	})
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordLaunchedAttempt(t, store, runID, "attempt-retry")
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-retry",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	_, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "next", next, []string{
		"decision: retry_step\n",
		"selected_step: plan\n",
		"retrying_after: done/ready\n",
		"retry_count: 1/1\n",
		"retry_source_attempt: attempt-retry\n",
		"launch: not launched\n",
	})
}

func recordMissingReportAttempt(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	recordLaunchedAttempt(t, store, runID, attemptID)
	if _, _, err := store.FinishAttempt(runID, runstore.FinishAttemptRequest{
		AttemptID: attemptID,
		State:     runstore.AttemptStateMissingReport,
		Status:    "failed",
		Result:    runstore.AttemptResultMissingReport,
		ExitState: "exited",
		Time:      fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
}

func recordRetriedMissingReportAttempt(t *testing.T, store *runstore.Store, runID string, retryCount int) {
	t.Helper()
	const firstAttemptID = "attempt-missing-report"
	const retryAttemptID = "attempt-retry"
	recordMissingReportAttempt(t, store, runID, firstAttemptID)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:           "plan",
		AgentID:          "planner",
		AttemptID:        retryAttemptID,
		Timeout:          30 * time.Minute,
		ReportExitGrace:  30 * time.Second,
		ConsumeAttemptID: firstAttemptID,
		RetryLineage:     &runstore.RetryLineage{StepID: "plan", Counts: map[string]int{"failed/missing_report": retryCount}},
		SupersedeReason:  "retry",
		Time:             fixedTime().Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	recordAttemptPromptLogAndProcess(t, store, runID, "plan", retryAttemptID, 3200*time.Millisecond)
	if _, _, err := store.FinishAttempt(runID, runstore.FinishAttemptRequest{
		AttemptID: retryAttemptID,
		State:     runstore.AttemptStateMissingReport,
		Status:    "failed",
		Result:    runstore.AttemptResultMissingReport,
		ExitState: "exited",
		Time:      fixedTime().Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
}

func recordAttemptPromptLogAndProcess(t *testing.T, store *runstore.Store, runID, stepID, attemptID string, at time.Duration) {
	t.Helper()
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    stepID + "-" + attemptID,
		Content: []byte("prompt\n"),
		Time:    fixedTime().Add(at),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedTime().Add(at + 100*time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    stepID + "-" + attemptID,
		Content: []byte("log\n"),
		Time:    fixedTime().Add(at + 200*time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedTime().Add(at + 300*time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedTime().Add(at + 400*time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
}

func TestSummaryContextRendersApprovedV1Structure(t *testing.T) {
	root := t.TempDir()
	runID := writeApprovedSummaryContextFixture(t, root)

	var stdout bytes.Buffer
	if err := SummaryContext(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("SummaryContext returned error: %v", err)
	}

	want := readRunInspectTestdata(t, "summary_context_v1.golden")
	if got := stdout.String(); got != want {
		t.Fatalf("summary context mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSummaryContextDoesNotMutateRunDirectory(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, "# Task\n")
	before := snapshotRunDir(t, fixture.root, fixture.runID)

	_ = fixture.renderSummaryContext(t)

	after := snapshotRunDir(t, fixture.root, fixture.runID)
	if !maps.Equal(before, after) {
		t.Fatalf("SummaryContext mutated run directory:\nbefore: %+v\nafter: %+v", before, after)
	}
}

func writeApprovedSummaryContextFixture(t *testing.T, root string) string {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		TwoStep:          true,
	})
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	writeSummaryTaskContext(t, store, runID, "# Task\n\nImplement summary context.")
	writeSummaryContextVCSSnapshot(t, store, runID, "vcs-pre-run", vcs.Snapshot{
		SchemaVersion: 1,
		Phase:         vcs.PhasePreRun,
		Kind:          vcs.KindJJ,
		Dirty:         true,
		Summary:       "Pre-existing dirty file: preexisting.md",
		ChangedPaths:  []string{"preexisting.md"},
		Commands:      [][]string{{"jj", "root"}, {"jj", "status"}},
	}, fixedTime().Add(500*time.Millisecond))
	// Record code before plan so the golden locks workflow declaration order
	// rather than report event order.
	recordLaunchedStepAttempt(t, store, runID, "code", "coder", "attempt-code")
	recordSummaryContextReport(t, store, runID, runstore.Report{
		RunID:        runID,
		StepID:       "code",
		AgentID:      "coder",
		AttemptID:    "attempt-code",
		Status:       "done",
		Result:       "ready",
		Summary:      "Code changed.",
		ChangedPaths: []string{"internal/runinspect/runinspect.go"},
		Commands:     []string{"go test ./internal/runinspect"},
		Tests:        []string{"go test ./internal/runinspect"},
		Risks:        []string{"format needs human approval"},
		Followups:    []runstore.Followup{{Title: "Update release note", Details: "Mention summary-context."}},
	}, fixedTime().Add(2*time.Minute))
	recordLaunchedStepAttempt(t, store, runID, "plan", "planner", "attempt-plan")
	recordSummaryContextReport(t, store, runID, runstore.Report{
		RunID:        runID,
		StepID:       "plan",
		AgentID:      "planner",
		AttemptID:    "attempt-plan",
		Status:       "done",
		Result:       "ready",
		Summary:      "Plan approved.",
		ChangedPaths: []string{"docs/features/summary-context.md"},
		Commands:     []string{"go test ./internal/cli"},
		Tests:        []string{"go test ./internal/cli"},
	}, fixedTime().Add(3*time.Minute), summaryContextReportContent{
		name:    "plan",
		content: []byte("## Plan Report\n"),
	})
	if _, err := store.RecordFollowup(runID, runstore.RecordFollowupRequest{
		Followup: runstore.Followup{
			Title:   "Create manual follow-up",
			Details: "Human decides whether this becomes a bead.",
		},
		Source: runstore.FollowupSourceOrchestrator,
		Time:   fixedTime().Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordFollowup returned error: %v", err)
	}
	writeSummaryContextVCSSnapshot(t, store, runID, "vcs-post-run", vcs.Snapshot{
		SchemaVersion: 1,
		Phase:         vcs.PhasePostRun,
		Kind:          vcs.KindJJ,
		Dirty:         true,
		Summary:       "Modified files: docs/features/summary-context.md internal/runinspect/runinspect.go",
		ChangedPaths:  []string{"docs/features/summary-context.md", "internal/runinspect/runinspect.go", "preexisting.md"},
		Commands:      [][]string{{"jj", "root"}, {"jj", "status"}},
	}, fixedTime().Add(5*time.Minute))
	if _, _, err := store.UpdateStatus(runID, runstore.StatusUpdate{
		State: workflow.RunStatusReadyForHuman,
		Time:  fixedTime().Add(6 * time.Minute),
	}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	return runID
}

type summaryContextReportContent struct {
	name    string
	content []byte
}

func recordSummaryContextReport(t *testing.T, store *runstore.Store, runID string, report runstore.Report, at time.Time, content ...summaryContextReportContent) {
	t.Helper()
	request := runstore.RecordReportRequest{
		State:  runstore.AttemptStateReported,
		Report: report,
		Time:   at,
	}
	if len(content) > 0 {
		request.ReportContent = content[0].content
		request.ReportContentSet = true
		request.ReportName = content[0].name
	}
	if _, _, err := store.RecordAttemptReport(runID, request); err != nil {
		t.Fatalf("RecordAttemptReport %s returned error: %v", report.AttemptID, err)
	}
}

func writeSummaryContextVCSSnapshot(t *testing.T, store *runstore.Store, runID, name string, snapshot vcs.Snapshot, at time.Time) {
	t.Helper()
	if _, err := vcs.WriteSnapshot(context.Background(), store, runID, name, snapshot, at); err != nil {
		t.Fatalf("WriteSnapshot %s returned error: %v", name, err)
	}
}

type summaryContextFixture struct {
	root  string
	runID string
	store *runstore.Store
}

func newSummaryContextFixture(t *testing.T, state, taskContext string) summaryContextFixture {
	t.Helper()
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, state, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	writeSummaryTaskContext(t, store, runID, taskContext)
	return summaryContextFixture{root: root, runID: runID, store: store}
}

func (fixture summaryContextFixture) renderSummaryContext(t *testing.T) string {
	t.Helper()
	var stdout bytes.Buffer
	if err := SummaryContext(context.Background(), Options{Root: fixture.root, RunID: fixture.runID, Stdout: &stdout}); err != nil {
		t.Fatalf("SummaryContext returned error: %v", err)
	}
	return stdout.String()
}

func TestSummaryContextUsesAdaptiveMarkdownFencesAndTruncatesExcerpts(t *testing.T) {
	taskContext := "# Task\n\n```go\nfmt.Println(\"inside\")\n```\n\n" + strings.Repeat("x", summaryExcerptLimit+1)
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, taskContext)

	output := fixture.renderSummaryContext(t)

	assertContainsAll(t, "summary context", output, []string{
		"````md\n# Task\n\n```go\nfmt.Println(\"inside\")\n```\n\n",
		"[excerpt truncated]\n````\n",
	})
}

func TestSummaryContextUsesLatestVCSSnapshotForPhase(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, "# Task\n")
	writeSummaryContextVCSSnapshot(t, fixture.store, fixture.runID, "vcs-post-run", vcs.Snapshot{
		SchemaVersion: 1,
		Phase:         vcs.PhasePostRun,
		Kind:          vcs.KindJJ,
		Dirty:         true,
		Summary:       "stale post-run summary",
		ChangedPaths:  []string{"stale.md"},
	}, fixedTime().Add(time.Minute))
	writeSummaryContextVCSSnapshot(t, fixture.store, fixture.runID, "vcs-post-run", vcs.Snapshot{
		SchemaVersion: 1,
		Phase:         vcs.PhasePostRun,
		Kind:          vcs.KindJJ,
		Dirty:         true,
		Summary:       "latest post-run summary",
		ChangedPaths:  []string{"latest.md"},
	}, fixedTime().Add(2*time.Minute))

	output := fixture.renderSummaryContext(t)
	assertContainsAll(t, "summary context", output, []string{
		"- \"latest.md\"\n",
		"latest post-run summary",
	})
	if strings.Contains(output, "stale.md") || strings.Contains(output, "stale post-run summary") {
		t.Fatalf("summary context used stale VCS snapshot:\n%s", output)
	}
}

func TestSummaryContextIgnoresUnrelatedSnapshotWithVCSSubstring(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, "# Task\n")
	if _, err := fixture.store.WriteArtifact(fixture.runID, runstore.Artifact{
		Kind:    runstore.KindSnapshot,
		Name:    "not-vcs-data",
		Content: []byte("{not-json"),
		Time:    fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("WriteArtifact unrelated snapshot returned error: %v", err)
	}

	assertContainsAll(t, "summary context", fixture.renderSummaryContext(t), []string{
		"### Pre-Run\n- not recorded\n",
		"### Post-Run\n- not recorded\n",
	})
}

func TestSummaryContextQuotesReportScalars(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, "# Task\n")
	scalars := []struct {
		name  string
		value string
		want  string
	}{
		{name: "outcome_result", value: "ready\n## fake result", want: "- outcome_result: %s\n"},
		{name: "summary", value: "Line one\n## fake heading", want: "- summary: %s\n"},
		{name: "changed_path", value: "README.md\n- fake path", want: "- %s\n"},
		{name: "command", value: "go test ./...\n## fake command", want: "- %s\n"},
		{name: "test", value: "task tests\n- fake test", want: "- %s\n"},
		{name: "risk", value: "risk\n### fake risk", want: "- %s\n"},
		{name: "followup_title", value: "title\n- fake follow-up", want: "- title: %s\n"},
		{name: "followup_details", value: "details\n## fake details", want: "  details: %s\n"},
	}
	scalar := func(name string) string {
		t.Helper()
		for _, item := range scalars {
			if item.name == name {
				return item.value
			}
		}
		t.Fatalf("test scalar %q is not declared", name)
		return ""
	}
	recordLaunchedAttempt(t, fixture.store, fixture.runID, "attempt-report")
	if _, _, err := fixture.store.RecordAttemptReport(fixture.runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:        fixture.runID,
			StepID:       "plan",
			AgentID:      "planner",
			AttemptID:    "attempt-report",
			Status:       "done",
			Result:       scalar("outcome_result"),
			Summary:      scalar("summary"),
			ChangedPaths: []string{scalar("changed_path")},
			Commands:     []string{scalar("command")},
			Tests:        []string{scalar("test")},
			Risks:        []string{scalar("risk")},
			Followups: []runstore.Followup{{
				Title:   scalar("followup_title"),
				Details: scalar("followup_details"),
			}},
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}
	if _, _, err := fixture.store.UpdateStatus(fixture.runID, runstore.StatusUpdate{
		State: workflow.RunStatusReadyForHuman,
		Time:  fixedTime().Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	wants := []string{
		`- step: "plan"` + "\n",
		`- attempt: "attempt-report"` + "\n",
		`- agent: "planner"` + "\n",
		`- outcome_status: "done"` + "\n",
	}
	for _, item := range scalars {
		wants = append(wants, fmt.Sprintf(item.want, quoteScalar(item.value)))
	}
	assertContainsAll(t, "summary context", fixture.renderSummaryContext(t), wants)
}

func TestSummaryContextLabelsBlockedRunAndMissingPostRunVCS(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusBlockedForHuman, "# Blocked task\n")

	assertContainsAll(t, "summary context", fixture.renderSummaryContext(t), []string{
		`- terminal_state: "blocked_for_human"` + "\n",
		`- human_attention: "blocked_for_human"` + "\n",
		"- blocked_for_human requires human attention\n",
		"### Post-Run\n- not recorded\n",
		"- Resolve the blocked_for_human terminal state before treating the run as ready.\n",
		"- Post-run VCS snapshot is not recorded.\n",
	})
}

func TestSummaryContextBlockedRunIncludesBlockedReportReason(t *testing.T) {
	fixture := newSummaryContextFixture(t, workflow.RunStatusRunning, "# Blocked task\n")
	recordLaunchedAttempt(t, fixture.store, fixture.runID, "attempt-blocked")
	if _, _, err := fixture.store.RecordAttemptReport(fixture.runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     fixture.runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-blocked",
			Status:    "blocked",
			Result:    "blocked",
			Summary:   "Waiting for network approval.",
			Risks:     []string{"Cannot verify without approval."},
		},
		Time: fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	assertContainsAll(t, "summary context", fixture.renderSummaryContext(t), []string{
		`- effective_state: "blocked_for_human"` + "\n",
		`- human_attention: "blocked_for_human"` + "\n",
		"- summary: \"Waiting for network approval.\"\n",
		"- \"Cannot verify without approval.\"\n",
	})
}

func recordLaunchedAttempt(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	recordLaunchedStepAttempt(t, store, runID, "plan", "planner", attemptID)
}

func recordLaunchedStepAttempt(t *testing.T, store *runstore.Store, runID, stepID, agentID, attemptID string) {
	t.Helper()
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          stepID,
		AgentID:         agentID,
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	recordAttemptPromptLogAndProcess(t, store, runID, stepID, attemptID, 1200*time.Millisecond)
}

func writeSummaryTaskContext(t *testing.T, store *runstore.Store, runID, content string) {
	t.Helper()
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte(content),
		Time:    fixedTime().Add(250 * time.Millisecond),
	}); err != nil {
		t.Fatalf("WriteArtifact task context returned error: %v", err)
	}
}

func TestReadyForHumanStatusAndNextShowReviewPaths(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusReadyForHuman, nil)

	status, next := inspectStatusAndNext(t, root, runID)
	paths := terminalPaths(root, runID)
	assertTerminalOutputs(t, status, next, terminalOutputWants{
		status: []string{
			"selected_step: none\n",
			"terminal_reason: ready_for_human\n",
			"review_state: ready_for_human; no more workers should launch\n",
			"summary_context: " + paths.run + "\n",
			"final_summaries: " + paths.summaries + "\n",
		},
		next: []string{
			"decision: terminal\n",
			"terminal_reason: ready_for_human\n",
			"launch: no worker should launch\n",
			"review_state: ready_for_human; no more workers should launch\n",
			"final_summaries: " + paths.summaries + "\n",
		},
	})
}

func TestBlockedForHumanStatusAndNextShowUnavailableReportDetails(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusBlockedForHuman, nil)

	status, next := inspectStatusAndNext(t, root, runID)
	paths := terminalPaths(root, runID)
	assertTerminalOutputs(t, status, next, terminalOutputWants{
		status: []string{
			"selected_step: none\n",
			"terminal_reason: blocked_for_human\n",
			"human_attention: blocked_for_human; report details not available\n",
			"summary_context: " + paths.run + "\n",
			"final_summaries: " + paths.summaries + "\n",
		},
		next: []string{
			"decision: terminal\n",
			"terminal_reason: blocked_for_human\n",
			"launch: no worker should launch\n",
			"human_attention: blocked_for_human; report details not available\n",
			"final_summaries: " + paths.summaries + "\n",
		},
	})
}

type terminalOutputWants struct {
	status []string
	next   []string
}

type terminalPathWants struct {
	run       string
	summaries string
}

func terminalPaths(root, runID string) terminalPathWants {
	runPath := filepath.ToSlash(filepath.Join(root, ".orc", "runs", runID))
	return terminalPathWants{
		run:       runPath,
		summaries: filepath.ToSlash(filepath.Join(runPath, "summaries")),
	}
}

func assertTerminalOutputs(t *testing.T, status, next string, wants terminalOutputWants) {
	t.Helper()
	assertContainsAll(t, "status", status, wants.status)
	assertContainsAll(t, "next", next, wants.next)
}

func TestBlockedStatusAndNextShowReportArtifact(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusBlockedForHuman, &runstore.Artifact{
		Kind:    runstore.KindReport,
		Name:    "plan",
		Content: []byte("blocked report\n"),
		Time:    fixedTime().Add(time.Minute),
	})

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"terminal_reason: blocked_for_human\n",
		"recent_reports:\n  - reports/000002-plan.md\n",
		"human_attention: blocked_for_human; see recent_reports\n",
	})
	assertContainsAll(t, "next", next, []string{
		"decision: terminal\n",
		"terminal_reason: blocked_for_human\n",
		"recent_reports:\n  - reports/000002-plan.md\n",
		"human_attention: blocked_for_human; see recent_reports\n",
	})
}

func TestInspectUnknownRunFailsClearly(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)

	for _, tc := range []struct {
		name    string
		inspect func(context.Context, Options) error
	}{
		{name: "status", inspect: Status},
		{name: "next", inspect: Next},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.inspect(context.Background(), Options{Root: root, RunID: "missing", Stdout: &bytes.Buffer{}})
			if err == nil || !strings.Contains(err.Error(), `run "missing" not found`) {
				t.Fatalf("%s error = %v, want not found", tc.name, err)
			}
		})
	}
}

func inspectStatusAndNext(t *testing.T, root, runID string) (string, string) {
	t.Helper()
	var statusOut bytes.Buffer
	if err := Status(context.Background(), Options{Root: root, RunID: runID, Stdout: &statusOut}); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	var nextOut bytes.Buffer
	if err := Next(context.Background(), Options{Root: root, RunID: runID, Stdout: &nextOut}); err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	return statusOut.String(), nextOut.String()
}

func assertContainsAll(t *testing.T, label, output string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("%s output missing %q:\n%s", label, want, output)
		}
	}
}

func assertBothOutputsContain(t *testing.T, status, next string, wants []string) {
	t.Helper()
	assertContainsAll(t, "status", status, wants)
	assertContainsAll(t, "next", next, wants)
}

func readRunInspectTestdata(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(content)
}

func createRun(t *testing.T, root, state string, artifact *runstore.Artifact) string {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "inspect-run",
		Workflow: "implementation",
		Time:     fixedTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeInspectConfigSnapshot(t, root, store, run.ID)
	if artifact != nil {
		if _, err := store.WriteArtifact(run.ID, *artifact); err != nil {
			t.Fatalf("WriteArtifact returned error: %v", err)
		}
	}
	if state != workflow.RunStatusRunning {
		at := fixedTime().Add(time.Minute)
		if artifact != nil {
			at = fixedTime().Add(2 * time.Minute)
		}
		if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: state, Time: at}); err != nil {
			t.Fatalf("UpdateStatus returned error: %v", err)
		}
	}
	return run.ID
}

func writeInspectConfigSnapshot(t *testing.T, root string, store *runstore.Store, runID string) {
	t.Helper()
	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}
	snapshot, err := configsnapshot.BuildInitial(project, "implementation", fixedTime())
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}
	if err := store.WriteInitialConfigSnapshot(runID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
}

func writeProject(t *testing.T, root string) {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		BlockedResults:   []string{"blocked"},
		FailedResults:    []string{"missing_report", "process_error", "timeout", "invalid_report"},
	})
}

func writeInspectWorkflow(t *testing.T, root, content string) {
	t.Helper()
	writeInspectFile(t, filepath.Join(root, ".orc", "workflows", "implementation.yaml"), content)
}

func writeInspectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write inspect file %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return content
}

func fixedTime() time.Time {
	return time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
}

func TestReportPathsFiltersReports(t *testing.T) {
	refs := []runstore.ArtifactRef{
		{Kind: runstore.KindTaskContext, Path: "task/context.md"},
		{Kind: runstore.KindReport, Path: "reports/000002-plan.md"},
	}
	paths := reportPaths(refs)
	if want := []string{"reports/000002-plan.md"}; !slices.Equal(paths, want) {
		t.Fatalf("report paths = %v, want %v", paths, want)
	}
}

func snapshotRunDir(t *testing.T, root, runID string) map[string]string {
	t.Helper()
	runDir := filepath.Join(root, ".orc", "runs", runID)
	snapshot := map[string]string{}
	if err := filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return fmt.Errorf("snapshot run dir: %w", err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("snapshot run dir: %w", err)
		}
		snapshot[filepath.ToSlash(rel)] = string(content)
		return nil
	}); err != nil {
		t.Fatalf("snapshot run dir: %v", err)
	}
	return snapshot
}
