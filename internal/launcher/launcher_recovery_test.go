package launcher

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLaunchNextRefusesFreshPIDLessStartingAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	store := openLauncherStore(t, root)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "starting-attempt",
		Timeout:         200 * time.Millisecond,
		ReportExitGrace: 30 * time.Millisecond,
		Time:            time.Now().UTC(),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	_, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err == nil || !strings.Contains(err.Error(), "already has starting attempt") {
		t.Fatalf("LaunchNext error = %v, want starting attempt refusal", err)
	}

	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.State != runstore.AttemptStateStarting {
		t.Fatalf("active attempt = %+v, want still-starting attempt", loaded.Status.ActiveAttempt)
	}
}

func TestLaunchNextRecoversStalePIDLessStartingAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	seedLauncherAttempt(t, store, runID, "stale-starting-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())

	var stdout bytes.Buffer

	core, recorded := observer.New(zap.DebugLevel)

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Stdout:  &stdout,
		Logger:  zap.New(core),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Recovered || result.Attempt.ExitState != exitStateUnknown {
		t.Fatalf("result = %+v, want stale starting recovery", result)
	}

	if !strings.Contains(stdout.String(), "recovered attempt "+result.Attempt.AttemptID) {
		t.Fatalf("stdout = %q, want recovered attempt output", stdout.String())
	}

	if strings.Contains(stdout.String(), "recovered active attempt") {
		t.Fatalf("stdout = %q, want no diagnostic log message", stdout.String())
	}

	entry := singleObservedLog(t, recorded, "recovered active attempt")
	assertObservedFields(t, entry, map[string]string{
		"run_id":           runID,
		"step_id":          "plan",
		"agent_id":         "planner",
		"attempt_id":       result.Attempt.AttemptID,
		"recovered_state":  runstore.AttemptStateProcessError,
		"recovered_result": resultProcessError,
		"exit_state":       exitStateUnknown,
	})
}

func TestLaunchNextRecoveryPreservesExistingLogRef(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	started := seedLauncherAttempt(t, store, runID, "stale-logged-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())

	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan-stale-logged-attempt",
		Content: []byte("partial log\n"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: started.AttemptID,
		LogRef:    logRef,
		Time:      fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if result.Attempt.LogRef == nil || *result.Attempt.LogRef != logRef {
		t.Fatalf("recovered log ref = %+v, want %+v", result.Attempt.LogRef, logRef)
	}
}

func TestLaunchNextRefusesLiveActiveAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "active-attempt", 200*time.Millisecond, time.Now().UTC())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)

	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              os.Getpid(),
		ProcessStartTime: currentProcessStartTime(t),
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	_, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err == nil || !strings.Contains(err.Error(), "already has active attempt") {
		t.Fatalf("LaunchNext error = %v, want active attempt refusal", err)
	}
}

func TestLaunchNextRecoversWhenPIDIdentityDoesNotMatch(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "reused-pid-attempt", 200*time.Millisecond, fixedLauncherTime())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)

	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              os.Getpid(),
		ProcessStartTime: "1",
		Time:             fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Recovered || result.Attempt.ExitState != "unknown" {
		t.Fatalf("result = %+v, want recovered unknown for mismatched process identity", result)
	}
}

func TestLauncherStoreRejectsMissingPIDIdentity(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "missing-identity-attempt", 200*time.Millisecond, fixedLauncherTime())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)

	_, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID: attempt.AttemptID,
		PID:       os.Getpid(),
		Time:      fixedLauncherTime(),
	})
	if err == nil || !strings.Contains(err.Error(), "process_start_time is required") {
		t.Fatalf("RecordAttemptProcess error = %v, want missing process_start_time", err)
	}
}

func TestLaunchNextRecoversExpiredLiveAttemptAsTimeout(t *testing.T) {
	root, runID := createLauncherRun(t, "20ms")
	store := openLauncherStore(t, root)
	attempt := seedLauncherAttempt(t, store, runID, "expired-live-attempt", 20*time.Millisecond, time.Now().Add(-time.Second).UTC())
	linkLauncherPromptAndLog(t, store, runID, attempt.AttemptID)

	cmd := exec.CommandContext(context.Background(), "sh", "-c", "trap '' TERM; sleep 30")

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker process: %v", err)
	}

	t.Cleanup(func() {
		terminateProcessGroup(cmd.Process.Pid)
		_, _ = cmd.Process.Wait()
	})

	processStartTime, err := processStartIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("processStartIdentity returned error: %v", err)
	}

	active, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              cmd.Process.Pid,
		ProcessStartTime: processStartTime,
		Time:             fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Recovered || result.Attempt.State != runstore.AttemptStateTimedOut || result.Attempt.Result != resultTimeout {
		t.Fatalf("result = %+v, want recovered timeout", result)
	}

	if active.LogRef == nil || result.Attempt.LogRef == nil || *result.Attempt.LogRef != *active.LogRef {
		t.Fatalf("recovered log ref = %+v, want preserved %+v", result.Attempt.LogRef, active.LogRef)
	}

	_, _ = cmd.Process.Wait()

	eventually(t, time.Second, func() bool {
		_, err := processStartIdentity(cmd.Process.Pid)
		return err != nil
	})
}

func TestLaunchNextRecoversUnverifiableActiveAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	store := openLauncherStore(t, root)
	seedLauncherAttempt(t, store, runID, "orphaned-attempt", 200*time.Millisecond, fixedLauncherTime())

	result, err := LaunchNext(context.Background(), Options{Root: root, RunID: runID, Command: []string{"sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Recovered || result.Attempt.State != runstore.AttemptStateProcessError ||
		result.Attempt.Result != resultProcessError || result.Attempt.ExitState != exitStateUnknown {
		t.Fatalf("result = %+v, want recovered process_error unknown", result)
	}

	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want recovered terminal attempt", loaded.Status.ActiveAttempt)
	}
}

func singleObservedLog(t *testing.T, recorded *observer.ObservedLogs, message string) observer.LoggedEntry {
	t.Helper()

	entries := recorded.FilterMessage(message).All()
	if len(entries) != 1 {
		t.Fatalf("observed logs for %q = %d, want 1: %+v", message, len(entries), entries)
	}

	return entries[0]
}

func assertObservedFields(t *testing.T, entry observer.LoggedEntry, want map[string]string) {
	t.Helper()

	fields := entry.ContextMap()
	for key, value := range want {
		got, ok := fields[key]
		if !ok {
			t.Fatalf("observed log missing field %q in %+v", key, fields)
		}

		if got != value {
			t.Fatalf("observed log field %q = %#v, want %q", key, got, value)
		}
	}
}
