package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

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

func TestLoadContextCancellationWhileLocalRunLockHeld(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "local-lock-cancel-load-run")

	err := runCanceledWhileLocalRunLockHeld(t, store, run.ID, "LoadContext", func(ctx context.Context) error {
		_, err := store.LoadContext(ctx, run.ID)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadContext error = %v, want context.Canceled", err)
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
				t.Helper()

				realOrc := filepath.Join(root, "real-orc")
				relocatePathBehindSymlink(t, filepath.Join(root, orcDirName), realOrc)
			},
		},
		{
			name: "runs",
			setup: func(t *testing.T, root string) {
				t.Helper()

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
			err = stableerr.Errorf("content = %q, want report", string(content))
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

	cmd := exec.CommandContext(context.Background(), os.Args[0])

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
