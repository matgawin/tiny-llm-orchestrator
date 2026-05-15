package report

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

const (
	reportIgnoredEvent = "report.ignored"
	reportStatusFailed = "failed"
)

func TestSubmitRejectsWorkerReportedSkippedOutcome(t *testing.T) {
	root := t.TempDir()
	writeSkippableReportProject(t, root)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "skip-report-run",
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeReportConfigSnapshot(t, root, store, run.ID)
	startActiveAttempt(t, store, run.ID, "attempt-001")

	result, err := Submit(context.Background(), Options{
		Root: root,
		Report: runstore.Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "skipped",
			Summary:   "Skipping this step.",
		},
		Time: time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("Submit returned nil error, want reserved skip outcome rejection")
	}
	if !strings.Contains(err.Error(), "workers cannot report reserved system outcome done/skipped") {
		t.Fatalf("error = %v, want reserved skip outcome rejection", err)
	}
	if result.Attempt.State != runstore.AttemptStateInvalidReport || result.Attempt.Status != reportStatusFailed || result.Attempt.Result != runstore.AttemptResultInvalidReport {
		t.Fatalf("attempt = %+v, want failed/invalid_report", result.Attempt)
	}

	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Status != reportStatusFailed || attempt.Result != runstore.AttemptResultInvalidReport {
		t.Fatalf("persisted attempt = %+v, want failed/invalid_report", attempt)
	}
}

func TestSubmitValidatesReportAgainstPinnedWorkflowAfterLiveMutation(t *testing.T) {
	root := t.TempDir()
	writeSkippableReportProject(t, root)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "pinned-report-run",
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeReportConfigSnapshot(t, root, store, run.ID)
	startActiveAttempt(t, store, run.ID, "attempt-1")
	writeReportWorkflow(t, root, `name: implementation
start: plan
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
  plan:
    agent: planner
    allowed_results:
      blocked: [needs_human]
    on:
      blocked/needs_human: blocked_for_human
`)

	result, err := Submit(context.Background(), Options{
		Root: root,
		Report: runstore.Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-1",
			Status:    "done",
			Result:    "ready",
			Summary:   "Pinned workflow accepted this.",
		},
		Time: time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Submit returned error after live workflow mutation: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateReported {
		t.Fatalf("attempt state = %q, want reported", result.Attempt.State)
	}
}

func TestRecordTargetRaceAsIgnoredRecordsIgnoredEvent(t *testing.T) {
	store, err := runstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "race-report-run",
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	report := runstore.Report{
		RunID:     run.ID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "stale-attempt",
		Status:    "done",
		Result:    "ready",
		Summary:   "Stale report.",
	}
	err = &runstore.ReportTargetError{
		RunID:  run.ID,
		Reason: "report does not target current active attempt",
		Err:    stableerr.New("run active attempt changed"),
	}
	ignored, result := recordTargetRaceAsIgnored(context.Background(), store, report, time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC), err)
	if !ignored {
		t.Fatal("ignored = false, want true")
	}
	if result.Err == nil || !errors.Is(result.Err, err) {
		t.Fatalf("result error = %v, want original target error", result.Err)
	}
	if result.Event.Type != reportIgnoredEvent {
		t.Fatalf("event type = %q, want report.ignored", result.Event.Type)
	}
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}
}

func TestSubmitRecordsIgnoredEventWhenTargetChangesBeforeStoreWrite(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		Beads:            "optional",
		MarkdownFallback: true,
	})
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "race-submit-run",
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	writeReportConfigSnapshot(t, root, store, run.ID)
	startActiveAttempt(t, store, run.ID, "attempt-001")

	report := runstore.Report{
		RunID:     run.ID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
		Status:    "done",
		Result:    "ready",
		Summary:   "Plan is ready.",
	}
	var once sync.Once
	result, err := submit(context.Background(), Options{
		Root:   root,
		Report: report,
		Time:   time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
	}, func() {
		once.Do(func() {
			if _, _, recordErr := store.RecordAttemptReport(run.ID, runstore.RecordReportRequest{
				Report: report,
				State:  runstore.AttemptStateReported,
				Time:   time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
			}); recordErr != nil {
				t.Fatalf("competing RecordAttemptReport returned error: %v", recordErr)
			}
		})
	})
	if err == nil {
		t.Fatal("Submit returned nil error, want stale target error")
	}
	if !result.Ignored {
		t.Fatalf("result ignored = false, want true")
	}
	var targetErr *runstore.ReportTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("error = %v, want ReportTargetError", err)
	}
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}
}

func writeReportConfigSnapshot(t *testing.T, root string, store *runstore.Store, runID string) {
	t.Helper()
	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}
	snapshot, err := configsnapshot.BuildInitial(project, "implementation", time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}
	if err := store.WriteInitialConfigSnapshot(runID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
}

func writeSkippableReportProject(t *testing.T, root string) {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		Beads:            "optional",
		MarkdownFallback: true,
	})
	workflow := `name: implementation
start: plan
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
  plan:
    agent: planner
    skippable: true
    allowed_results:
      done: [ready, skipped]
    on:
      done/ready: ready_for_human
      done/skipped: ready_for_human
`
	if err := os.WriteFile(filepath.Join(root, ".orc", "workflows", "implementation.yaml"), []byte(workflow), 0o600); err != nil {
		t.Fatalf("write skippable workflow: %v", err)
	}
}

func writeReportWorkflow(t *testing.T, root, workflow string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".orc", "workflows", "implementation.yaml"), []byte(workflow), 0o600); err != nil {
		t.Fatalf("write report workflow: %v", err)
	}
}

func startActiveAttempt(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan-" + attemptID,
		Content: []byte("prompt\n"),
		Time:    time.Date(2026, 5, 4, 12, 0, 1, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      time.Date(2026, 5, 4, 12, 0, 2, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan-" + attemptID,
		Content: []byte("log\n"),
		Time:    time.Date(2026, 5, 4, 12, 0, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      time.Date(2026, 5, 4, 12, 0, 4, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             time.Date(2026, 5, 4, 12, 0, 5, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
}
