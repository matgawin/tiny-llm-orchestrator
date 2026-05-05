package report

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

const reportIgnoredEvent = "report.ignored"

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
		Err:    errors.New("run active attempt changed"),
	}
	ignored, result := recordTargetRaceAsIgnored(store, report, time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC), err)
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
