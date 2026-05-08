package launcher

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestLaunchNextPersistsPromptLogAndMissingReportAttempt(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")

	var stdout bytes.Buffer
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Stdout:  &stdout,
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}
	if result.Attempt.State != runstore.AttemptStateMissingReport ||
		result.Attempt.Status != reportStatusFailed ||
		result.Attempt.Result != "missing_report" {
		t.Fatalf("attempt = %+v, want failed/missing_report", result.Attempt)
	}
	if result.Attempt.PromptRef == nil || result.Attempt.LogRef == nil {
		t.Fatalf("attempt refs = prompt %+v log %+v, want both recorded", result.Attempt.PromptRef, result.Attempt.LogRef)
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	assertContainsAll(t, string(logContent), []string{
		"# Tiny Orc Worker Prompt\n",
		"- run_id: `launcher-run`\n",
		"- step_id: `plan`\n",
		"- agent_id: `planner`\n",
		"- attempt_id: `" + result.Attempt.AttemptID + "`\n",
	})
	if !strings.Contains(stdout.String(), "launched attempt "+result.Attempt.AttemptID) {
		t.Fatalf("stdout = %q, want launched attempt", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want terminalized", loaded.Status.ActiveAttempt)
	}
	if got := len(loaded.Status.Attempts); got != 1 {
		t.Fatalf("attempt history len = %d, want 1", got)
	}
}

func TestLaunchNextStreamsLogBeforeWorkerExits(t *testing.T) {
	root, runID := createLauncherRun(t, "500ms")
	done := make(chan error, 1)
	go func() {
		_, err := LaunchNext(context.Background(), Options{
			Root:    root,
			RunID:   runID,
			Command: []string{"sh", "-c", "printf 'before-exit\\n'; sleep 0.2"},
			Time:    fixedLauncherTime(),
		})
		done <- err
	}()

	eventually(t, time.Second, func() bool {
		return strings.Contains(allLauncherLogs(t, root, runID), "before-exit")
	})
	loadedWhileRunning := loadLauncherRun(t, root, runID)
	if loadedWhileRunning.Status.ActiveAttempt == nil || loadedWhileRunning.Status.ActiveAttempt.LogRef == nil {
		t.Fatalf("active attempt = %+v, want log ref before worker exits", loadedWhileRunning.Status.ActiveAttempt)
	}
	if err := <-done; err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
}
