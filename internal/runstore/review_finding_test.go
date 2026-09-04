package runstore

import (
	"encoding/json"
	"strings"
	"testing"
)

const reviewFindingReviewerStep = "review"

func TestReplayReviewFindingBlockRequiresRunningState(t *testing.T) {
	status := Status{State: stateBlockedHuman}
	event := reviewFindingEvent(t, eventRepeatedReviewFinding, reviewFindingBlockPayload{
		Block: validReviewFindingBlock(),
		State: stateBlockedHuman,
	})

	err := applyReplayedReviewFindingBlock(&status, event)
	if err == nil || !strings.Contains(err.Error(), "invalid for current run state") {
		t.Fatalf("error = %v, want current-state rejection", err)
	}
}

func TestReplayReviewFindingOverrideMustMatchActiveBlock(t *testing.T) {
	block := validReviewFindingBlock()
	status := Status{State: stateBlockedHuman, ReviewFindingBlock: &block}
	override := ReviewFindingOverride{ReviewFindingBlock: block, HumanAction: "allow_review_finding", TargetStep: block.ProposedCorrectionStepID}
	override.FindingID = "different-finding"
	event := reviewFindingEvent(t, eventReviewFindingOverride, reviewFindingOverridePayload{Override: override, State: stateRunning})

	err := applyReplayedReviewFindingOverride(&status, event)
	if err == nil || !strings.Contains(err.Error(), "does not match the active block") {
		t.Fatalf("error = %v, want active-block rejection", err)
	}
}

func TestReplayAttemptStartReviewFindingOverrideRequiresMatchingEntryAndAttempt(t *testing.T) {
	const otherStep = "other"

	block := validReviewFindingBlock()
	override := ReviewFindingOverride{ReviewFindingBlock: block, HumanAction: "allow_review_finding", TargetStep: block.ProposedCorrectionStepID}
	status := Status{RunID: "run-1", State: stateRunning, PendingReviewFindingOverride: &override}

	tests := []struct {
		name    string
		entry   *WorkflowStateEntry
		stepID  string
		wantErr string
	}{
		{name: "missing entry", stepID: override.TargetStep, wantErr: "without workflow state entry"},
		{name: "wrong entry", entry: &WorkflowStateEntry{State: otherStep}, stepID: override.TargetStep, wantErr: "does not match the pending target"},
		{name: "wrong attempt", entry: &WorkflowStateEntry{State: override.TargetStep}, stepID: otherStep, wantErr: "does not match the pending target"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := attemptStartedPayload{
				Attempt:            Attempt{RunID: status.RunID, AttemptID: "attempt-1", StepID: test.stepID},
				WorkflowStateEntry: test.entry, ConsumedReviewFindingOverride: &override,
			}

			event := Event{Sequence: 3, Type: eventAttemptStarted}
			if err := validateReplayedAttemptStart(status, event, payload, attemptStartRouting{}); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRepeatedReviewFindingEventsAreReserved(t *testing.T) {
	for _, eventType := range []string{eventRepeatedReviewFinding, eventReviewFindingOverride} {
		if !reservedEventType(eventType) {
			t.Fatalf("event %q is not reserved", eventType)
		}
	}
}

func validReviewFindingBlock() ReviewFindingBlock {
	return ReviewFindingBlock{
		Reason: RepeatedReviewFindingReason, FindingID: "same-finding", ReviewerStepID: reviewFindingReviewerStep,
		ProposedCorrectionStepID: "code_fixer", FirstReportAttemptID: "review-1",
		RepeatedReportAttemptID: "review-2", OccurrenceCount: 2,
	}
}

func reviewFindingEvent(t *testing.T, eventType string, payload any) Event {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	return Event{Sequence: 2, Type: eventType, Payload: data}
}
