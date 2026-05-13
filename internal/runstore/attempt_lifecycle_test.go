package runstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
		Status:    reportStatusDone,
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
		payload.Status = reportStatusDone
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
