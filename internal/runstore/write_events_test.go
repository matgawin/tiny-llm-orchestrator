package runstore

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
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
				Err:              stableerr.New(tc.underlyingMessage),
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
