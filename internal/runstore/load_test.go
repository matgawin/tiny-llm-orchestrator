package runstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const testReportContent = "report\n"

func TestMain(m *testing.M) {
	if mode := os.Getenv("RUNSTORE_LOCK_HELPER"); mode != "" {
		os.Exit(runstoreLockHelper(mode))
	}
	os.Exit(m.Run())
}

func TestLoadRejectsSymlinkedStoreParents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "orc",
			setup: func(t *testing.T, root string) {
				realOrc := filepath.Join(root, "real-orc")
				relocatePathBehindSymlink(t, filepath.Join(root, orcDirName), realOrc)
			},
		},
		{
			name: "runs",
			setup: func(t *testing.T, root string) {
				realRuns := filepath.Join(root, "real-runs")
				relocatePathBehindSymlink(t, filepath.Join(root, orcDirName, runsDirName), realRuns)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := openStore(t, root)
			run := createManualRun(t, store, "load-parent-symlink")
			tc.setup(t, root)

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, "symlink")
		})
	}
}

func TestLoadReportsMalformedStateWithArtifactPath(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "broken-run")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	if err := os.Remove(filepath.Join(run.Path, filepath.FromSlash(ref.Path))); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}

	_, err = store.Load(run.ID)
	requireErrorContains(t, err, ref.Path)
}

func TestLoadWaitsForRunLock(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "load-lock-run")
	locked, release, lockDone := holdRunLock(t, store, run.ID)
	<-locked

	loadDone := make(chan error, 1)
	go func() {
		_, err := store.Load(run.ID)
		loadDone <- err
	}()
	assertStillBlocked(t, loadDone, "Load")
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("held lock returned error: %v", err)
	}
	if err := <-loadDone; err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestReadArtifactWaitsForRunLock(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "read-artifact-lock-run")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	locked, release, lockDone := holdRunLock(t, store, run.ID)
	<-locked

	readDone := make(chan error, 1)
	go func() {
		content, err := store.ReadArtifact(run.ID, ref)
		if err == nil && string(content) != testReportContent {
			err = fmt.Errorf("content = %q, want report", string(content))
		}
		readDone <- err
	}()
	assertStillBlocked(t, readDone, "ReadArtifact")
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("held lock returned error: %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
}

func TestReadOnlyLegacyRunWithoutLockBackfillsLock(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "legacy-no-lock-run")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	lockPath := filepath.Join(run.Path, ".lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove legacy lock: %v", err)
	}

	if _, err := store.Load(run.ID); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	assertRunLockFile(t, lockPath)

	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove legacy lock before ReadArtifact: %v", err)
	}

	if content, err := store.ReadArtifact(run.ID, ref); err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	} else if string(content) != testReportContent {
		t.Fatalf("artifact content = %q, want report", string(content))
	}
	assertRunLockFile(t, lockPath)

	if _, err := store.AppendEvent(run.ID, Event{Type: "test.backfill_lock"}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	assertRunLockFile(t, lockPath)
}

func TestLoadRejectsSymlinkedRunLock(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "lock-symlink-run")
	lockPath := filepath.Join(run.Path, ".lock")
	outside := filepath.Join(root, "outside.lock")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatalf("write outside lock: %v", err)
	}
	replacePathWithSymlink(t, lockPath, outside)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "run lock", "symlink")
}

func TestRunLockCoordinatesAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "cross-process-lock-run")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	readyPath := filepath.Join(root, "lock-ready")
	releasePath := filepath.Join(root, "lock-release")
	lockCmd := startRunstoreHelper(t, "lock", root, run.ID, ArtifactRef{}, readyPath, releasePath)
	waitForRunstoreHelperMarker(t, readyPath)
	restartLockHelper := func(t *testing.T) {
		t.Helper()
		lockCmd = startRunstoreHelper(t, "lock", root, run.ID, ArtifactRef{}, readyPath, releasePath)
		waitForRunstoreHelperMarker(t, readyPath)
	}
	releaseBlockingLock := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
			t.Fatalf("write release file: %v", err)
		}
		if err := lockCmd.Wait(); err != nil {
			t.Fatalf("lock helper returned error: %v", err)
		}
	}
	cleanupAndRestartLock := func(t *testing.T, attemptPath string) {
		t.Helper()
		if err := os.Remove(releasePath); err != nil {
			t.Fatalf("remove release file: %v", err)
		}
		if err := os.Remove(readyPath); err != nil {
			t.Fatalf("remove ready file: %v", err)
		}
		if err := os.Remove(attemptPath); err != nil {
			t.Fatalf("remove attempt file: %v", err)
		}
		restartLockHelper(t)
	}
	for _, tc := range []struct {
		name string
		mode string
		ref  ArtifactRef
	}{
		{name: "Load", mode: "load"},
		{name: "ReadArtifact", mode: "read", ref: ref},
		{name: "AppendEvent", mode: "append"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attemptPath := filepath.Join(root, "attempt-"+tc.mode)
			cmd := startRunstoreHelper(t, tc.mode, root, run.ID, tc.ref, "", "")
			done := waitRunstoreHelper(cmd)
			waitForRunstoreHelperMarker(t, attemptPath)
			assertProcessStillBlocked(t, done, tc.name)
			releaseBlockingLock(t)
			if err := <-done; err != nil {
				t.Fatalf("%s helper returned error: %v", tc.name, err)
			}
			cleanupAndRestartLock(t, attemptPath)
		})
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("write final release file: %v", err)
	}
	if err := lockCmd.Wait(); err != nil {
		t.Fatalf("lock helper returned error: %v", err)
	}
}

func runstoreLockHelper(mode string) int {
	root := os.Getenv("RUNSTORE_LOCK_ROOT")
	runID := os.Getenv("RUNSTORE_LOCK_RUN_ID")
	if root == "" || runID == "" {
		return runstoreLockExitMissingEnv
	}
	store, err := Open(root)
	if err != nil {
		return runstoreLockExitOpenStore
	}
	switch mode {
	case "lock":
		return runstoreHoldFileLock(store, runID, os.Getenv("RUNSTORE_LOCK_READY"), os.Getenv("RUNSTORE_LOCK_RELEASE"))
	case "load":
		runstoreMarkLockAttempt()
		if _, err := store.Load(runID); err != nil {
			return runstoreLockExitLoad
		}
	case "read":
		var ref ArtifactRef
		if err := json.Unmarshal([]byte(os.Getenv("RUNSTORE_LOCK_REF")), &ref); err != nil {
			return runstoreLockExitParseRef
		}
		runstoreMarkLockAttempt()
		if _, err := store.ReadArtifact(runID, ref); err != nil {
			return runstoreLockExitReadArtifact
		}
	case "append":
		runstoreMarkLockAttempt()
		if _, err := store.AppendEvent(runID, Event{Type: "test.cross_process"}); err != nil {
			return runstoreLockExitAppendEvent
		}
	default:
		return runstoreLockExitUnknownMode
	}
	return 0
}

const (
	runstoreLockExitMissingEnv   = 2
	runstoreLockExitOpenStore    = 3
	runstoreLockExitLoad         = 4
	runstoreLockExitParseRef     = 5
	runstoreLockExitReadArtifact = 6
	runstoreLockExitAppendEvent  = 7
	runstoreLockExitUnknownMode  = 8
	runstoreLockExitLockEnv      = 9
	runstoreLockExitLockLoad     = 10
	runstoreLockExitLockOpen     = 11
	runstoreLockExitLockFlock    = 12
	runstoreLockExitLockReady    = 13
	runstoreLockExitLockUnlock   = 14
)

func runstoreMarkLockAttempt() {
	if path := os.Getenv("RUNSTORE_LOCK_ATTEMPT"); path != "" {
		_ = os.WriteFile(path, nil, 0o600)
	}
}

func runstoreHoldFileLock(store *Store, runID, readyPath, releasePath string) int {
	if readyPath == "" || releasePath == "" {
		return runstoreLockExitLockEnv
	}
	run, err := store.Load(runID)
	if err != nil {
		return runstoreLockExitLockLoad
	}
	file, err := os.OpenFile(filepath.Join(run.Path, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return runstoreLockExitLockOpen
	}
	defer func() {
		_ = file.Close()
	}()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return runstoreLockExitLockFlock
	}
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		return runstoreLockExitLockReady
	}
	for {
		if _, err := os.Stat(releasePath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return runstoreLockExitLockUnlock
	}
	return 0
}

func startRunstoreHelper(t *testing.T, mode, root, runID string, ref ArtifactRef, readyPath, releasePath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	refContent, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	cmd.Env = append(os.Environ(),
		"RUNSTORE_LOCK_HELPER="+mode,
		"RUNSTORE_LOCK_ROOT="+root,
		"RUNSTORE_LOCK_RUN_ID="+runID,
		"RUNSTORE_LOCK_REF="+string(refContent),
		"RUNSTORE_LOCK_READY="+readyPath,
		"RUNSTORE_LOCK_RELEASE="+releasePath,
		"RUNSTORE_LOCK_ATTEMPT="+filepath.Join(root, "attempt-"+mode),
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	return cmd
}

func waitRunstoreHelper(cmd *exec.Cmd) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	return done
}

func assertProcessStillBlocked(t *testing.T, done <-chan error, name string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s helper completed while lock was held: %v", name, err)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForRunstoreHelperMarker(t *testing.T, path string) {
	t.Helper()
	eventuallyRunstore(t, time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func assertRunLockFile(t *testing.T, lockPath string) {
	t.Helper()
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock stat err = %v, want lock file", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("lock mode = %s, want regular file", info.Mode())
	}
}

func eventuallyRunstore(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if condition() {
		return
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestLoadRejectsBootstrapFIFO(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "events-fifo")
	eventsPath := runEventsPath(run)
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	makeFIFO(t, eventsPath)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "regular file")
}

func TestLoadRejectsRunDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run := createManualRun(t, store, "run-dir-symlink")
	realRunPath := filepath.Join(root, "outside-run")
	relocatePathBehindSymlink(t, run.Path, realRunPath)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "symlink")
}

func TestLoadRejectsMissingBootstrapFiles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remove      string
		wantContext string
	}{
		{name: "status", remove: statusName, wantContext: "status.json"},
		{name: "events", remove: eventsName, wantContext: "events.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "missing-"+tc.name)
			if err := os.Remove(filepath.Join(run.Path, tc.remove)); err != nil {
				t.Fatalf("remove %s: %v", tc.remove, err)
			}

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.wantContext)
		})
	}
}

func TestLoadRejectsBootstrapFileSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{name: "status", file: statusName},
		{name: "events", file: eventsName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "bootstrap-symlink-"+tc.name)
			path := filepath.Join(run.Path, tc.file)
			outside := filepath.Join(filepath.Dir(run.Path), "outside-"+tc.file)
			if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
				t.Fatalf("write outside %s: %v", tc.file, err)
			}
			replacePathWithSymlink(t, path, outside)

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, "symlink")
		})
	}
}

func TestLoadRejectsHalfCreatedRunDirectory(t *testing.T) {
	store := openStore(t, t.TempDir())
	runDir := filepath.Join(store.runsDir, "half-created")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		t.Fatalf("mkdir half-created run: %v", err)
	}

	_, err := store.Load("half-created")
	requireErrorContains(t, err, "layout")
}

func TestLoadRejectsMissingInitialRunLayout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remove      string
		wantContext string
	}{
		{name: "followups", remove: followupsName, wantContext: followupsName},
		{name: "reports", remove: "reports", wantContext: "reports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run := createManualRun(t, store, "missing-layout-"+tc.name)
			if err := os.Remove(filepath.Join(run.Path, tc.remove)); err != nil {
				t.Fatalf("remove %s: %v", tc.remove, err)
			}

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.wantContext)
		})
	}
}

func TestLoadRejectsMalformedArtifactFileShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(t *testing.T, run *Run, artifactPath string)
		want    string
	}{
		{
			name: "directory",
			corrupt: func(t *testing.T, _ *Run, artifactPath string) {
				t.Helper()
				if err := os.Remove(artifactPath); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				if err := os.Mkdir(artifactPath, 0o750); err != nil {
					t.Fatalf("mkdir artifact path: %v", err)
				}
			},
			want: "is a directory",
		},
		{
			name: "symlink",
			corrupt: func(t *testing.T, run *Run, artifactPath string) {
				t.Helper()
				outsideFileSymlink(t, run.Path, artifactPath, "outside.md", []byte("outside\n"))
			},
			want: "symlink",
		},
		{
			name: "fifo",
			corrupt: func(t *testing.T, _ *Run, artifactPath string) {
				t.Helper()
				if err := os.Remove(artifactPath); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				makeFIFO(t, artifactPath)
			},
			want: "regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			run, _, artifactPath := createRunWithReportArtifact(t, store, "artifact-"+tc.name)
			tc.corrupt(t, run, artifactPath)

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadRejectsSymlinkedArtifactParent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref, artifactPath := createRunWithReportArtifact(t, store, "load-symlink-parent")
	reportsDir := filepath.Join(run.Path, "reports")
	if err := os.Remove(artifactPath); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}
	outsideDir := outsideDirSymlink(t, run.Path, reportsDir, "outside-load-reports")
	if err := os.WriteFile(filepath.Join(outsideDir, filepath.Base(ref.Path)), []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "symlink")
}

func TestLoadReportsMalformedEventLine(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "bad-events")
	if err := os.WriteFile(runEventsPath(run), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write bad events: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "events.jsonl", "line 1")
}

func TestLoadRejectsEmptyEventLog(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "empty-events")
	if err := os.WriteFile(runEventsPath(run), nil, 0o600); err != nil {
		t.Fatalf("truncate events: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "no events")
}

func TestLoadRejectsMissingEventPayload(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-payload")
	events := readRunEvents(t, run)
	events[0].Payload = nil
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "payload")
}

func TestLoadRejectsEventLogWithoutTrailingNewline(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-newline")
	eventsPath := runEventsPath(run)
	content := readFile(t, eventsPath)
	content = bytes.TrimSuffix(content, []byte("\n"))
	if err := os.WriteFile(eventsPath, content, 0o600); err != nil {
		t.Fatalf("write events without trailing newline: %v", err)
	}

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "trailing newline")
}

func TestLoadReadsLargeEventPayload(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "large-event")
	payload, err := json.Marshal(map[string]string{
		"content": string(bytes.Repeat([]byte("x"), 128*1024)),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := store.AppendEvent(run.ID, Event{Type: "workflow.large-payload", Payload: payload}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Events); got != 2 {
		t.Fatalf("loaded event count = %d, want 2", got)
	}
	if got := len(loaded.Events[1].Payload); got <= 64*1024 {
		t.Fatalf("loaded payload size = %d, want over scanner default limit", got)
	}
}

func TestLoadRejectsMissingRunCreatedEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-created")
	events := readRunEvents(t, run)
	events[0].Type = "workflow.step.selected"
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "run.created")
}

func TestLoadRejectsRunCreatedWorkflowMismatch(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "created-workflow-mismatch")
	events := readRunEvents(t, run)
	events[0].Payload = json.RawMessage(`{"workflow":"other","task_slug":""}`)
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "workflow")
}

func TestLoadRejectsDuplicateRunCreatedEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "duplicate-created")
	events := readRunEvents(t, run)
	duplicate := events[0]
	duplicate.Sequence = 2
	duplicate.Time = duplicate.Time.Add(time.Second)
	writeRunEvents(t, run, append(events, duplicate))

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "duplicate", eventRunCreated)
}

func TestLoadReplaysStatusStateWhenMaterializedStatusIsStale(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "status-mismatch")
	if _, _, err := store.UpdateStatus(run.ID, StatusUpdate{State: readyForHumanState}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}
	status := readRunStatus(t, run)
	status.State = stateRunning
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.State != readyForHumanState {
		t.Fatalf("loaded state = %q, want replayed %s", loaded.Status.State, readyForHumanState)
	}
}

func TestLoadReplaysArtifactEventWhenMaterializedStatusIsStale(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-artifact-ref")
	ref, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	status := readRunStatus(t, run)
	status.Artifacts = nil
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Status.Artifacts); got != 1 {
		t.Fatalf("loaded artifact refs = %d, want 1", got)
	}
	if loaded.Status.Artifacts[0].Path != ref.Path {
		t.Fatalf("loaded artifact path = %q, want %q", loaded.Status.Artifacts[0].Path, ref.Path)
	}
}

func TestLoadUsesArtifactEventPayloadName(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithMutatedArtifactEvent(t, store, "artifact-payload-mismatch", func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		payload.Artifact.Name = "other"
	})

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.Artifacts[0].Name != "other" {
		t.Fatalf("loaded artifact name = %q, want event payload name other", loaded.Status.Artifacts[0].Name)
	}
	if loaded.Status.Artifacts[0].Path != ref.Path {
		t.Fatalf("loaded artifact path = %q, want %q", loaded.Status.Artifacts[0].Path, ref.Path)
	}
}

func TestLoadRejectsMalformedArtifactPayloadRefs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*artifactWrittenPayload)
		want   func(ArtifactRef) []string
	}{
		{
			name: "event sequence mismatch",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.EventSequence = 99
			},
			want: func(ref ArtifactRef) []string {
				return []string{"event_sequence", ref.Path}
			},
		},
		{
			name: "unclean path",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = "reports/../status.json"
			},
			want: func(ArtifactRef) []string {
				return []string{"must be clean"}
			},
		},
		{
			name: "backslash path",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = `reports/000002-plan.md\evil.md`
			},
			want: func(ArtifactRef) []string {
				return []string{"slash separators"}
			},
		},
		{
			name: "kind path mismatch",
			mutate: func(payload *artifactWrittenPayload) {
				payload.Artifact.Path = "prompts/000002-plan.md"
			},
			want: func(ArtifactRef) []string {
				return []string{"does not match kind"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			runID := "artifact-" + slugPart(tc.name, "ref")
			run, ref := createRunWithMutatedArtifactEvent(t, store, runID, func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
				tc.mutate(payload)
			})

			_, err := store.Load(run.ID)
			requireErrorContains(t, err, tc.want(ref)...)
		})
	}
}

func TestLoadRejectsNestedRepeatableArtifactPath(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, _ := createRunWithMutatedArtifactEvent(t, store, "nested-artifact-path", func(run *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		nestedPath := "reports/000002-plan/evil.md"
		if err := os.Mkdir(filepath.Join(run.Path, "reports", "000002-plan"), 0o750); err != nil {
			t.Fatalf("mkdir nested artifact dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(run.Path, filepath.FromSlash(nestedPath)), []byte("evil\n"), 0o600); err != nil {
			t.Fatalf("write nested artifact: %v", err)
		}
		payload.Artifact.Path = nestedPath
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "does not match kind")
}

func TestLoadRejectsMissingStatusTimestamps(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-status-time")
	status := readRunStatus(t, run)
	status.CreatedAt = time.Time{}
	writeRunStatus(t, run, status)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "created_at")
}

func TestLoadRejectsMissingEventTime(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-event-time")
	events := readRunEvents(t, run)
	events[0].Time = time.Time{}
	writeRunEvents(t, run, events)

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "time")
}

func TestLoadRejectsUnsupportedArtifactKind(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, ref := createRunWithMutatedArtifactEvent(t, store, "unsupported-artifact-kind", func(_ *Run, _ ArtifactRef, payload *artifactWrittenPayload) {
		payload.Artifact.Kind = ArtifactKind("unknown")
	})

	_, err := store.Load(run.ID)
	requireErrorContains(t, err, "unsupported artifact kind", ref.Path)
}

func TestLoadIgnoresStatusRefMissingArtifactEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "missing-artifact-event")
	_, err := store.WriteArtifact(run.ID, Artifact{Kind: KindReport, Name: "plan", Content: []byte(testReportContent)})
	if err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}
	events := readRunEvents(t, run)
	if err := writeInitialEventLog(filepath.Join(run.Path, eventsName+".next"), events[0]); err != nil {
		t.Fatalf("write replacement events: %v", err)
	}
	if err := os.Rename(filepath.Join(run.Path, eventsName+".next"), runEventsPath(run)); err != nil {
		t.Fatalf("replace events: %v", err)
	}
	status := readRunStatus(t, run)
	status.LastSequence = 1
	writeRunStatus(t, run, status)

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := len(loaded.Status.Artifacts); got != 0 {
		t.Fatalf("loaded artifact refs = %d, want 0 from event replay", got)
	}
}

func TestLoadRejectsUnsafeRunID(t *testing.T) {
	store := openStore(t, t.TempDir())
	_, err := store.Load("../escape")
	requireErrorContains(t, err, "path separators")
}
