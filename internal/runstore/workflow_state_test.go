//nolint:goconst // Test strings are clearer in place.
package runstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStartAttemptPersistsWorkflowStateEntry(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "loop-start", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	_, event, err := store.StartAttemptContext(context.Background(), run.ID, StartAttemptRequest{
		StepID:          "code",
		AgentID:         "coder",
		AttemptID:       "attempt-001",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		WorkflowStateEntry: WorkflowStateEntryRequest{
			State:         "code",
			PreviousState: "test",
			TriggerStatus: attemptStatusDone,
			TriggerResult: "ready",
		},
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.Counts["code"]; got != 1 {
		t.Fatalf("code count = %d, want 1", got)
	}

	if got := loaded.Status.WorkflowLoop.RepeatedStates; len(got) != 0 {
		t.Fatalf("repeated states = %+v, want none", got)
	}

	entry := loaded.Status.WorkflowLoop.Entries[1]
	if entry.State != "code" || entry.Count != 1 || entry.Repeated || entry.PreviousState != "test" || entry.TriggerStatus != attemptStatusDone || entry.TriggerResult != "ready" {
		t.Fatalf("entry = %+v, want first routed code entry with trigger", entry)
	}

	var payload attemptStartedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal attempt.started payload: %v", err)
	}

	if payload.WorkflowStateEntry == nil || *payload.WorkflowStateEntry != entry {
		t.Fatalf("payload entry = %+v, want %+v", payload.WorkflowStateEntry, entry)
	}
}

func TestStartAttemptWithoutWorkflowStateEntryDoesNotIncrementLoopCounters(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "retry-start", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if _, _, err := store.StartAttemptContext(context.Background(), run.ID, StartAttemptRequest{
		StepID:          "code",
		AgentID:         "coder",
		AttemptID:       "attempt-001",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.Counts["code"]; got != 0 {
		t.Fatalf("code count = %d, want unchanged initial count 0", got)
	}

	if got := len(loaded.Status.WorkflowLoop.Entries); got != 1 {
		t.Fatalf("entry count = %d, want unchanged initial entry", got)
	}
}

func TestUpdateStatusPersistsTerminalWorkflowStateEntry(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "terminal-loop", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	status, event, err := store.UpdateStatusContext(context.Background(), run.ID, StatusUpdate{
		State: "ready_for_human",
		WorkflowStateEntry: WorkflowStateEntryRequest{
			State:         "ready_for_human",
			PreviousState: "code",
			TriggerStatus: attemptStatusDone,
			TriggerResult: "ready",
		},
	})
	if err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	if got := status.WorkflowLoop.Counts["ready_for_human"]; got != 1 {
		t.Fatalf("terminal count = %d, want 1", got)
	}

	entry := status.WorkflowLoop.Entries[1]
	if entry.State != "ready_for_human" || entry.Count != 1 || entry.PreviousState != "code" || entry.TriggerStatus != attemptStatusDone || entry.TriggerResult != "ready" {
		t.Fatalf("entry = %+v, want terminal entry with trigger", entry)
	}

	var payload statusUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal status.updated payload: %v", err)
	}

	if payload.WorkflowStateEntry == nil || *payload.WorkflowStateEntry != entry {
		t.Fatalf("payload entry = %+v, want %+v", payload.WorkflowStateEntry, entry)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.Counts["ready_for_human"]; got != 1 {
		t.Fatalf("loaded terminal count = %d, want 1", got)
	}
}

func TestRecordStepSkipPersistsAuditHistoryAndTransition(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "skip-step", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	at := time.Date(2026, 5, 8, 10, 15, 0, 0, time.UTC)

	status, event, err := store.RecordStepSkipContext(context.Background(), run.ID, RecordStepSkipRequest{
		StepID: "code",
		Reason: "not needed after human review",
		Source: "test",
		Time:   at,
	}, func(locked Status) (StepSkipTransition, error) {
		if len(locked.Attempts) != 0 {
			t.Fatalf("locked attempts = %d, want 0", len(locked.Attempts))
		}

		return StepSkipTransition{
			State: stateRunning,
			WorkflowStateEntry: WorkflowStateEntryRequest{
				State:         "review",
				CounterKey:    "review-group",
				PreviousState: "code",
				TriggerStatus: attemptStatusDone,
				TriggerResult: "skipped",
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("RecordStepSkip returned error: %v", err)
	}

	if event.Type != eventWorkflowStepSkipped || event.Sequence != 2 {
		t.Fatalf("event = %+v, want workflow.step_skipped sequence 2", event)
	}

	if len(status.Attempts) != 0 || status.ActiveAttempt != nil {
		t.Fatalf("attempts = %d active=%+v, want unchanged empty attempts", len(status.Attempts), status.ActiveAttempt)
	}

	if got := status.WorkflowLoop.Counts["review-group"]; got != 1 {
		t.Fatalf("review-group loop count = %d, want 1", got)
	}

	entry := status.WorkflowLoop.Entries[1]
	if entry.State != "review" || entry.CounterKey != "review-group" || entry.PreviousState != "code" || entry.TriggerStatus != attemptStatusDone || entry.TriggerResult != "skipped" {
		t.Fatalf("workflow entry = %+v, want skip transition to review", entry)
	}

	if len(status.SkippedSteps) != 1 {
		t.Fatalf("skipped steps = %d, want 1", len(status.SkippedSteps))
	}

	skipped := status.SkippedSteps[0]
	if skipped.StepID != "code" || skipped.Status != attemptStatusDone || skipped.Result != "skipped" || skipped.Reason != "not needed after human review" || skipped.EventSequence != event.Sequence || !skipped.Time.Equal(at) || skipped.Source != "test" {
		t.Fatalf("skipped = %+v, want audited skip", skipped)
	}

	var payload workflowStepSkippedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal workflow.step_skipped payload: %v", err)
	}

	if payload.StepID != "code" || payload.Status != attemptStatusDone || payload.Result != "skipped" || payload.Reason != skipped.Reason || payload.WorkflowStateEntry == nil || *payload.WorkflowStateEntry != entry {
		t.Fatalf("payload = %+v, want skip payload with workflow entry %+v", payload, entry)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(loaded.Status.SkippedSteps) != 1 || loaded.Status.SkippedSteps[0] != skipped {
		t.Fatalf("replayed skipped steps = %+v, want %+v", loaded.Status.SkippedSteps, skipped)
	}

	if got := loaded.Status.WorkflowLoop.Entries[1]; got != entry {
		t.Fatalf("replayed workflow entry = %+v, want %+v", got, entry)
	}

	if got := loaded.Status.WorkflowLoop.Counts["review-group"]; got != 1 {
		t.Fatalf("replayed review-group loop count = %d, want 1", got)
	}
}

func TestRecordStepSkipRejectsBlankReasonWithoutMutation(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "blank-skip", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	before := run.Status

	_, _, err = store.RecordStepSkipContext(context.Background(), run.ID, RecordStepSkipRequest{StepID: "code", Reason: " \t "}, func(Status) (StepSkipTransition, error) {
		t.Fatal("validator called for blank reason")
		return StepSkipTransition{}, nil
	})
	requireErrorContains(t, err, "skip reason is required")

	after, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if after.Status.LastSequence != before.LastSequence || len(after.Status.SkippedSteps) != 0 {
		t.Fatalf("status after rejection = %+v, want unchanged sequence %d and no skips", after.Status, before.LastSequence)
	}
}

func TestRecordWorkflowLoopSoftCapDeduplicatesByCounterKey(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "soft-key", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	for _, state := range []string{"code", "code_fixer"} {
		_, _, err := store.RecordWorkflowLoopSoftCapContext(context.Background(), run.ID, WorkflowLoopSoftCap{
			Workflow: "implementation", State: state, CounterKey: "coding", Count: 3, Soft: 2, Hard: 3,
		}, time.Time{})
		if err != nil {
			t.Fatalf("RecordWorkflowLoopSoftCap returned error: %v", err)
		}
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.SoftCapWarnings; len(got) != 1 || got[0].CounterKey != "coding" || got[0].State != "code" {
		t.Fatalf("soft warnings = %+v, want one warning for coding", got)
	}
}

func TestRecordIgnoredReportDoesNotIncrementWorkflowLoopCounters(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "ignored-report-loop", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if _, err := store.RecordIgnoredReportContext(context.Background(), run.ID, IgnoreReportRequest{Reason: "validation_failed"}); err != nil {
		t.Fatalf("RecordIgnoredReport returned error: %v", err)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.Counts["code"]; got != 0 {
		t.Fatalf("code count = %d, want unchanged initial count 0", got)
	}

	if got := len(loaded.Status.WorkflowLoop.Entries); got != 1 {
		t.Fatalf("entry count = %d, want unchanged initial entry", got)
	}
}

func TestAllowWorkflowLoopHardCapPersistsPendingOverride(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "loop-override", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	block := WorkflowLoopHardCap{
		Workflow:         "implementation",
		BlockedState:     "code",
		CounterKey:       "coding",
		CurrentCount:     1,
		ProspectiveCount: 2,
		Soft:             1,
		Hard:             1,
		Reason:           WorkflowLoopHardCapReason,
	}
	if _, _, err := store.BlockWorkflowLoopHardCapContext(context.Background(), run.ID, block, time.Date(2026, 5, 2, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("BlockWorkflowLoopHardCap returned error: %v", err)
	}

	status, event, err := store.AllowWorkflowLoopHardCap(run.ID, "allow_loop_cap", time.Date(2026, 5, 2, 16, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("AllowWorkflowLoopHardCap returned error: %v", err)
	}

	if event.Type != eventWorkflowHardCapOverride {
		t.Fatalf("event type = %q, want %q", event.Type, eventWorkflowHardCapOverride)
	}

	if status.State != stateRunning || status.WorkflowLoop.HardCapBlock != nil {
		t.Fatalf("status = %+v, want running with cleared active block", status)
	}

	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil || override.Workflow != "implementation" || override.TargetState != "code" || override.CountBeforeOverride != 1 || override.CountAfterOverride != 2 || override.Soft != 1 || override.Hard != 1 || override.HumanAction != "allow_loop_cap" {
		t.Fatalf("pending override = %+v, want block-derived override", override)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Status.WorkflowLoop.PendingHardCapOverride == nil || *loaded.Status.WorkflowLoop.PendingHardCapOverride != *override {
		t.Fatalf("loaded override = %+v, want %+v", loaded.Status.WorkflowLoop.PendingHardCapOverride, override)
	}
}

func TestReplayWorkflowLoopHardCapOverrideRejectsDifferentEffectiveCounterKey(t *testing.T) {
	block := WorkflowLoopHardCap{Workflow: "implementation", BlockedState: "code", CounterKey: "coding", CurrentCount: 1, ProspectiveCount: 2, Soft: 1, Hard: 1, Reason: WorkflowLoopHardCapReason}
	override := WorkflowLoopHardCapOverride{Workflow: block.Workflow, TargetState: block.BlockedState, CounterKey: "different", CountBeforeOverride: block.CurrentCount, CountAfterOverride: block.ProspectiveCount, Soft: block.Soft, Hard: block.Hard, Reason: block.Reason}
	status := Status{Workflow: "implementation", State: stateBlockedHuman, WorkflowLoop: WorkflowLoop{HardCapBlock: &block}}

	err := validateReplayedWorkflowHardCapOverride(status, Event{Sequence: 3}, workflowLoopHardCapOverridePayload{Override: override, State: stateRunning})
	if err == nil || !strings.Contains(err.Error(), "does not match active hard-cap block") {
		t.Fatalf("validation error = %v, want counter-key mismatch rejection", err)
	}
}

func TestReplayAcceptsLegacyCountedInitialWorkflowState(t *testing.T) {
	status := Status{Workflow: "implementation"}
	entry := WorkflowStateEntry{Workflow: "implementation", State: "review", Count: 1}

	if err := applyReplayedWorkflowStateEntry(&status, Event{Sequence: 1}, &entry); err != nil {
		t.Fatalf("legacy initial entry replay error = %v", err)
	}

	if got := status.WorkflowLoop.Counts["review"]; got != 1 {
		t.Fatalf("legacy review count = %d, want 1", got)
	}
}

func TestAllowWorkflowLoopHardCapFailsWithoutActiveBlock(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "no-loop-override", Workflow: "implementation", InitialState: "code"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	before, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load before returned error: %v", err)
	}

	_, _, err = store.AllowWorkflowLoopHardCap(run.ID, "allow_loop_cap", time.Date(2026, 5, 2, 16, 1, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "no active workflow loop hard-cap block") {
		t.Fatalf("AllowWorkflowLoopHardCap error = %v, want no-active-block failure", err)
	}

	after, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load after returned error: %v", err)
	}

	if after.Status.LastSequence != before.Status.LastSequence || after.Status.State != before.Status.State || after.Status.WorkflowLoop.PendingHardCapOverride != nil {
		t.Fatalf("after status = %+v, want no mutation from %+v", after.Status, before.Status)
	}
}

func TestResolveHumanBlockPersistsContinuationAndReplays(t *testing.T) {
	store := openStore(t, t.TempDir())
	run := createManualRun(t, store, "resolve-human-block-run")
	startAttemptForTest(t, store, run.ID, "attempt-001")
	linkPromptAndLogForTest(t, store, run.ID, "attempt-001")
	recordProcessForTest(t, store, run.ID, "attempt-001")

	if _, _, err := store.RecordAttemptReportContext(context.Background(), run.ID, RecordReportRequest{
		State: AttemptStateReported,
		Report: Report{
			RunID:     run.ID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: "attempt-001",
			Status:    "blocked",
			Result:    "blocked",
			Summary:   "Need human config fix.",
		},
		Time: time.Date(2026, 5, 4, 12, 2, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}

	if _, _, err := store.UpdateStatusContext(context.Background(), run.ID, StatusUpdate{
		State: stateBlockedHuman,
		WorkflowStateEntry: WorkflowStateEntryRequest{
			State:         stateBlockedHuman,
			PreviousState: "plan",
			TriggerStatus: "blocked",
			TriggerResult: "blocked",
		},
	}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	status, event, err := store.ResolveHumanBlock(run.ID, "  fixed workflow config and reran checks  ", time.Date(2026, 5, 4, 12, 3, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ResolveHumanBlock returned error: %v", err)
	}

	if event.Type != eventRunContinued {
		t.Fatalf("event type = %q, want %q", event.Type, eventRunContinued)
	}

	if status.State != stateRunning || status.ActiveAttempt != nil {
		t.Fatalf("status = %+v, want running without active attempt", status)
	}

	if status.Continued == nil ||
		status.Continued.Mode != ContinueModeResolveBlock ||
		status.Continued.Reason != "fixed workflow config and reran checks" ||
		status.Continued.ResolvedAttemptID != "attempt-001" ||
		status.Continued.ResolvedStepID != "plan" ||
		status.Continued.ResolvedStatus != "blocked" ||
		status.Continued.ResolvedResult != "blocked" {
		t.Fatalf("continued marker = %+v, want resolved blocked attempt", status.Continued)
	}

	var payload runContinuedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal run.continued payload: %v", err)
	}

	if payload.Mode != ContinueModeResolveBlock || payload.PreviousState != stateBlockedHuman || payload.NewState != stateRunning || payload.Reason != "fixed workflow config and reran checks" {
		t.Fatalf("payload = %+v, want resolve_block transition with trimmed reason", payload)
	}

	if _, ok := LatestConsumableOutcome(status); ok {
		t.Fatal("LatestConsumableOutcome ok = true, want resolved blocked outcome ignored")
	}

	if step, ok := ResolvedHumanBlockStep(status); !ok || step != "plan" {
		t.Fatalf("ResolvedHumanBlockStep = %q/%v, want plan/true", step, ok)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Status.Continued == nil || *loaded.Status.Continued != *status.Continued {
		t.Fatalf("loaded continued = %+v, want %+v", loaded.Status.Continued, status.Continued)
	}

	if _, ok := LatestConsumableOutcome(loaded.Status); ok {
		t.Fatal("replayed LatestConsumableOutcome ok = true, want resolved blocked outcome ignored")
	}
}

func TestResolveHumanBlockRefusalsDoNotMutate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *Store, string)
		reason  string
		want    []string
	}{
		{
			name:   "empty reason",
			reason: " \t ",
			want:   []string{"--reason", "non-empty"},
		},
		{
			name: "active hard cap",
			prepare: func(t *testing.T, store *Store, runID string) {
				t.Helper()

				_, _, err := store.BlockWorkflowLoopHardCapContext(context.Background(), runID, WorkflowLoopHardCap{
					Workflow:         "implementation",
					BlockedState:     "code",
					CurrentCount:     1,
					ProspectiveCount: 2,
					Soft:             1,
					Hard:             1,
					Reason:           WorkflowLoopHardCapReason,
				}, time.Time{})
				if err != nil {
					t.Fatalf("BlockWorkflowLoopHardCap returned error: %v", err)
				}
			},
			reason: "reviewed",
			want:   []string{"workflow-loop hard cap", "--allow-loop-cap"},
		},
		{
			name: "active attempt",
			prepare: func(t *testing.T, store *Store, runID string) {
				t.Helper()
				startAttemptForTest(t, store, runID, "attempt-001")
			},
			reason: "reviewed",
			want:   []string{"active attempt", "wait, recover, or inspect"},
		},
		{
			name:   "running",
			reason: "reviewed",
			want:   []string{"state is \"running\"", "not in a resumable blocked state"},
		},
		{
			name: "blocked without attempt",
			prepare: func(t *testing.T, store *Store, runID string) {
				t.Helper()

				if _, _, err := store.UpdateStatusContext(context.Background(), runID, StatusUpdate{State: stateBlockedHuman}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			},
			reason: "reviewed",
			want:   []string{"no terminal blocked attempt", "inspect the run or start a new workflow"},
		},
		{
			name: "blocked without routing evidence",
			prepare: func(t *testing.T, store *Store, runID string) {
				t.Helper()
				startAttemptForTest(t, store, runID, "attempt-001")
				linkPromptAndLogForTest(t, store, runID, "attempt-001")
				recordProcessForTest(t, store, runID, "attempt-001")

				if _, _, err := store.RecordAttemptReportContext(context.Background(), runID, RecordReportRequest{
					State: AttemptStateReported,
					Report: Report{
						RunID:     runID,
						StepID:    "plan",
						AgentID:   "planner",
						AttemptID: "attempt-001",
						Status:    attemptStatusDone,
						Result:    "ready",
						Summary:   "Plan is ready.",
					},
				}); err != nil {
					t.Fatalf("RecordAttemptReport returned error: %v", err)
				}

				if _, _, err := store.UpdateStatusContext(context.Background(), runID, StatusUpdate{State: stateBlockedHuman}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			},
			reason: "reviewed",
			want:   []string{"no terminal blocked attempt", "inspect the run or start a new workflow"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())

			run := createManualRun(t, store, "resolve-refusal-"+strings.ReplaceAll(tc.name, " ", "-"))
			if tc.prepare != nil {
				tc.prepare(t, store, run.ID)
			}

			before, err := store.LoadContext(context.Background(), run.ID)
			if err != nil {
				t.Fatalf("Load before returned error: %v", err)
			}

			_, _, err = store.ResolveHumanBlock(run.ID, tc.reason, time.Time{})
			requireErrorContains(t, err, tc.want...)

			after, err := store.LoadContext(context.Background(), run.ID)
			if err != nil {
				t.Fatalf("Load after returned error: %v", err)
			}

			if after.Status.LastSequence != before.Status.LastSequence || after.Status.State != before.Status.State {
				t.Fatalf("after status = %+v, want no mutation from %+v", after.Status, before.Status)
			}
		})
	}
}

func TestStartAttemptConsumesWorkflowLoopHardCapOverrideOnce(t *testing.T) {
	store := openStore(t, t.TempDir())

	run, err := store.CreateContext(context.Background(), CreateRunRequest{RunID: "consume-loop-override", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	block := WorkflowLoopHardCap{
		Workflow:         "implementation",
		BlockedState:     "code",
		CounterKey:       "coding",
		CurrentCount:     0,
		ProspectiveCount: 1,
		Soft:             1,
		Hard:             1,
		Reason:           WorkflowLoopHardCapReason,
	}
	if _, _, err := store.BlockWorkflowLoopHardCapContext(context.Background(), run.ID, block, time.Date(2026, 5, 2, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("BlockWorkflowLoopHardCap returned error: %v", err)
	}

	status, _, err := store.AllowWorkflowLoopHardCap(run.ID, "allow_loop_cap", time.Date(2026, 5, 2, 16, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("AllowWorkflowLoopHardCap returned error: %v", err)
	}

	override := status.WorkflowLoop.PendingHardCapOverride
	if override == nil {
		t.Fatal("pending override is nil")
	}

	_, event, err := store.StartAttemptContext(context.Background(), run.ID, StartAttemptRequest{
		StepID:                             "code",
		AgentID:                            "coder",
		AttemptID:                          "attempt-override",
		Timeout:                            time.Minute,
		ReportExitGrace:                    time.Second,
		Time:                               time.Date(2026, 5, 2, 16, 2, 0, 0, time.UTC),
		WorkflowStateEntry:                 WorkflowStateEntryRequest{State: "code", CounterKey: "coding", PreviousState: "code", TriggerStatus: attemptStatusDone, TriggerResult: "ready"},
		ConsumeWorkflowLoopHardCapOverride: override,
	})
	if err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	var payload attemptStartedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal attempt.started payload: %v", err)
	}

	if payload.ConsumedWorkflowLoopHardCapOverride == nil || *payload.ConsumedWorkflowLoopHardCapOverride != *override {
		t.Fatalf("consumed override payload = %+v, want %+v", payload.ConsumedWorkflowLoopHardCapOverride, override)
	}

	loaded, err := store.LoadContext(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Status.WorkflowLoop.Counts["coding"]; got != 1 {
		t.Fatalf("coding count = %d, want overridden prospective count 1", got)
	}

	if loaded.Status.WorkflowLoop.PendingHardCapOverride != nil {
		t.Fatalf("pending override = %+v, want consumed", loaded.Status.WorkflowLoop.PendingHardCapOverride)
	}
}
