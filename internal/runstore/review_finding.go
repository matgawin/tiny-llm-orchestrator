package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// BlockRepeatedReviewFindingContext records a repeated-finding human stop.
func (s *Store) BlockRepeatedReviewFindingContext(ctx context.Context, runID string, block ReviewFindingBlock, at time.Time) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, errors.New("context is required")
	}

	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}

	if err := validateReviewFindingBlock(block); err != nil {
		return Status{}, Event{}, err
	}

	at = normalizeTime(at)

	var (
		status Status
		event  Event
	)

	err := s.withRunLockContext(ctx, runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}

		if run.Status.State != stateRunning || run.Status.ActiveAttempt != nil {
			return fmt.Errorf("run %q is not available for a repeated review finding stop", runID)
		}

		payload, err := marshalPayload(reviewFindingBlockPayload{Block: block, State: stateBlockedHuman})
		if err != nil {
			return err
		}

		event = Event{Time: at, Type: eventRepeatedReviewFinding, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			status.ReviewFindingBlock = &block
			status.State = stateBlockedHuman
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})

		return err
	})

	return status, event, err
}

// AllowRepeatedReviewFinding records a one-use human continuation.
func (s *Store) AllowRepeatedReviewFinding(runID string, at time.Time) (Status, Event, error) {
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}

	at = normalizeTime(at)

	var (
		status Status
		event  Event
	)

	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}

		if run.Status.State != stateBlockedHuman || run.Status.ReviewFindingBlock == nil {
			return fmt.Errorf("run %q has no active repeated review finding block", runID)
		}

		if run.Status.PendingReviewFindingOverride != nil {
			return fmt.Errorf("run %q already has a pending repeated review finding override", runID)
		}

		override := ReviewFindingOverride{ReviewFindingBlock: *run.Status.ReviewFindingBlock, HumanAction: "allow_review_finding", TargetStep: run.Status.ReviewFindingBlock.ProposedCorrectionStepID}

		payload, err := marshalPayload(reviewFindingOverridePayload{Override: override, State: stateRunning})
		if err != nil {
			return err
		}

		event = Event{Time: at, Type: eventReviewFindingOverride, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			status.PendingReviewFindingOverride = &override
			status.ReviewFindingBlock = nil
			status.State = stateRunning
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})

		return err
	})

	return status, event, err
}

func validateReviewFindingBlock(block ReviewFindingBlock) error {
	if block.Reason != RepeatedReviewFindingReason || block.FindingID == "" || block.ReviewerStepID == "" || block.ProposedCorrectionStepID == "" || block.FirstReportAttemptID == "" || block.RepeatedReportAttemptID == "" || block.OccurrenceCount < 2 {
		return errors.New("invalid repeated review finding block")
	}

	return nil
}

func applyReplayedReviewFindingBlock(status *Status, event Event) error {
	var payload reviewFindingBlockPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d repeated review finding payload: %w", event.Sequence, err)
	}

	if err := validateReviewFindingBlock(payload.Block); err != nil {
		return fmt.Errorf("event %d: %w", event.Sequence, err)
	}

	if payload.State != stateBlockedHuman {
		return fmt.Errorf("event %d repeated review finding state is %q", event.Sequence, payload.State)
	}

	if status.State != stateRunning || status.ActiveAttempt != nil || status.ReviewFindingBlock != nil || status.PendingReviewFindingOverride != nil {
		return fmt.Errorf("event %d repeated review finding stop is invalid for current run state", event.Sequence)
	}

	status.ReviewFindingBlock = &payload.Block
	status.State = payload.State

	return nil
}

func applyReplayedReviewFindingOverride(status *Status, event Event) error {
	var payload reviewFindingOverridePayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d repeated review finding override payload: %w", event.Sequence, err)
	}

	if err := validateReviewFindingBlock(payload.Override.ReviewFindingBlock); err != nil {
		return fmt.Errorf("event %d: %w", event.Sequence, err)
	}

	if payload.Override.HumanAction != "allow_review_finding" || payload.Override.TargetStep != payload.Override.ProposedCorrectionStepID || payload.State != stateRunning {
		return fmt.Errorf("event %d invalid repeated review finding override", event.Sequence)
	}

	if status.State != stateBlockedHuman || status.ActiveAttempt != nil || status.ReviewFindingBlock == nil || *status.ReviewFindingBlock != payload.Override.ReviewFindingBlock || status.PendingReviewFindingOverride != nil {
		return fmt.Errorf("event %d repeated review finding override does not match the active block", event.Sequence)
	}

	status.PendingReviewFindingOverride = &payload.Override
	status.ReviewFindingBlock = nil
	status.State = payload.State

	return nil
}
