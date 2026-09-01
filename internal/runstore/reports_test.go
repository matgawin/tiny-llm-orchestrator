package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordAttemptReportTerminalizesActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	recorded, event, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:        run.ID,
			StepID:       testWorkflowStatePlan,
			AgentID:      testAgentPlanner,
			AttemptID:    testAttemptID,
			Status:       attemptStatusDone,
			Result:       reportResultReady,
			Summary:      testReportSummaryPlanReady,
			ChangedPaths: []string{"README.md"},
			Commands:     []string{"go test ./internal/runstore"},
			Tests:        []string{"go test ./internal/runstore"},
			Risks:        []string{"none"},
			Followups:    []Followup{{Title: "Document report summaries", Details: "Later summary work."}},
		},
		Time: time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	if recorded.State != AttemptStateReported || recorded.Status != attemptStatusDone || recorded.Result != reportResultReady {
		t.Fatalf("reported attempt = %+v, want reported done/ready", recorded)
	}

	if recorded.Report == nil || recorded.Report.Summary != testReportSummaryPlanReady || len(recorded.Report.Followups) != 1 {
		t.Fatalf("reported attempt report = %+v, want structured report", recorded.Report)
	}

	if recorded.ReportRef == nil || recorded.ReportRef.Kind != KindReport {
		t.Fatalf("report ref = %+v, want canonical report artifact", recorded.ReportRef)
	}

	if recorded.Report.ReportRef == nil || *recorded.Report.ReportRef != *recorded.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", recorded.Report.ReportRef, recorded.ReportRef)
	}

	var payload attemptReportedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}

	if payload.State != AttemptStateReported || payload.Report.Summary != testReportSummaryPlanReady {
		t.Fatalf("report payload = %+v, want reported payload", payload)
	}

	if payload.Report.ReportRef == nil || *payload.Report.ReportRef != *recorded.ReportRef {
		t.Fatalf("payload report ref = %+v, want %+v", payload.Report.ReportRef, recorded.ReportRef)
	}

	if len(payload.FollowupRefs) != 1 || payload.FollowupRefs[0].Kind != KindFollowup {
		t.Fatalf("followup refs = %+v, want one followup artifact ref", payload.FollowupRefs)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want nil", loaded.Status.ActiveAttempt)
	}

	replayed := loaded.Status.Attempts[0]
	if replayed.Report == nil || replayed.Report.ChangedPaths[0] != "README.md" {
		t.Fatalf("replayed report = %+v, want structured changed path", replayed.Report)
	}

	if replayed.ReportRef == nil || *replayed.ReportRef != *recorded.ReportRef {
		t.Fatalf("replayed report ref = %+v, want %+v", replayed.ReportRef, recorded.ReportRef)
	}

	reportContent := string(readFile(t, filepath.Join(run.Path, filepath.FromSlash(recorded.ReportRef.Path))))
	assertContainsAll(t, reportContent, []string{
		"# Worker Report\n",
		"## Metadata\n",
		"- run_id: `record-report-run`",
		"- step_id: `plan`",
		"- agent_id: `planner`",
		"- attempt_id: `attempt-001`",
		"- status/result: `done/ready`",
		"## Summary\n\nPlan is ready.",
		"## Commands\n\n- go test ./internal/runstore",
		"## Tests\n\n- go test ./internal/runstore",
		"## Risks\n\n- none",
		"## Changed Paths\n\n- README.md",
		"## Follow-ups\n\n- Document report summaries\n  Details: Later summary work.",
	})
	assertReportSectionOrder(t, reportContent, []string{
		"# Worker Report",
		"## Metadata",
		"## Summary",
		"## Commands",
		"## Tests",
		"## Risks",
		"## Changed Paths",
		"## Follow-ups",
	})

	followups := string(readFile(t, filepath.Join(run.Path, followupsName)))
	if !strings.Contains(followups, "## Document report summaries") || !strings.Contains(followups, "Source: report") {
		t.Fatalf("followups.md = %q, want report-sourced followup", followups)
	}

	if got := loaded.Status.Artifacts[len(loaded.Status.Artifacts)-2]; got.Kind != KindReport || got.EventSequence != event.Sequence {
		t.Fatalf("report artifact = %+v, want report ref owned by attempt.reported event %d", got, event.Sequence)
	}

	if got := loaded.Status.Artifacts[len(loaded.Status.Artifacts)-1]; got.Kind != KindFollowup || got.EventSequence != event.Sequence {
		t.Fatalf("last artifact = %+v, want followup ref owned by attempt.reported event %d", got, event.Sequence)
	}
}

func TestRecordAttemptReportFollowupStageFailureLeavesAttemptActive(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-followup-stage-failure")
	attemptID := "attempt-followup-stage-failure"
	startAttemptForTest(t, store, run.ID, attemptID)
	linkPromptAndLogForTest(t, store, run.ID, attemptID)
	recordProcessForTest(t, store, run.ID, attemptID)
	denyStatusMaterializationOrSkip(t, run.Path)

	_, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: attemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
			Followups: []Followup{{Title: "Later"}},
		},
	})
	requireErrorContains(t, err, followupsName)

	loaded, loadErr := store.LoadContext(context.Background(), run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != attemptID {
		t.Fatalf("active attempt = %+v, want unchanged active attempt", loaded.Status.ActiveAttempt)
	}

	if got := loaded.Status.Attempts[len(loaded.Status.Attempts)-1].State; got != AttemptStateActive {
		t.Fatalf("latest attempt state = %q, want active", got)
	}

	if got := string(readFile(t, filepath.Join(run.Path, followupsName))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestRecordAttemptReportRequiresReportRunID(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-missing-run-id")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	_, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testAttemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
		},
	})
	requireErrorContains(t, err, "run id is required")
}

func TestRecordAttemptReportReturnsTargetErrorForStaleAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-stale-attempt")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	_, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testOldAttemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
		},
	})

	var targetErr *ReportTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("error = %v, want ReportTargetError", err)
	}

	if targetErr.Reason != reportTargetCurrentAttemptReason {
		t.Fatalf("target reason = %q, want current active attempt reason", targetErr.Reason)
	}
}

func TestRecordAttemptReportRejectsStartingAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-starting-report-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)

	_, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testAttemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
		},
		ReportContent:    []byte("# Should not persist\n"),
		ReportContentSet: true,
	})
	requireErrorContains(t, err, "state", "starting", "want active")

	loaded, loadErr := store.LoadContext(context.Background(), run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.State != AttemptStateStarting {
		t.Fatalf("active attempt = %+v, want unchanged starting attempt", loaded.Status.ActiveAttempt)
	}

	entries, readErr := os.ReadDir(filepath.Join(run.Path, "reports"))
	if readErr != nil {
		t.Fatalf("read reports dir: %v", readErr)
	}

	if len(entries) != 0 {
		t.Fatalf("reports dir entries = %v, want no orphan report artifact", entries)
	}
}

func TestRecordAttemptInvalidReportCreatesNoReportArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-invalid-report-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	recorded, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateInvalidReport,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testAttemptID,
			Status:    attemptStatusFailed,
			Result:    AttemptResultInvalidReport,
			Summary:   "invalid report: bad result",
		},
	})
	if err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	if recorded.ReportRef != nil || recorded.Report == nil || recorded.Report.ReportRef != nil {
		t.Fatalf("report refs = %+v/%+v, want none for invalid report", recorded.ReportRef, recorded.Report)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	for _, artifact := range loaded.Status.Artifacts {
		if artifact.Kind == KindReport {
			t.Fatalf("artifact = %+v, want no report artifact for invalid report", artifact)
		}
	}
}

func TestRecordAttemptReportWritesReportArtifactAtomically(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-artifact-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	recorded, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testAttemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
		},
		ReportContent:    []byte("# Detail\n"),
		ReportContentSet: true,
	})
	if err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	if recorded.ReportRef == nil {
		t.Fatal("report ref = nil, want stored report artifact")
	}

	if recorded.Report == nil || recorded.Report.ReportRef == nil || *recorded.Report.ReportRef != *recorded.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", recorded.Report, recorded.ReportRef)
	}

	got := string(readFile(t, filepath.Join(run.Path, filepath.FromSlash(recorded.ReportRef.Path))))
	assertContainsAll(t, got, []string{
		"# Worker Report\n",
		"## Metadata\n",
		"## Summary\n\nPlan is ready.",
		"## Report Detail\n\n# Detail\n",
	})
	assertReportSectionOrder(t, got, []string{"# Worker Report", "## Metadata", "## Summary", "## Report Detail"})

	if !strings.HasSuffix(got, "## Report Detail\n\n# Detail\n") {
		t.Fatalf("report content suffix = %q, want exact supplied detail after Report Detail heading", got)
	}
}

func TestRecordAttemptReportRejectsCallerSuppliedReportRef(t *testing.T) {
	const attemptID = testAttemptID

	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-existing-ref-run")
	startAttemptForTest(t, store, run.ID, attemptID)
	linkPromptAndLogForTest(t, store, run.ID, attemptID)
	recordProcessForTest(t, store, run.ID, attemptID)

	ref, err := store.WriteArtifactContext(context.Background(), run.ID, Artifact{
		Kind:    KindReport,
		Name:    "existing-report",
		Content: []byte("# Existing\n"),
		Time:    time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	_, _, err = store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: attemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
			ReportRef: &ref,
		},
	})
	requireErrorContains(t, err, "report_ref", "cannot be supplied")

	loaded, loadErr := store.LoadContext(context.Background(), run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != attemptID {
		t.Fatalf("active attempt = %+v, want unchanged attempt", loaded.Status.ActiveAttempt)
	}

	if loaded.Status.ActiveAttempt.ReportRef != nil || loaded.Status.ActiveAttempt.Report != nil {
		t.Fatalf("active attempt = %+v, want no report attached", loaded.Status.ActiveAttempt)
	}

	var reportArtifacts int

	for _, artifact := range loaded.Status.Artifacts {
		if artifact.Kind == KindReport {
			reportArtifacts++
		}
	}

	if reportArtifacts != 1 {
		t.Fatalf("report artifact count = %d, want only existing artifact", reportArtifacts)
	}
}

func TestRecordAttemptReportReusesSameContentOrphanReportArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-orphan-retry-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)
	linkPromptAndLogForTest(t, store, run.ID, testAttemptID)
	recordProcessForTest(t, store, run.ID, testAttemptID)

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	relPath, err := artifactPath(KindReport, testWorkflowStatePlan, nextEventSequence(loaded))
	if err != nil {
		t.Fatalf("artifactPath returned error: %v", err)
	}

	orphanPath := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0o750); err != nil {
		t.Fatalf("mkdir report dir: %v", err)
	}

	orphanContent := canonicalReportMarkdown(Report{
		RunID:     run.ID,
		StepID:    testWorkflowStatePlan,
		AgentID:   testAgentPlanner,
		AttemptID: testAttemptID,
		Status:    attemptStatusDone,
		Result:    reportResultReady,
		Summary:   testReportSummaryPlanReady,
	}, []byte("# Detail\n"), true)

	if err := os.WriteFile(orphanPath, orphanContent, 0o600); err != nil {
		t.Fatalf("write orphan report: %v", err)
	}

	recorded, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    testWorkflowStatePlan,
			AgentID:   testAgentPlanner,
			AttemptID: testAttemptID,
			Status:    attemptStatusDone,
			Result:    reportResultReady,
			Summary:   testReportSummaryPlanReady,
		},
		ReportContent:    []byte("# Detail\n"),
		ReportContentSet: true,
	})
	if err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	if recorded.ReportRef == nil || recorded.ReportRef.Path != relPath {
		t.Fatalf("report ref = %+v, want reused %s", recorded.ReportRef, relPath)
	}

	if recorded.Report == nil || recorded.Report.ReportRef == nil || *recorded.Report.ReportRef != *recorded.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", recorded.Report, recorded.ReportRef)
	}

	if got := string(readFile(t, orphanPath)); got != string(orphanContent) {
		t.Fatalf("report content = %q, want reused orphan content", got)
	}
}

func TestRecordIgnoredReportDoesNotMutateActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-run")
	startAttemptForTest(t, store, run.ID, testAttemptID)

	event, err := store.RecordIgnoredReportContext(context.Background(), run.ID, IgnoreReportRequest{
		RunID:     run.ID,
		StepID:    testWorkflowStatePlan,
		AgentID:   testAgentPlanner,
		AttemptID: testOldAttemptID,
		Reason:    reportTargetCurrentAttemptReason,
		Errors:    []string{"attempt mismatch"},
		Time:      time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordIgnoredReport returned error: %v", err)
	}

	if event.Type != eventReportIgnored {
		t.Fatalf("event type = %q, want %q", event.Type, eventReportIgnored)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != testAttemptID {
		t.Fatalf("active attempt = %+v, want unchanged attempt-001", loaded.Status.ActiveAttempt)
	}
}

func TestRecordIgnoredReportRejectsMismatchedRunID(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-mismatch-run")

	_, err := store.RecordIgnoredReportContext(context.Background(), run.ID, IgnoreReportRequest{
		RunID:     testOtherRunID,
		StepID:    testWorkflowStatePlan,
		AgentID:   testAgentPlanner,
		AttemptID: testOldAttemptID,
		Reason:    reportTargetCurrentAttemptReason,
	})
	requireErrorContains(t, err, "run_id", testOtherRunID, "does not match")
}

func TestLoadRejectsIgnoredReportRunIDMismatch(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-replay-mismatch-run")
	events := readRunEvents(t, run)

	payload, err := marshalPayload(reportIgnoredPayload{
		RunID:  testOtherRunID,
		Reason: reportTargetCurrentAttemptReason,
	})
	if err != nil {
		t.Fatalf("marshal report ignored payload: %v", err)
	}

	events = append(events, Event{
		SchemaVersion: schemaVersion,
		Sequence:      len(events) + 1,
		RunID:         run.ID,
		Type:          eventReportIgnored,
		Time:          time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
		Payload:       payload,
	})
	writeRunEvents(t, run, events)

	_, err = store.LoadContext(context.Background(), run.ID)
	requireErrorContains(t, err, "report ignored run_id", testOtherRunID, "does not match")
}

func assertContainsAll(t *testing.T, got string, wants []string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
}

func assertReportSectionOrder(t *testing.T, got string, sections []string) {
	t.Helper()

	last := -1

	for _, section := range sections {
		next := strings.Index(got, section)
		if next == -1 {
			t.Fatalf("content missing section %q:\n%s", section, got)
		}

		if next <= last {
			t.Fatalf("section %q is out of order in:\n%s", section, got)
		}

		last = next
	}
}
