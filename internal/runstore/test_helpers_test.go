package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	readyForHumanState   = "ready_for_human"
	testProcessStartTime = "123456789"
)

func openStore(t *testing.T, root string) *Store {
	t.Helper()

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	return store
}

func createManualRun(t *testing.T, store *Store, id string) *Run {
	t.Helper()

	run, err := store.Create(CreateRunRequest{
		RunID:    id,
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 2, 14, 30, 22, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	return run
}

func createRunWithMutatedArtifactEvent(t *testing.T, store *Store, id string, mutate func(*Run, ArtifactRef, *artifactWrittenPayload)) (*Run, ArtifactRef) {
	t.Helper()
	run := createManualRun(t, store, id)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	mutateRunEventPayload(t, run, eventArtifactWritten, func(payload *artifactWrittenPayload) {
		mutate(run, ref, payload)
	})

	return run, ref
}

func createRunWithReportArtifact(t *testing.T, store *Store, id string) (*Run, ArtifactRef, string) {
	t.Helper()
	run := createManualRun(t, store, id)

	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte("report\n")})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	return run, ref, filepath.Join(run.Path, filepath.FromSlash(ref.Path))
}

func assertDir(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	if info.IsDir() {
		t.Fatalf("%s is a directory, want file", path)
	}
}

func symlinkPath(t *testing.T, linkPath, target string) {
	t.Helper()

	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func replacePathWithSymlink(t *testing.T, path, target string) {
	t.Helper()

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}

	symlinkPath(t, path, target)
}

func relocatePathBehindSymlink(t *testing.T, path, target string) {
	t.Helper()

	if err := os.Rename(path, target); err != nil {
		t.Fatalf("move %s to %s: %v", path, target, err)
	}

	symlinkPath(t, path, target)
}

func outsideFileSymlink(t *testing.T, runPath, linkPath, name string, content []byte) string {
	t.Helper()

	outside := filepath.Join(filepath.Dir(runPath), name)
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatalf("write outside file %s: %v", outside, err)
	}

	replacePathWithSymlink(t, linkPath, outside)

	return outside
}

func outsideDirSymlink(t *testing.T, runPath, linkPath, name string) string {
	t.Helper()

	outside := filepath.Join(filepath.Dir(runPath), name)
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatalf("mkdir outside dir %s: %v", outside, err)
	}

	replacePathWithSymlink(t, linkPath, outside)

	return outside
}

func requireErrorContains(t *testing.T, err error, parts ...string) {
	t.Helper()

	if err == nil {
		if len(parts) == 0 {
			t.Fatal("got nil error, want error")
		}

		t.Fatalf("got nil error, want error containing %q", parts)
	}

	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error = %q, want context %q", err, part)
		}
	}
}

func holdRunLock(t *testing.T, store *Store, runID string) (<-chan struct{}, chan<- struct{}, <-chan error) {
	t.Helper()

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- store.withRunLock(runID, func() error {
			close(locked)
			<-release

			return nil
		})
	}()

	return locked, release, done
}

func assertStillBlocked(t *testing.T, done <-chan error, name string) {
	t.Helper()

	select {
	case err := <-done:
		t.Fatalf("%s completed while run lock was held: %v", name, err)
	case <-time.After(25 * time.Millisecond):
	}
}

func requireStatusMaterializationError(t *testing.T, err error, runPath string) {
	t.Helper()

	if err == nil {
		t.Fatal("got nil error, want StatusMaterializationError")
	}

	var materializationErr *StatusMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("error = %T %v, want StatusMaterializationError", err, err)
	}

	requireErrorContains(t, err, filepath.Join(runPath, statusName))
}

func runStatusPath(run *Run) string {
	return filepath.Join(run.Path, statusName)
}

func runEventsPath(run *Run) string {
	return filepath.Join(run.Path, eventsName)
}

func readRunStatus(t *testing.T, run *Run) Status {
	t.Helper()
	return readStatusFile(t, runStatusPath(run))
}

func writeRunStatus(t *testing.T, run *Run, status Status) {
	t.Helper()

	if err := writeStatus(runStatusPath(run), status); err != nil {
		t.Fatalf("write status: %v", err)
	}
}

func readRunEvents(t *testing.T, run *Run) []Event {
	t.Helper()
	return readEventLines(t, runEventsPath(run))
}

func writeRunEvents(t *testing.T, run *Run, events []Event) {
	t.Helper()
	writeEvents(t, runEventsPath(run), events)
}

func mutateRunEventPayload[T any](t *testing.T, run *Run, eventType string, mutate func(*T)) {
	t.Helper()

	events := readRunEvents(t, run)
	for i := range events {
		if events[i].Type != eventType {
			continue
		}

		var payload T
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s payload: %v", eventType, err)
		}

		mutate(&payload)

		nextPayload, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s payload: %v", eventType, err)
		}

		events[i].Payload = nextPayload
		writeRunEvents(t, run, events)

		return
	}

	t.Fatalf("event %s not found", eventType)
}

func readStatusFile(t *testing.T, path string) Status {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}

	var status Status
	if err := json.Unmarshal(content, &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}

	return status
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return content
}

func readEventLines(t *testing.T, path string) []Event {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal event %q: %v", line, err)
		}

		events = append(events, event)
	}

	return events
}

func writeEvents(t *testing.T, path string, events []Event) {
	t.Helper()

	var content []byte

	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}

		content = append(content, line...)
		content = append(content, '\n')
	}

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}
}

func denyStatusMaterializationOrSkip(t *testing.T, runPath string) {
	t.Helper()

	lockPath := filepath.Join(runPath, ".lock")

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create run lock: %v", err)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("close run lock: %v", err)
	}

	if err := os.Chmod(runPath, 0o500); err != nil {
		t.Fatalf("chmod run dir read-only: %v", err)
	}

	t.Cleanup(func() {
		_ = os.Chmod(runPath, 0o750)
	})

	temp, err := os.CreateTemp(runPath, ".status-probe-*.tmp")
	if err == nil {
		name := temp.Name()
		_ = temp.Close()
		_ = os.Remove(name)

		t.Skip("chmod did not deny temp file creation in run directory")
	}
}

func denyFileAppendOrSkip(t *testing.T, path string) {
	t.Helper()

	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod file read-only: %v", err)
	}

	t.Cleanup(func() {
		_ = os.Chmod(path, 0o600)
	})

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		_ = file.Close()

		t.Skip("chmod did not deny appending to read-only file")
	}
}

func makeFIFO(t *testing.T, path string) {
	t.Helper()

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
}
