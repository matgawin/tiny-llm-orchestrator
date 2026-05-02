package runstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	for _, eventType := range []string{eventRunCreated, eventStatusUpdated, eventArtifactWritten} {
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
