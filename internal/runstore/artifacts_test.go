package runstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
