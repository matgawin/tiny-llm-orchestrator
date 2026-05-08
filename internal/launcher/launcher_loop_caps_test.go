package launcher

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestLaunchNextAppliesWorkflowLoopHardCapAfterResolvedHumanBlock(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")

	blockedAttempt, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("first LaunchNext returned error: %v", err)
	}
	assertLaunchNextBlocksWithoutRelaunch(t, root, runID, blockedAttempt.Attempt.AttemptID, 2)

	if _, _, err := store.ResolveHumanBlock(runID, "fixed worker input", fixedLauncherTime().Add(2*time.Second)); err != nil {
		t.Fatalf("ResolveHumanBlock returned error: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("retry LaunchNext error = %v, want workflow-loop hard cap", err)
	}
	if result.Launched || result.Attempt.AttemptID != "" {
		t.Fatalf("blocked retry result = %+v, want no launched attempt", result)
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.Counts[blockedAttempt.Attempt.StepID]; got != 2 {
		t.Fatalf("%s workflow-loop count = %d, want hard-capped current count 2", blockedAttempt.Attempt.StepID, got)
	}
	block := loaded.Status.WorkflowLoop.HardCapBlock
	if block == nil || block.BlockedState != blockedAttempt.Attempt.StepID || block.CurrentCount != 2 || block.ProspectiveCount != 3 ||
		block.PreviousState != blockedAttempt.Attempt.StepID || block.TriggerStatus != blockedAttempt.Attempt.Status || block.TriggerResult != blockedAttempt.Attempt.Result {
		t.Fatalf("hard-cap block = %+v, want resolved blocked attempt trigger", block)
	}
}

func TestLaunchNextWarnsAndContinuesAtWorkflowLoopSoftCap(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 2\nhard: 4\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	var stdout bytes.Buffer
	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched || result.SoftCap == nil {
		t.Fatalf("result = %+v, want launched with soft-cap warning", result)
	}
	if !strings.Contains(stdout.String(), "warning: workflow loop soft cap reached for workflow implementation state plan at count 3 (soft 2, hard 4)") {
		t.Fatalf("stdout = %q, want soft-cap warning", stdout.String())
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 3 {
		t.Fatalf("plan count = %d, want soft-cap entry count 3", got)
	}
	if got := loaded.Status.WorkflowLoop.SoftCapWarnings; len(got) != 1 || got[0].Count != 3 || got[0].TriggerStatus != launcherStatusDone || got[0].TriggerResult != launcherResultReady {
		t.Fatalf("soft cap warnings = %+v, want one threshold warning with trigger", got)
	}
}

func TestLaunchNextBlocksBeforeWorkflowLoopHardCapIncrement(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("LaunchNext error = %v, want hard-cap block reason", err)
	}
	if result.Attempt.AttemptID != "attempt-2" {
		t.Fatalf("result attempt = %+v, want latest triggering attempt", result.Attempt)
	}
	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.State != workflow.RunStatusBlockedForHuman {
		t.Fatalf("run state = %q, want blocked_for_human", loaded.Status.State)
	}
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != 2 {
		t.Fatalf("plan count = %d, want hard cap to leave count at 2", got)
	}
	if got := len(loaded.Status.Attempts); got != 2 {
		t.Fatalf("attempt count = %d, want no new attempt", got)
	}
	block := loaded.Status.WorkflowLoop.HardCapBlock
	if block == nil || block.BlockedState != "plan" || block.CurrentCount != 2 || block.ProspectiveCount != 3 || block.Reason != runstore.WorkflowLoopHardCapReason {
		t.Fatalf("hard cap block = %+v, want blocked plan prospective count 3", block)
	}
}

func TestLaunchNextConsumesWorkflowLoopHardCapOverride(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")
	if _, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	}); err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("initial LaunchNext error = %v, want hard-cap block", err)
	}
	status, _, err := store.AllowWorkflowLoopHardCap(runID, "allow_loop_cap", fixedLauncherTime().Add(4*time.Second))
	if err != nil {
		t.Fatalf("AllowWorkflowLoopHardCap returned error: %v", err)
	}
	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil {
		t.Fatal("pending override is nil")
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("LaunchNext with override returned error: %v", err)
	}
	if !result.Launched {
		t.Fatalf("result = %+v, want launched through one-shot override", result)
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.Counts["plan"]; got != override.CountAfterOverride {
		t.Fatalf("plan count = %d, want override count %d", got, override.CountAfterOverride)
	}
	if loaded.Status.WorkflowLoop.PendingHardCapOverride != nil {
		t.Fatalf("pending override = %+v, want consumed", loaded.Status.WorkflowLoop.PendingHardCapOverride)
	}
}

func TestLaunchNextBypassesDisabledWorkflowLoopCaps(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: false\nsoft: 1\nhard: 2\n", "")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatalf("Launched = false, want disabled caps to continue")
	}
	loaded := loadLauncherRun(t, root, runID)
	if got := loaded.Status.WorkflowLoop.HardCapBlock; got != nil {
		t.Fatalf("hard cap block = %+v, want none", got)
	}
}

func TestLaunchNextUsesWorkflowSpecificLoopCapOverride(t *testing.T) {
	root, runID := createLoopCapLauncherRun(t, "enabled: true\nsoft: 10\nhard: 20\n", "enabled: true\nsoft: 1\nhard: 2\n")
	store := openLauncherStore(t, root)
	seedReportedLoopAttempt(t, store, runID, "attempt-1", "")
	seedReportedLoopAttempt(t, store, runID, "attempt-2", "attempt-1")

	_, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null"},
		Time:    fixedLauncherTime().Add(3 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), runstore.WorkflowLoopHardCapReason) {
		t.Fatalf("LaunchNext error = %v, want workflow override hard cap", err)
	}
}
