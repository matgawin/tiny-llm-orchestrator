package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestAppendEventPreservesOrderedLogAndUpdatesStatus(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "run-1")
	payload := json.RawMessage(`{"step":"plan"}`)
	beforeContent := readFile(t, runEventsPath(run))
	beforeFirstLine := bytes.SplitN(beforeContent, []byte("\n"), 2)[0]

	event, err := store.AppendEvent(run.ID, Event{
		Type:    "workflow.step.selected",
		Time:    time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	if event.Sequence != 2 {
		t.Fatalf("sequence = %d, want 2", event.Sequence)
	}
	events := readRunEvents(t, run)
	if got := len(events); got != 2 {
		t.Fatalf("event count = %d, want 2", got)
	}
	afterContent := readFile(t, runEventsPath(run))
	afterFirstLine := bytes.SplitN(afterContent, []byte("\n"), 2)[0]
	if !bytes.Equal(afterFirstLine, beforeFirstLine) {
		t.Fatalf("first event line changed after append:\nbefore: %s\nafter:  %s", beforeFirstLine, afterFirstLine)
	}
	if events[0].Type != eventRunCreated || events[1].Type != "workflow.step.selected" {
		t.Fatalf("event order = %s then %s, want created then selected", events[0].Type, events[1].Type)
	}
	status := readRunStatus(t, run)
	if status.LastSequence != 2 {
		t.Fatalf("status last sequence = %d, want 2", status.LastSequence)
	}
	if status.State != stateRunning {
		t.Fatalf("status state = %q, want unchanged running state for generic event", status.State)
	}
}

func TestAppendEventRejectsReservedEventTypes(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "reserved-events")

	for _, eventType := range []string{
		eventRunCreated,
		eventStatusUpdated,
		eventArtifactWritten,
		eventAttemptStarted,
		eventAttemptPrompted,
		eventAttemptLogged,
		eventAttemptProcess,
		eventAttemptFinished,
		eventAttemptRecovered,
	} {
		t.Run(eventType, func(t *testing.T) {
			_, err := store.AppendEvent(run.ID, Event{Type: eventType})
			requireErrorContains(t, err, "store-owned")
		})
	}
}

func TestAttemptLifecyclePersistsAndReplaysActiveAttemptState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-run")
	startedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	started, event, err := store.StartAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-001",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            startedAt,
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	if event.Type != eventAttemptStarted || started.State != AttemptStateStarting {
		t.Fatalf("started/event = %+v / %+v, want starting event", started, event)
	}
	ref, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
		Time:    startedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	prompted, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: started.AttemptID,
		PromptRef: ref,
		Time:      startedAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	if prompted.PromptRef == nil || *prompted.PromptRef != ref {
		t.Fatalf("prompt ref = %+v, want %+v", prompted.PromptRef, ref)
	}
	logRef, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindLog,
		Name:    "plan-attempt-001",
		Content: []byte("log\n"),
		Time:    startedAt.Add(2500 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	logged, _, err := store.RecordAttemptLog(run.ID, AttemptLogRequest{
		AttemptID: started.AttemptID,
		LogRef:    logRef,
		Time:      startedAt.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if logged.LogRef == nil || *logged.LogRef != logRef {
		t.Fatalf("log ref = %+v, want %+v", logged.LogRef, logRef)
	}
	processed, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        started.AttemptID,
		PID:              12345,
		ProcessStartTime: testProcessStartTime,
		Time:             startedAt.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	if processed.PID != 12345 {
		t.Fatalf("pid = %d, want 12345", processed.PID)
	}
	if processed.State != AttemptStateActive {
		t.Fatalf("state = %q, want active after process metadata", processed.State)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != started.AttemptID {
		t.Fatalf("active attempt = %+v, want attempt-001", loaded.Status.ActiveAttempt)
	}
	if got := loaded.Status.Attempts[0].PID; got != 12345 {
		t.Fatalf("replayed pid = %d, want 12345", got)
	}
	if got := loaded.Status.Attempts[0].State; got != AttemptStateActive {
		t.Fatalf("replayed state = %q, want active", got)
	}
	if loaded.Status.Attempts[0].LogRef == nil || *loaded.Status.Attempts[0].LogRef != logRef {
		t.Fatalf("replayed log ref = %+v, want %+v", loaded.Status.Attempts[0].LogRef, logRef)
	}
}

func TestRecordAttemptProcessOnlyTransitionsStartingAttemptOnce(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-process-once-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")

	if _, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        "attempt-001",
		PID:              12345,
		ProcessStartTime: testProcessStartTime,
	}); err != nil {
		t.Fatalf("RecordAttemptProcess first call returned error: %v", err)
	}

	_, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        "attempt-001",
		PID:              23456,
		ProcessStartTime: testProcessStartTime,
	})
	requireErrorContains(t, err, "want starting")
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := loaded.Status.ActiveAttempt.PID; got != 12345 {
		t.Fatalf("pid after duplicate process record = %d, want original 12345", got)
	}
}

func TestLoadRejectsDuplicateAttemptProcessStarted(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-process-replay-once-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        "attempt-001",
		PID:              12345,
		ProcessStartTime: testProcessStartTime,
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	events := readRunEvents(t, run)
	var duplicate Event
	for _, event := range events {
		if event.Type == eventAttemptProcess {
			duplicate = event
			break
		}
	}
	duplicate.Sequence = len(events) + 1
	writeRunEvents(t, run, append(events, duplicate))

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "want starting")
}

func TestLoadRejectsAttemptWithNonPositiveDurations(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-duration-replay-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	mutateRunEventPayload(t, run, eventAttemptStarted, func(payload *attemptStartedPayload) {
		payload.Attempt.Timeout = "0s"
	})
	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "timeout", "> 0")

	mutateRunEventPayload(t, run, eventAttemptStarted, func(payload *attemptStartedPayload) {
		payload.Attempt.Timeout = "30m0s"
		payload.Attempt.ReportExitGrace = "not-a-duration"
	})
	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "report_exit_grace", "> 0")
}

func TestAttemptArtifactRefsMustBeRecordedWithExpectedKind(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-ref-kind-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	logRef, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindLog,
		Name:    "plan-attempt-001",
		Content: []byte("log\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}

	_, _, err = store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: logRef,
	})
	requireErrorContains(t, err, "kind", string(KindPrompt))
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if loaded.Status.ActiveAttempt.PromptRef != nil {
		t.Fatalf("prompt ref = %+v, want unchanged nil after wrong-kind ref", loaded.Status.ActiveAttempt.PromptRef)
	}

	_, _, err = store.RecordAttemptLog(run.ID, AttemptLogRequest{
		AttemptID: "attempt-001",
		LogRef: ArtifactRef{
			Kind:          KindLog,
			Path:          "logs/000099-missing.log",
			Name:          "missing",
			EventSequence: 99,
		},
	})
	requireErrorContains(t, err, "not recorded")
}

func TestLoadRejectsAttemptArtifactRefsNotRecordedWithExpectedKind(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-replay-ref-kind-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	promptRef, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: promptRef,
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	mutateRunEventPayload(t, run, eventAttemptPrompted, func(payload *attemptPromptedPayload) {
		payload.PromptRef.Kind = KindLog
	})

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "kind", string(KindPrompt))
}

func TestAttemptPromptAndLogCanOnlyBeLinkedOnceWhileStarting(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-link-once-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	promptRef := writeArtifactForTest(t, store, run.ID, KindPrompt, "plan", []byte("prompt\n"))
	logRef := writeArtifactForTest(t, store, run.ID, KindLog, "plan-attempt-001", []byte("log\n"))
	if _, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: promptRef,
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	_, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: promptRef,
	})
	requireErrorContains(t, err, "already has prompt ref")
	if _, _, err := store.RecordAttemptLog(run.ID, AttemptLogRequest{
		AttemptID: "attempt-001",
		LogRef:    logRef,
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	_, _, err = store.RecordAttemptLog(run.ID, AttemptLogRequest{
		AttemptID: "attempt-001",
		LogRef:    logRef,
	})
	requireErrorContains(t, err, "already has log ref")
	if _, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        "attempt-001",
		PID:              12345,
		ProcessStartTime: testProcessStartTime,
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	latePrompt := writeArtifactForTest(t, store, run.ID, KindPrompt, "late", []byte("late\n"))
	_, _, err = store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: latePrompt,
	})
	requireErrorContains(t, err, "want starting")
}

func TestRecordAttemptProcessRequiresPromptAndLogRefs(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-process-requires-refs-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	_, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID: "attempt-001",
		PID:       12345,
	})
	requireErrorContains(t, err, "prompt ref is required")
	promptRef := writeArtifactForTest(t, store, run.ID, KindPrompt, "plan", []byte("prompt\n"))
	if _, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{
		AttemptID: "attempt-001",
		PromptRef: promptRef,
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	_, _, err = store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID: "attempt-001",
		PID:       12345,
	})
	requireErrorContains(t, err, "log ref is required")
}

func TestRecordAttemptProcessValidatesProcessStartTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-process-start-time-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{
		AttemptID:        "attempt-001",
		PID:              12345,
		ProcessStartTime: "not-a-number",
	})
	requireErrorContains(t, err, "process_start_time", "decimal digits")
}

func TestLoadRejectsLateOrDuplicatePromptLogAndProcessBeforeRefs(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-replay-order-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	promptRef := writeArtifactForTest(t, store, run.ID, KindPrompt, "plan", []byte("prompt\n"))
	logRef := writeArtifactForTest(t, store, run.ID, KindLog, "plan-attempt-001", []byte("log\n"))
	if _, _, err := store.RecordAttemptPrompt(run.ID, AttemptPromptRequest{AttemptID: "attempt-001", PromptRef: promptRef}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(run.ID, AttemptLogRequest{AttemptID: "attempt-001", LogRef: logRef}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	events := readRunEvents(t, run)
	var duplicatePrompt Event
	for _, event := range events {
		if event.Type == eventAttemptPrompted {
			duplicatePrompt = event
			break
		}
	}
	duplicatePrompt.Sequence = len(events) + 1
	writeRunEvents(t, run, append(events, duplicatePrompt))
	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "already has prompt ref")

	events = readRunEvents(t, run)
	writeRunEvents(t, run, events[:len(events)-1])
	if _, _, err := store.RecordAttemptProcess(run.ID, AttemptProcessRequest{AttemptID: "attempt-001", PID: 12345, ProcessStartTime: testProcessStartTime}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	events = readRunEvents(t, run)
	var lateLog Event
	for _, event := range events {
		if event.Type == eventAttemptLogged {
			lateLog = event
			break
		}
	}
	lateLog.Sequence = len(events) + 1
	writeRunEvents(t, run, append(events, lateLog))
	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "want starting")

	run = createManualRun(t, store, "attempt-replay-process-before-refs-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	processPayload, err := marshalPayload(attemptProcessPayload{AttemptID: "attempt-001", PID: 12345})
	if err != nil {
		t.Fatalf("marshal process payload: %v", err)
	}
	processEvent := Event{
		SchemaVersion: schemaVersion,
		Sequence:      3,
		Time:          time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
		RunID:         run.ID,
		Type:          eventAttemptProcess,
		Payload:       processPayload,
	}
	writeRunEvents(t, run, append(readRunEvents(t, run), processEvent))
	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "prompt ref is required")
}

func TestLoadRejectsInvalidProcessStartTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "attempt-process-start-time-replay-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	processPayload, err := marshalPayload(attemptProcessPayload{
		AttemptID:        "attempt-001",
		PID:              12345,
		ProcessStartTime: "not-a-number",
	})
	if err != nil {
		t.Fatalf("marshal process payload: %v", err)
	}
	events := readRunEvents(t, run)
	events = append(events, Event{
		SchemaVersion: schemaVersion,
		Sequence:      len(events) + 1,
		Time:          time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
		RunID:         run.ID,
		Type:          eventAttemptProcess,
		Payload:       processPayload,
	})
	writeRunEvents(t, run, events)

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "process_start_time", "decimal digits")
}

func TestStartAttemptAllowsOnlyOneConcurrentActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "concurrent-attempt-run")
	startedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, attemptID := range []string{"attempt-a", "attempt-b"} {
		wg.Add(1)
		go func(attemptID string) {
			defer wg.Done()
			_, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
				StepID:          "plan",
				AgentID:         "planner",
				AttemptID:       attemptID,
				Timeout:         30 * time.Minute,
				ReportExitGrace: 30 * time.Second,
				Time:            startedAt,
			})
			errs <- err
		}(attemptID)
	}
	wg.Wait()
	close(errs)

	successes := 0
	failures := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		requireErrorContains(t, err, "already has active attempt")
		failures++
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes/failures = %d/%d, want 1/1", successes, failures)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt == nil || len(loaded.Status.Attempts) != 1 {
		t.Fatalf("loaded active/history = %+v / %+v, want one active attempt", loaded.Status.ActiveAttempt, loaded.Status.Attempts)
	}
}

func TestStatusBackedWritesShareRunLock(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "shared-lock-run")
	const writers = 30
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0:
				_, err = store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
			case 1:
				_, _, err = store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState})
			default:
				_, err = store.WriteArtifact(run.ID, Artifact{
					Kind:    KindFollowup,
					Name:    "followup",
					Content: []byte("- follow-up\n"),
				})
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write returned error: %v", err)
		}
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error after concurrent writes: %v", err)
	}
	if got, want := loaded.Status.LastSequence, writers+1; got != want {
		t.Fatalf("last sequence = %d, want %d", got, want)
	}
	seen := map[int]bool{}
	for _, event := range loaded.Events {
		if seen[event.Sequence] {
			t.Fatalf("duplicate sequence %d in events: %+v", event.Sequence, loaded.Events)
		}
		seen[event.Sequence] = true
	}
}

func TestStartAttemptRejectsDuplicateHistoricalAttemptID(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "duplicate-attempt-run")
	startedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	startAttemptForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		ExitState: "exited",
		Time:      startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}

	_, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-001",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            startedAt.Add(2 * time.Minute),
	})
	requireErrorContains(t, err, "already has attempt", "attempt-001")
}

func TestStartAttemptRejectsUnconsumedLauncherOutcome(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "unconsumed-outcome-attempt-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateMissingReport,
		Status:    "failed",
		Result:    "missing_report",
		ExitState: "exited",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}

	_, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-002",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
	})
	requireErrorContains(t, err, "unconsumed launcher outcome", "attempt-001")
}

func TestStartAttemptRejectsRetryLineageWithoutConsumedOutcome(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "retry-lineage-without-consume-run")

	_, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-001",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		RetryLineage:    &RetryLineage{StepID: "plan", Counts: map[string]int{"failed/missing_report": 1}},
	})
	requireErrorContains(t, err, "retry lineage requires consume_attempt_id")
}

func TestStartAttemptContextCancellationWhileLocalRunLockHeld(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "local-lock-cancel-attempt-run")

	err := runCanceledWhileLocalRunLockHeld(t, store, run.ID, "StartAttemptContext", func(ctx context.Context) error {
		_, _, err := store.StartAttemptContext(ctx, run.ID, StartAttemptRequest{
			StepID:          "plan",
			AgentID:         "planner",
			AttemptID:       "attempt-001",
			Timeout:         30 * time.Minute,
			ReportExitGrace: 30 * time.Second,
		})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartAttemptContext error = %v, want context.Canceled", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt != nil || len(loaded.Status.Attempts) != 0 {
		t.Fatalf("attempt state = active %+v history %+v, want no attempt", loaded.Status.ActiveAttempt, loaded.Status.Attempts)
	}
}

func TestRecordAttemptProcessContextCancellationWhileLocalRunLockHeld(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "local-lock-cancel-process-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")

	err := runCanceledWhileLocalRunLockHeld(t, store, run.ID, "RecordAttemptProcessContext", func(ctx context.Context) error {
		_, _, err := store.RecordAttemptProcessContext(ctx, run.ID, AttemptProcessRequest{
			AttemptID:        "attempt-001",
			PID:              12345,
			ProcessStartTime: testProcessStartTime,
		})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecordAttemptProcessContext error = %v, want context.Canceled", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt == nil {
		t.Fatal("active attempt = nil, want starting attempt")
	}
	if loaded.Status.ActiveAttempt.State != AttemptStateStarting || loaded.Status.ActiveAttempt.PID != 0 {
		t.Fatalf("active attempt = %+v, want starting without process metadata", *loaded.Status.ActiveAttempt)
	}
}

func runCanceledWhileLocalRunLockHeld(t *testing.T, store *Store, runID, name string, operation func(context.Context) error) error {
	t.Helper()
	locked, release, holderDone := holdRunLock(t, store, runID)
	<-locked
	ctx, cancel := context.WithCancel(context.Background())
	lockWait := observeRunStoreLocalLockWait(t, runID)
	done := make(chan error, 1)
	go func() {
		done <- operation(ctx)
	}()
	waitForRunStoreLocalLockWaiter(t, lockWait)
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not return after context cancellation while local lock was held", name)
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("held lock returned error: %v", err)
	}
	return err
}

func TestStartAttemptRejectsTerminalRunStates(t *testing.T) {
	for _, state := range []string{readyForHumanState, "blocked_for_human", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "terminal-attempt-"+state)
			if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: state}); err != nil {
				t.Fatalf("UpdateStatus returned error: %v", err)
			}

			_, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
				StepID:          "plan",
				AgentID:         "planner",
				AttemptID:       "attempt-terminal",
				Timeout:         30 * time.Minute,
				ReportExitGrace: 30 * time.Second,
			})
			requireErrorContains(t, err, "state is", state, stateRunning)

			loaded, loadErr := store.Load(run.ID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			if loaded.Status.ActiveAttempt != nil {
				t.Fatalf("active attempt = %+v, want nil", loaded.Status.ActiveAttempt)
			}
		})
	}
}

func TestUpdateStatusRejectsNonRunningStateWhileAttemptActive(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "active-status-update-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState})
	requireErrorContains(t, err, "active attempt", "ready_for_human")

	if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: stateRunning}); err != nil {
		t.Fatalf("UpdateStatus running returned error: %v", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt == nil {
		t.Fatal("active attempt = nil, want still active")
	}
}

func TestLoadRejectsAttemptStartedReplayBeforeConsumingLauncherOutcome(t *testing.T) {
	assertLoadRejectsAttemptStartAfterUnconsumedLauncherOutcome(t, "replayed original attempt.started", func(t *testing.T, run *Run) Event {
		t.Helper()
		events := readRunEvents(t, run)
		for _, event := range events {
			if event.Type == eventAttemptStarted {
				event.Sequence = len(events) + 1
				return event
			}
		}
		t.Fatal("attempt.started event not found")
		return Event{}
	})
}

func TestLoadRejectsNewAttemptStartedAfterUnconsumedLauncherOutcome(t *testing.T) {
	assertLoadRejectsAttemptStartAfterUnconsumedLauncherOutcome(t, "new attempt.started", func(t *testing.T, run *Run) Event {
		t.Helper()
		attempt, err := newStartedAttempt(run.ID, StartAttemptRequest{
			StepID:          "plan",
			AgentID:         "planner",
			AttemptID:       "attempt-002",
			Timeout:         30 * time.Minute,
			ReportExitGrace: 30 * time.Second,
		})
		if err != nil {
			t.Fatalf("newStartedAttempt returned error: %v", err)
		}
		payload, err := marshalPayload(attemptStartedPayload{Attempt: attempt})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		events := readRunEvents(t, run)
		return Event{
			SchemaVersion: schemaVersion,
			Sequence:      len(events) + 1,
			Time:          time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
			RunID:         run.ID,
			Type:          eventAttemptStarted,
			Payload:       payload,
		}
	})
}

func assertLoadRejectsAttemptStartAfterUnconsumedLauncherOutcome(t *testing.T, name string, nextEvent func(*testing.T, *Run) Event) {
	t.Helper()
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "unconsumed-outcome-"+strings.ReplaceAll(name, " ", "-"))
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateMissingReport,
		Status:    "failed",
		Result:    "missing_report",
		ExitState: "exited",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	events := readRunEvents(t, run)
	unconsumedStart := nextEvent(t, run)
	writeRunEvents(t, run, append(events, unconsumedStart))

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "unconsumed launcher outcome", "attempt-001")
}

func TestLoadRejectsStatusUpdatedToTerminalWhileAttemptActive(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "active-status-replay-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	payload, err := marshalPayload(statusUpdatedPayload{State: readyForHumanState})
	if err != nil {
		t.Fatalf("marshal status payload: %v", err)
	}
	events := readRunEvents(t, run)
	events = append(events, Event{
		SchemaVersion: schemaVersion,
		Sequence:      len(events) + 1,
		Time:          time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
		RunID:         run.ID,
		Type:          eventStatusUpdated,
		Payload:       payload,
	})
	writeRunEvents(t, run, events)

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "updates run state", readyForHumanState, "active")
}

func TestLoadRejectsAttemptStartedAfterTerminalRunState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "terminal-attempt-replay-run")
	if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	attempt, err := newStartedAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-terminal-replay",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("newStartedAttempt returned error: %v", err)
	}
	payload, err := marshalPayload(attemptStartedPayload{Attempt: attempt})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	events := readRunEvents(t, run)
	events = append(events, Event{
		SchemaVersion: schemaVersion,
		Sequence:      loaded.Status.LastSequence + 1,
		Time:          time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		RunID:         run.ID,
		Type:          eventAttemptStarted,
		Payload:       payload,
	})
	writeRunEvents(t, run, events)

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "starts attempt while run state is", readyForHumanState, stateRunning)
}

func TestLoadRejectsPollutedAttemptStartedPayload(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "polluted-started-attempt-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	events := readRunEvents(t, run)
	for i := range events {
		if events[i].Type != eventAttemptStarted {
			continue
		}
		var payload attemptStartedPayload
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			t.Fatalf("unmarshal attempt.started payload: %v", err)
		}
		payload.Attempt.PromptRef = &ArtifactRef{Kind: KindPrompt, Path: "prompts/000002-plan.md", Name: "plan", EventSequence: 2}
		nextPayload, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal attempt.started payload: %v", err)
		}
		events[i].Payload = nextPayload
		break
	}
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "prompt_ref", "starting attempt")
}

func TestStartAttemptWithZeroTimeReplays(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "zero-time-attempt-run")
	started, _, err := store.StartAttempt(run.ID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-zero-time",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !loaded.Status.ActiveAttempt.StartedAt.Equal(started.StartedAt) {
		t.Fatalf("replayed started_at = %s, want %s", loaded.Status.ActiveAttempt.StartedAt, started.StartedAt)
	}
}

func TestFinishAttemptTerminalizesActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-attempt-run")
	startedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	startAttemptForTest(t, store, run.ID, "attempt-001")
	_, logRef := linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	exitCode := 0
	finished, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateMissingReport,
		Status:    "failed",
		Result:    "missing_report",
		ExitCode:  &exitCode,
		ExitState: "exited",
		LogRef:    &logRef,
		Time:      startedAt.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	if finished.State != AttemptStateMissingReport || finished.LogRef == nil || *finished.LogRef != logRef {
		t.Fatalf("finished = %+v, want terminal missing_report with log", finished)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want nil", loaded.Status.ActiveAttempt)
	}
	if got := loaded.Status.Attempts[0].State; got != AttemptStateMissingReport {
		t.Fatalf("replayed attempt state = %q, want missing_report", got)
	}
}

func TestRecordAttemptReportTerminalizesActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	recorded, event, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:        run.ID,
			StepID:       "plan",
			AgentID:      "planner",
			AttemptID:    "attempt-001",
			Status:       "done",
			Result:       "ready",
			Summary:      "Plan is ready.",
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
	if recorded.State != AttemptStateReported || recorded.Status != "done" || recorded.Result != "ready" {
		t.Fatalf("reported attempt = %+v, want reported done/ready", recorded)
	}
	if recorded.Report == nil || recorded.Report.Summary != "Plan is ready." || len(recorded.Report.Followups) != 1 {
		t.Fatalf("reported attempt report = %+v, want structured report", recorded.Report)
	}
	var payload attemptReportedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	if payload.State != AttemptStateReported || payload.Report.Summary != "Plan is ready." {
		t.Fatalf("report payload = %+v, want reported payload", payload)
	}
	if len(payload.FollowupRefs) != 1 || payload.FollowupRefs[0].Kind != KindFollowup {
		t.Fatalf("followup refs = %+v, want one followup artifact ref", payload.FollowupRefs)
	}

	loaded, err := store.Load(run.ID)
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
	followups := string(readFile(t, filepath.Join(run.Path, followupsName)))
	if !strings.Contains(followups, "## Document report summaries") || !strings.Contains(followups, "Source: report") {
		t.Fatalf("followups.md = %q, want report-sourced followup", followups)
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

	_, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: attemptID,
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
			Followups: []Followup{{Title: "Later"}},
		},
	})
	requireErrorContains(t, err, followupsName)

	loaded, loadErr := store.Load(run.ID)
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
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
	})
	requireErrorContains(t, err, "run id is required")
}

func TestRecordAttemptReportReturnsTargetErrorForStaleAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-stale-attempt")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "old-attempt",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
	})
	var targetErr *ReportTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("error = %v, want ReportTargetError", err)
	}
	if targetErr.Reason != "report does not target current active attempt" {
		t.Fatalf("target reason = %q, want current active attempt reason", targetErr.Reason)
	}
}

func TestRecordAttemptReportRejectsStartingAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-starting-report-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		ReportName:       "plan",
		ReportContent:    []byte("# Should not persist\n"),
		ReportContentSet: true,
	})
	requireErrorContains(t, err, "state", "starting", "want active")
	loaded, loadErr := store.Load(run.ID)
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

func TestRecordAttemptReportWritesReportArtifactAtomically(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-artifact-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")

	recorded, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		ReportName:       "plan",
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
	if got := string(readFile(t, filepath.Join(run.Path, filepath.FromSlash(recorded.ReportRef.Path)))); got != "# Detail\n" {
		t.Fatalf("report detail = %q, want copied detail", got)
	}
}

func TestRecordAttemptReportRejectsCallerSuppliedReportRef(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "record-report-existing-ref-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	ref, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindReport,
		Name:    "existing-report",
		Content: []byte("# Existing\n"),
		Time:    time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	_, _, err = store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
			ReportRef: &ref,
		},
	})
	requireErrorContains(t, err, "report_ref", "cannot be supplied")
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != "attempt-001" {
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
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	relPath, err := artifactPath(KindReport, "plan", nextEventSequence(loaded))
	if err != nil {
		t.Fatalf("artifactPath returned error: %v", err)
	}
	orphanPath := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0o750); err != nil {
		t.Fatalf("mkdir report dir: %v", err)
	}
	if err := os.WriteFile(orphanPath, []byte("# Detail\n"), 0o600); err != nil {
		t.Fatalf("write orphan report: %v", err)
	}

	recorded, _, err := store.RecordAttemptReport(run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "done",
			Result:    "ready",
			Summary:   "Plan is ready.",
		},
		ReportName:       "plan",
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
	if got := string(readFile(t, orphanPath)); got != "# Detail\n" {
		t.Fatalf("report detail = %q, want reused orphan content", got)
	}
}

func TestRecordIgnoredReportDoesNotMutateActiveAttempt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	event, err := store.RecordIgnoredReport(run.ID, IgnoreReportRequest{
		RunID:     run.ID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "old-attempt",
		Reason:    "report does not target current active attempt",
		Errors:    []string{"attempt mismatch"},
		Time:      time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordIgnoredReport returned error: %v", err)
	}
	if event.Type != eventReportIgnored {
		t.Fatalf("event type = %q, want %q", event.Type, eventReportIgnored)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != "attempt-001" {
		t.Fatalf("active attempt = %+v, want unchanged attempt-001", loaded.Status.ActiveAttempt)
	}
}

func TestRecordIgnoredReportRejectsMismatchedRunID(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-mismatch-run")

	_, err := store.RecordIgnoredReport(run.ID, IgnoreReportRequest{
		RunID:     "other-run",
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "old-attempt",
		Reason:    "report does not target current active attempt",
	})
	requireErrorContains(t, err, "run_id", "other-run", "does not match")
}

func TestLoadRejectsIgnoredReportRunIDMismatch(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "ignored-report-replay-mismatch-run")
	events := readRunEvents(t, run)
	payload, err := marshalPayload(reportIgnoredPayload{
		RunID:  "other-run",
		Reason: "report does not target current active attempt",
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

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "report ignored run_id", "other-run", "does not match")
}

func TestFinishAttemptPreservesExistingLogRefWhenRequestOmitsIt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-preserve-log-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	_, logRef := linkPromptAndLogForTest(t, store, run.ID, "attempt-001")

	finished, event, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		ExitState: "exited",
	})
	if err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	if finished.LogRef == nil || *finished.LogRef != logRef {
		t.Fatalf("finished log ref = %+v, want preserved %+v", finished.LogRef, logRef)
	}
	var payload attemptFinishedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal finish payload: %v", err)
	}
	if payload.LogRef == nil || *payload.LogRef != logRef {
		t.Fatalf("finish event log ref = %+v, want preserved %+v", payload.LogRef, logRef)
	}
}

func TestLoadPreservesExistingLogRefWhenTerminalEventOmitsIt(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-replay-preserve-log-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	_, logRef := linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		ExitState: "exited",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	mutateRunEventPayload(t, run, eventAttemptFinished, func(payload *attemptFinishedPayload) {
		payload.LogRef = nil
	})

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	got := loaded.Status.Attempts[0].LogRef
	if got == nil || *got != logRef {
		t.Fatalf("replayed log ref = %+v, want preserved %+v", got, logRef)
	}
}

func TestFinishAttemptWithZeroTimeUsesCommittedEventTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-zero-time-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	finished, event, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
	})
	if err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	assertFinishedAtMatchesEvent(t, finished, event)
	status := readRunStatus(t, run)
	assertAttemptFinishedAt(t, status.Attempts[0], event.Time)
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	assertAttemptFinishedAt(t, loaded.Status.Attempts[0], event.Time)
}

func TestRecoverAttemptWithZeroTimeUsesCommittedEventTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "recover-zero-time-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	recovered, event, err := store.RecoverAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
	})
	if err != nil {
		t.Fatalf("RecoverAttempt returned error: %v", err)
	}
	assertFinishedAtMatchesEvent(t, recovered, event)
	status := readRunStatus(t, run)
	assertAttemptFinishedAt(t, status.Attempts[0], event.Time)
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	assertAttemptFinishedAt(t, loaded.Status.Attempts[0], event.Time)
}

func TestFinishAttemptRejectsNonTerminalState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-non-terminal-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateActive,
		Status:    "failed",
		Result:    "process_error",
	})
	requireErrorContains(t, err, "not terminal")
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if loaded.Status.ActiveAttempt == nil {
		t.Fatal("active attempt cleared after rejected non-terminal finish")
	}
}

func TestRecoverAttemptRejectsNonTerminalState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "recover-non-terminal-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.RecoverAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateStarting,
		Status:    "failed",
		Result:    "process_error",
	})
	requireErrorContains(t, err, "not terminal")
}

func TestFinishAttemptRejectsInvalidTerminalOutcomeTuple(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-invalid-tuple-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	_, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateTimedOut,
		Status:    "done",
		Result:    "process_error",
	})
	requireErrorContains(t, err, "terminal outcome", "invalid")
}

func TestFinishAttemptRejectsLaunchedOutcomesBeforeProcessMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  string
		result string
	}{
		{name: "missing-report", state: AttemptStateMissingReport, result: "missing_report"},
		{name: "timeout", state: AttemptStateTimedOut, result: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "finish-no-process-"+tc.name)
			startAttemptForTest(t, store, run.ID, "attempt-001")
			linkPromptAndLogForTest(t, store, run.ID, "attempt-001")

			_, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
				AttemptID: "attempt-001",
				State:     tc.state,
				Status:    "failed",
				Result:    tc.result,
				ExitState: "exited",
			})
			requireErrorContains(t, err, "no process metadata")
		})
	}
}

func TestFinishAttemptAllowsProcessErrorBeforeProcessMetadata(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-pre-process-error-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")

	finished, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		ExitState: "start_failed",
	})
	if err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	if finished.State != AttemptStateProcessError {
		t.Fatalf("finished state = %q, want process_error", finished.State)
	}
}

func TestLoadRejectsInvalidTerminalOutcomeTuple(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-invalid-tuple-replay-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateTimedOut,
		Status:    "failed",
		Result:    "timeout",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	mutateRunEventPayload(t, run, eventAttemptFinished, func(payload *attemptFinishedPayload) {
		payload.Status = "done"
		payload.Result = "process_error"
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "terminal outcome", "invalid")
}

func TestLoadRejectsLaunchedOutcomeBeforeProcessMetadata(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-no-process-replay-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	mutateRunEventPayload(t, run, eventAttemptFinished, func(payload *attemptFinishedPayload) {
		payload.State = AttemptStateMissingReport
		payload.Result = "missing_report"
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "no process metadata")
}

func TestFinishAttemptLogRefMustBeRecordedKindLog(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "finish-log-ref-kind-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	promptRef, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}

	_, _, err = store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		LogRef:    &promptRef,
	})
	requireErrorContains(t, err, "kind", string(KindLog))

	missingLog := ArtifactRef{Kind: KindLog, Path: "logs/000099-missing.log", Name: "missing", EventSequence: 99}
	_, _, err = store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		LogRef:    &missingLog,
	})
	requireErrorContains(t, err, "not recorded")
}

func TestLoadRejectsTerminalNonTerminalStateAndInvalidLogRef(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "terminal-replay-invalid-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	logRef, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindLog,
		Name:    "plan-attempt-001",
		Content: []byte("log\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		LogRef:    &logRef,
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	mutateRunEventPayload(t, run, eventAttemptFinished, func(payload *attemptFinishedPayload) {
		payload.State = AttemptStateActive
	})
	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "not terminal")

	mutateRunEventPayload(t, run, eventAttemptFinished, func(payload *attemptFinishedPayload) {
		payload.State = AttemptStateProcessError
		if payload.LogRef == nil {
			t.Fatal("finish payload log ref is nil")
		}
		payload.LogRef.Kind = KindPrompt
	})
	_, err = store.Load(run.ID)
	requireErrorContains(t, err, "kind", string(KindLog))
}

func startAttemptForTest(t *testing.T, store *Store, runID, attemptID string) {
	t.Helper()
	if _, _, err := store.StartAttempt(runID, StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
}

func linkPromptAndLogForTest(t *testing.T, store *Store, runID, attemptID string) (ArtifactRef, ArtifactRef) {
	t.Helper()
	promptRef := writeArtifactForTest(t, store, runID, KindPrompt, "plan", []byte("prompt\n"))
	if _, _, err := store.RecordAttemptPrompt(runID, AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef := writeArtifactForTest(t, store, runID, KindLog, "plan-"+attemptID, []byte("log\n"))
	if _, _, err := store.RecordAttemptLog(runID, AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	return promptRef, logRef
}

func recordProcessForTest(t *testing.T, store *Store, runID, attemptID string) {
	t.Helper()
	if _, _, err := store.RecordAttemptProcess(runID, AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: testProcessStartTime,
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
}

func writeArtifactForTest(t *testing.T, store *Store, runID string, kind ArtifactKind, name string, content []byte) ArtifactRef {
	t.Helper()
	ref, err := store.WriteArtifact(runID, Artifact{Kind: kind, Name: name, Content: content})
	if err != nil {
		t.Fatalf("WriteArtifact %s returned error: %v", kind, err)
	}
	return ref
}

func assertFinishedAtMatchesEvent(t *testing.T, attempt Attempt, event Event) {
	t.Helper()
	assertAttemptFinishedAt(t, attempt, event.Time)
}

func assertAttemptFinishedAt(t *testing.T, attempt Attempt, want time.Time) {
	t.Helper()
	if attempt.FinishedAt == nil {
		t.Fatalf("attempt %q finished_at = nil, want %s", attempt.AttemptID, want)
	}
	if !attempt.FinishedAt.Equal(want) {
		t.Fatalf("attempt %q finished_at = %s, want %s", attempt.AttemptID, attempt.FinishedAt, want)
	}
}

func observeRunStoreLocalLockWait(t *testing.T, runID string) <-chan struct{} {
	t.Helper()
	waiting := make(chan struct{})
	var once sync.Once
	cleanup := SetRunLockWaitObserverForTest(func(lockName string) {
		if lockName == runID {
			once.Do(func() {
				close(waiting)
			})
		}
	})
	t.Cleanup(cleanup)
	return waiting
}

func waitForRunStoreLocalLockWaiter(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("run-store goroutine did not reach the local lock wait path")
	}
}

func TestAppendEventReturnsCommittedEventWhenStatusMaterializationFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "append-status-failure")
	denyStatusMaterializationOrSkip(t, run.Path)

	event, err := store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
	requireStatusMaterializationError(t, err, run.Path)
	if event.Sequence != 2 || event.Type != "workflow.step.finished" {
		t.Fatalf("committed event = %+v, want sequence 2 workflow.step.finished", event)
	}
	events := readRunEvents(t, run)
	if got := len(events); got != 2 {
		t.Fatalf("event count = %d, want committed appended event", got)
	}
}

func TestUpdateStatusAppendsEventAndMaterializesLatestState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "status-run")
	now := time.Date(2026, 5, 2, 15, 30, 0, 0, time.UTC)

	status, event, err := store.UpdateStatus(run.ID, StatusUpdate{
		State: readyForHumanState,
		Time:  now,
	})
	if err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	if event.Type != eventStatusUpdated || event.Sequence != 2 {
		t.Fatalf("event = %+v, want status.updated sequence 2", event)
	}
	if status.State != readyForHumanState || status.LastSequence != 2 {
		t.Fatalf("status = %+v, want materialized %s at sequence 2", status, readyForHumanState)
	}
	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.State != readyForHumanState {
		t.Fatalf("loaded state = %q, want %s", loaded.Status.State, readyForHumanState)
	}
}

func TestUpdateStatusReturnsCommittedEventWhenStatusMaterializationFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "update-status-failure")
	denyStatusMaterializationOrSkip(t, run.Path)

	status, event, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState})
	requireStatusMaterializationError(t, err, run.Path)
	if event.Sequence != 2 || event.Type != eventStatusUpdated {
		t.Fatalf("committed event = %+v, want status.updated sequence 2", event)
	}
	if status.State != readyForHumanState || status.LastSequence != 2 {
		t.Fatalf("returned status = %+v, want committed in-memory status", status)
	}
}

func TestEventAppendPossiblyCommitted(t *testing.T) {
	for _, tc := range []struct {
		name              string
		possiblyAppended  bool
		underlyingMessage string
		want              bool
	}{
		{name: "ambiguous append", possiblyAppended: true, underlyingMessage: "close failed", want: true},
		{name: "definite failure", possiblyAppended: false, underlyingMessage: "open failed", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &EventAppendError{
				Path:             "events.jsonl",
				PossiblyAppended: tc.possiblyAppended,
				Err:              errors.New(tc.underlyingMessage),
			}

			if got := eventAppendPossiblyCommitted(err); got != tc.want {
				t.Fatalf("eventAppendPossiblyCommitted returned %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWriteAPIsRecoverFromStaleMaterializedStatus(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "stale-status")
	staleEvent := Event{
		SchemaVersion: schemaVersion,
		Sequence:      2,
		Time:          time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC),
		RunID:         run.ID,
		Type:          "workflow.step.selected",
		Payload:       json.RawMessage(`{}`),
	}
	if err := appendEvent(runEventsPath(run), staleEvent); err != nil {
		t.Fatalf("append stale event: %v", err)
	}

	event, err := store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
	if err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	if event.Sequence != 3 {
		t.Fatalf("AppendEvent sequence = %d, want 3 from event log replay", event.Sequence)
	}
	status, event, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState})
	if err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	if event.Sequence != 4 || status.LastSequence != 4 {
		t.Fatalf("UpdateStatus sequence/status = %d/%d, want 4/4", event.Sequence, status.LastSequence)
	}
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	if ref.EventSequence != 5 {
		t.Fatalf("artifact event sequence = %d, want 5", ref.EventSequence)
	}
	events := readRunEvents(t, run)
	if got := len(events); got != 5 {
		t.Fatalf("event count after replayed writes = %d, want 5", got)
	}
}

func TestWriteAPIsRejectWrongStatusRunIDBeforeMutating(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "wrong-status-id")
	status := readRunStatus(t, run)
	status.RunID = "other-run"
	writeRunStatus(t, run, status)

	_, err := store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
	requireErrorContains(t, err, "run_id")
	events := readRunEvents(t, run)
	if got := len(events); got != 1 {
		t.Fatalf("event count after rejected write = %d, want 1", got)
	}
}

func TestWriteArtifactPersistsSupportedArtifactsAndLoadsRefs(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "run-artifacts")
	artifacts := []Artifact{
		{Kind: KindTaskContext, Name: "task", Content: []byte("# Task\n")},
		{Kind: KindTaskSnapshot, Name: "task", Content: []byte(`{"source":"beads"}`)},
		{Kind: KindReport, Name: "plan", Content: []byte("# Report\n")},
		{Kind: KindPrompt, Name: "code", Content: []byte("Prompt\n")},
		{Kind: KindLog, Name: "review", Content: []byte("log line\n")},
		{Kind: KindSnapshot, Name: "vcs", Content: []byte(`{"changed":[]}`)},
		{Kind: KindSummary, Name: "orchestrator", Content: []byte("Summary\n")},
		{Kind: KindFollowup, Name: "followup", Content: []byte("- [ ] Follow up\n")},
	}

	var refs []ArtifactRef
	var reportRef ArtifactRef
	for _, artifact := range artifacts {
		ref, err := store.WriteArtifact(run.ID, artifact)
		if err != nil {
			t.Fatalf("WriteArtifact(%s) returned error: %v", artifact.Kind, err)
		}
		refs = append(refs, ref)
		if artifact.Kind == KindReport {
			reportRef = ref
		}
		assertLatestArtifactWrite(t, run, artifact, ref)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got, want := len(loaded.Status.Artifacts), len(artifacts); got != want {
		t.Fatalf("loaded artifact refs = %d, want %d", got, want)
	}
	for i, ref := range refs {
		if loaded.Status.Artifacts[i].Path != ref.Path {
			t.Fatalf("artifact ref %d path = %q, want %q", i, loaded.Status.Artifacts[i].Path, ref.Path)
		}
	}
	if !strings.HasPrefix(reportRef.Path, "reports/000004-plan.md") {
		t.Fatalf("report path = %q, want sequence-prefixed semantic path", reportRef.Path)
	}
}

func TestWriteArtifactIfStateRequiresMatchingState(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "state-guarded-artifact")
	summary := Artifact{
		Kind:    KindSummary,
		Name:    "orchestrator",
		Content: []byte("Summary\n"),
	}

	_, err := store.WriteArtifactIfState(run.ID, readyForHumanState, summary)
	requireErrorContains(t, err, "state is", stateRunning)
	var stateErr *StateMismatchError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error = %T %[1]v, want StateMismatchError", err)
	}
	if stateErr.RunID != run.ID || stateErr.Got != stateRunning || stateErr.Want != readyForHumanState {
		t.Fatalf("state mismatch = %+v, want run/state precondition details", stateErr)
	}

	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if len(loaded.Status.Artifacts) != 0 {
		t.Fatalf("artifacts = %+v, want none after rejected state-guarded write", loaded.Status.Artifacts)
	}

	if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	ref, err := store.WriteArtifactIfState(run.ID, readyForHumanState, summary)
	if err != nil {
		t.Fatalf("WriteArtifactIfState returned error: %v", err)
	}
	if ref.Kind != KindSummary {
		t.Fatalf("artifact kind = %q, want %q", ref.Kind, KindSummary)
	}
}

func TestReadArtifactReadsRecordedArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "read-artifact")
	ref, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindPrompt,
		Name:    "plan",
		Content: []byte("Prompt\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	content, err := store.ReadArtifact(run.ID, ref)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	if string(content) != "Prompt\n" {
		t.Fatalf("artifact content = %q, want prompt content", string(content))
	}
}

func TestOpenArtifactAppendWritesRecordedArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithActiveLog(t, store, "append-artifact")

	file, err := store.OpenArtifactAppend(run.ID, ref)
	if err != nil {
		t.Fatalf("OpenArtifactAppend returned error: %v", err)
	}
	if _, err := file.WriteString("second\n"); err != nil {
		_ = file.Close()
		t.Fatalf("append artifact: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}
	content, err := store.ReadArtifact(run.ID, ref)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	if string(content) != "first\nsecond\n" {
		t.Fatalf("artifact content = %q, want appended log", string(content))
	}
}

func createRunWithActiveLog(t *testing.T, store *Store, runID string) (*Run, ArtifactRef) {
	t.Helper()
	run := createManualRun(t, store, runID)
	startAttemptForTest(t, store, run.ID, "attempt-001")
	ref, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindLog,
		Name:    "plan-attempt-001",
		Content: []byte("first\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(run.ID, AttemptLogRequest{AttemptID: "attempt-001", LogRef: ref}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	return run, ref
}

func TestOpenArtifactAppendRejectsSymlinkArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithActiveLog(t, store, "append-artifact-symlink")
	path := filepath.Join(run.Path, filepath.FromSlash(ref.Path))
	if err := os.Remove(path); err != nil {
		t.Fatalf("replace artifact with symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.log"), path); err != nil {
		t.Fatalf("create artifact symlink: %v", err)
	}

	file, err := store.OpenArtifactAppend(run.ID, ref)
	if file != nil {
		_ = file.Close()
	}
	requireErrorContains(t, err, "symlink")
}

func TestOpenArtifactAppendRejectsNonLogArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "append-artifact-non-log")
	ref, err := store.WriteArtifact(run.ID, Artifact{
		Kind:    KindPrompt,
		Name:    "plan",
		Content: []byte("Prompt\n"),
	})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	file, err := store.OpenArtifactAppend(run.ID, ref)
	if file != nil {
		_ = file.Close()
	}
	requireErrorContains(t, err, "kind", string(KindLog))
}

func TestOpenArtifactAppendRejectsTerminalAttemptLog(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithActiveLog(t, store, "append-terminal-log")
	if _, _, err := store.FinishAttempt(run.ID, FinishAttemptRequest{
		AttemptID: "attempt-001",
		State:     AttemptStateProcessError,
		Status:    "failed",
		Result:    "process_error",
		ExitState: "start_failed",
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}

	file, err := store.OpenArtifactAppend(run.ID, ref)
	if file != nil {
		_ = file.Close()
	}
	requireErrorContains(t, err, "current active attempt log")
}

func TestOpenArtifactAppendRejectsFIFOArtifact(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithActiveLog(t, store, "append-artifact-fifo")
	path := filepath.Join(run.Path, filepath.FromSlash(ref.Path))
	if err := os.Remove(path); err != nil {
		t.Fatalf("replace artifact with fifo: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create artifact fifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		file, err := store.OpenArtifactAppend(run.ID, ref)
		if file != nil {
			_ = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		requireErrorContains(t, err, "not a regular file")
	case <-time.After(time.Second):
		t.Fatal("OpenArtifactAppend blocked on FIFO")
	}
}

func TestReadArtifactRejectsUnrecordedRefs(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "unrecorded-artifact")

	_, err := store.ReadArtifact(run.ID, ArtifactRef{
		Kind:          KindReport,
		Path:          "reports/000002-plan.md",
		Name:          "plan",
		EventSequence: 2,
	})
	requireErrorContains(t, err, "is not recorded")
}

func TestReadArtifactRejectsMalformedRefs(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "malformed-artifact-ref")

	_, err := store.ReadArtifact(run.ID, ArtifactRef{
		Kind:          KindReport,
		Path:          "prompts/000002-plan.md",
		Name:          "plan",
		EventSequence: 2,
	})
	requireErrorContains(t, err, "does not match kind")
}

func assertLatestArtifactWrite(t *testing.T, run *Run, artifact Artifact, ref ArtifactRef) {
	t.Helper()
	artifactPath := filepath.Join(run.Path, filepath.FromSlash(ref.Path))
	assertFile(t, artifactPath)
	if got := readFile(t, artifactPath); !bytes.Equal(got, artifact.Content) {
		t.Fatalf("%s content = %q, want %q", ref.Path, string(got), string(artifact.Content))
	}
	events := readRunEvents(t, run)
	lastEvent := events[len(events)-1]
	if lastEvent.Type != eventArtifactWritten {
		t.Fatalf("last event type = %q, want %s", lastEvent.Type, eventArtifactWritten)
	}
	var payload artifactWrittenPayload
	if err := json.Unmarshal(lastEvent.Payload, &payload); err != nil {
		t.Fatalf("unmarshal artifact payload: %v", err)
	}
	if payload.Artifact != ref {
		t.Fatalf("artifact payload = %+v, want %+v", payload.Artifact, ref)
	}
	status := readRunStatus(t, run)
	if got := status.Artifacts[len(status.Artifacts)-1]; got != ref {
		t.Fatalf("materialized artifact ref = %+v, want %+v", got, ref)
	}
}

func TestWriteArtifactRejectsDuplicateTaskSingletons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    ArtifactKind
		path    string
		content []byte
	}{
		{name: "context", kind: KindTaskContext, path: "task/context.md", content: []byte("first context\n")},
		{name: "snapshot", kind: KindTaskSnapshot, path: "task/snapshot.json", content: []byte(`{"first":true}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "duplicate-task-"+tc.name)
			ref, err := store.WriteArtifact(run.ID, Artifact{Kind: tc.kind, Name: tc.name, Content: tc.content})
			if err != nil {
				t.Fatalf("first WriteArtifact returned error: %v", err)
			}
			if ref.Path != tc.path {
				t.Fatalf("artifact path = %q, want %q", ref.Path, tc.path)
			}

			_, err = store.WriteArtifact(run.ID, Artifact{Kind: tc.kind, Name: tc.name, Content: []byte("second\n")})
			requireErrorContains(t, err, "already been written")
			if got := readFile(t, filepath.Join(run.Path, filepath.FromSlash(tc.path))); !bytes.Equal(got, tc.content) {
				t.Fatalf("singleton content = %q, want original %q", string(got), string(tc.content))
			}
			events := readRunEvents(t, run)
			if got := len(events); got != 2 {
				t.Fatalf("event count = %d, want only initial and first artifact event", got)
			}
		})
	}
}

func TestWriteArtifactRejectsPreexistingUnreferencedArtifactFile(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "preexisting-artifact")
	path := filepath.Join(run.Path, "reports", "000002-plan.md")
	original := []byte("partial artifact\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write preexisting artifact: %v", err)
	}

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("new artifact\n")})
	requireErrorContains(t, err, "already exists")
	if got := readFile(t, path); !bytes.Equal(got, original) {
		t.Fatalf("preexisting artifact content = %q, want unchanged %q", string(got), string(original))
	}
	events := readRunEvents(t, run)
	if got := len(events); got != 1 {
		t.Fatalf("event count = %d, want only initial event", got)
	}
}

func TestWriteArtifactReturnsCommittedRefWhenStatusMaterializationFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "artifact-status-failure")
	denyStatusMaterializationOrSkip(t, run.Path)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	requireStatusMaterializationError(t, err, run.Path)
	if ref.Path != "reports/000002-plan.md" || ref.EventSequence != 2 {
		t.Fatalf("committed ref = %+v, want report sequence 2", ref)
	}
	if got := string(readFile(t, filepath.Join(run.Path, filepath.FromSlash(ref.Path)))); got != "report\n" {
		t.Fatalf("artifact content = %q, want committed report", got)
	}
	events := readRunEvents(t, run)
	if got := len(events); got != 2 {
		t.Fatalf("event count = %d, want artifact event committed", got)
	}
}

func TestWriteArtifactAppendsFollowupsToSingleFile(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-append")

	first, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "first", Content: []byte("- first\n")})
	if err != nil {
		t.Fatalf("WriteArtifact first followup returned error: %v", err)
	}
	second, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "second", Content: []byte("- second\n")})
	if err != nil {
		t.Fatalf("WriteArtifact second followup returned error: %v", err)
	}

	if first.Path != followupsName || second.Path != followupsName {
		t.Fatalf("followup paths = %q, %q; want %s", first.Path, second.Path, followupsName)
	}
	if first.EventSequence == second.EventSequence {
		t.Fatalf("followup event sequences both = %d, want distinct sequences", first.EventSequence)
	}
	content := readFile(t, filepath.Join(run.Path, followupsName))
	if got, want := string(content), "- first\n- second\n"; got != want {
		t.Fatalf("followups.md content = %q, want %q", got, want)
	}
	status := readRunStatus(t, run)
	if got := len(status.Artifacts); got != 2 {
		t.Fatalf("status artifact refs = %d, want 2", got)
	}
	if status.Artifacts[0] != first || status.Artifacts[1] != second {
		t.Fatalf("status artifact refs = %+v, want [%+v %+v]", status.Artifacts, first, second)
	}
}

func TestRecordFollowupAppendsAttributedMarkdown(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-append")
	recordedAt := time.Date(2026, 5, 4, 12, 3, 0, 0, time.UTC)

	first, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{
			Title:   "Document report summaries",
			Details: "Capture summary context once follow-up recording lands.",
		},
		Source:    FollowupSourceReport,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
		Time:      recordedAt,
	})
	if err != nil {
		t.Fatalf("RecordFollowup report returned error: %v", err)
	}
	second, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Create release note"},
		Source:   FollowupSourceOrchestrator,
		Time:     recordedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordFollowup orchestrator returned error: %v", err)
	}

	if first.Kind != KindFollowup || second.Kind != KindFollowup || first.Path != followupsName || second.Path != followupsName {
		t.Fatalf("followup refs = %+v %+v, want followups.md refs", first, second)
	}
	content := string(readFile(t, filepath.Join(run.Path, followupsName)))
	want := `## Document report summaries

Source: report
Step: plan
Agent: planner
Attempt: attempt-001
Recorded-At: 2026-05-04T12:03:00Z

Capture summary context once follow-up recording lands.

## Create release note

Source: orchestrator
Recorded-At: 2026-05-04T12:04:00Z

`
	if content != want {
		t.Fatalf("followups.md = %q, want %q", content, want)
	}
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := len(loaded.Status.Artifacts); got != 2 {
		t.Fatalf("artifact count = %d, want 2", got)
	}
}

func TestRecordFollowupUsesSameDefaultTimeForMarkdownAndEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-zero-time")

	ref, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Create release note"},
		Source:   FollowupSourceOrchestrator,
	})
	if err != nil {
		t.Fatalf("RecordFollowup returned error: %v", err)
	}
	loaded, loadErr := store.Load(run.ID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	event := loaded.Events[len(loaded.Events)-1]
	if ref.EventSequence != event.Sequence {
		t.Fatalf("ref sequence = %d, want event sequence %d", ref.EventSequence, event.Sequence)
	}
	want := "Recorded-At: " + event.Time.Format(time.RFC3339)
	if got := string(readFile(t, filepath.Join(run.Path, followupsName))); !strings.Contains(got, want) {
		t.Fatalf("followups.md = %q, want %q", got, want)
	}
}

func TestRecordFollowupRequiresTitleAndReportAttribution(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "typed-followup-validation")

	_, err := store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: " \t"},
		Source:   FollowupSourceOrchestrator,
	})
	requireErrorContains(t, err, "title is required")
	_, err = store.RecordFollowup(run.ID, RecordFollowupRequest{
		Followup: Followup{Title: "Report follow-up"},
		Source:   FollowupSourceReport,
		StepID:   "plan",
	})
	requireErrorContains(t, err, "agent id is required")
	if got := string(readFile(t, filepath.Join(run.Path, followupsName))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestWriteArtifactRejectsPreexistingFollowupsSymlink(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-symlink")
	followupsPath := filepath.Join(run.Path, followupsName)
	outside := outsideFileSymlink(t, run.Path, followupsPath, "outside-followups.md", []byte("outside\n"))

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "next", Content: []byte("inside\n")})
	requireErrorContains(t, err, "symlink")
	if got := string(readFile(t, outside)); got != "outside\n" {
		t.Fatalf("outside followups content = %q, want unchanged external content", got)
	}
}

func TestWriteArtifactRejectsPreexistingArtifactFIFO(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "artifact-fifo-write")
	path := filepath.Join(run.Path, "reports", "000002-plan.md")
	makeFIFO(t, path)

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	requireErrorContains(t, err, "regular file")
}

func TestWriteAPIsRejectRunDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "write-run-dir-symlink")
	realRunPath := filepath.Join(root, "outside-write-run")
	relocatePathBehindSymlink(t, run.Path, realRunPath)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{
			name: "AppendEvent",
			call: func() error {
				_, err := store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
				return err
			},
		},
		{
			name: "UpdateStatus",
			call: func() error {
				_, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState})
				return err
			},
		},
		{
			name: "WriteArtifact",
			call: func() error {
				_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			requireErrorContains(t, err, "symlink")
		})
	}
}

func TestAppendEventRejectsEventsSymlinkBeforeMutating(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "append-events-symlink")
	eventsPath := runEventsPath(run)
	outside := outsideFileSymlink(t, run.Path, eventsPath, "outside-events.jsonl", []byte("outside\n"))

	_, err := store.AppendEvent(run.ID, Event{Type: "workflow.step.finished"})
	requireErrorContains(t, err, "symlink")
	if got := string(readFile(t, outside)); got != "outside\n" {
		t.Fatalf("outside events content = %q, want unchanged", got)
	}
}

func TestWriteArtifactRejectsMissingInitialParentDirectory(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-artifact-parent")
	reportsDir := filepath.Join(run.Path, "reports")
	if err := os.Remove(reportsDir); err != nil {
		t.Fatalf("remove reports dir: %v", err)
	}

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	requireErrorContains(t, err, "layout", "reports")
}

func TestWriteArtifactRejectsSymlinkedArtifactParent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "write-symlink-parent")
	reportsDir := filepath.Join(run.Path, "reports")
	outsideDir := outsideDirSymlink(t, run.Path, reportsDir, "outside-reports")

	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	requireErrorContains(t, err, "symlink")
	if _, statErr := os.Stat(filepath.Join(outsideDir, "000002-plan.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside artifact stat err = %v, want no escaped write", statErr)
	}
}

func TestWriteArtifactRollsBackCommittedArtifactWhenEventAppendFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "artifact-rollback")
	eventsPath := runEventsPath(run)
	denyFileAppendOrSkip(t, eventsPath)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	if err == nil {
		t.Fatal("WriteArtifact returned nil error, want append failure")
	}
	if ref.Path != "" {
		t.Fatalf("ref = %+v, want zero ref on failure", ref)
	}
	artifactPath := filepath.Join(run.Path, "reports", "000002-plan.md")
	if _, statErr := os.Stat(artifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("artifact stat err = %v, want artifact rolled back", statErr)
	}
}

func TestWriteArtifactRollsBackFollowupAppendWhenEventAppendFails(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "followup-rollback")
	first := []byte("- first\n")
	if _, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "first", Content: first}); err != nil {
		t.Fatalf("initial WriteArtifact returned error: %v", err)
	}
	eventsPath := runEventsPath(run)
	eventsBefore := readFile(t, eventsPath)
	denyFileAppendOrSkip(t, eventsPath)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindFollowup, Name: "second", Content: []byte("- second\n")})
	if err == nil {
		t.Fatal("WriteArtifact returned nil error, want append failure")
	}
	if ref.Path != "" {
		t.Fatalf("ref = %+v, want zero ref on definite append failure", ref)
	}
	if got := readFile(t, filepath.Join(run.Path, followupsName)); !bytes.Equal(got, first) {
		t.Fatalf("followups.md content = %q, want restored original %q", string(got), string(first))
	}
	if got := readFile(t, eventsPath); !bytes.Equal(got, eventsBefore) {
		t.Fatalf("events changed after failed followup append:\nbefore: %s\nafter:  %s", eventsBefore, got)
	}
}
