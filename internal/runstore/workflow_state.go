package runstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// RecordWorkflowLoopSoftCap records the first advisory soft-cap hit for a workflow state.
func (s *Store) RecordWorkflowLoopSoftCap(runID string, loopCap WorkflowLoopSoftCap, at time.Time) (Status, Event, error) {
	return s.RecordWorkflowLoopSoftCapContext(context.Background(), runID, loopCap, at)
}

// RecordWorkflowLoopSoftCapContext records the first advisory soft-cap hit unless ctx is canceled before commit.
func (s *Store) RecordWorkflowLoopSoftCapContext(ctx context.Context, runID string, loopCap WorkflowLoopSoftCap, at time.Time) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if err := validateWorkflowLoopSoftCap(loopCap); err != nil {
		return Status{}, Event{}, err
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("record workflow loop soft cap context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if workflowSoftCapRecorded(run.Status.WorkflowLoop.SoftCapWarnings, loopCap.Workflow, loopCap.State) {
			status = run.Status
			return nil
		}
		payload, err := marshalPayload(workflowLoopSoftCapPayload{Cap: loopCap})
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventWorkflowSoftCap, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			applyWorkflowLoopSoftCap(status, loopCap)
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

// BlockWorkflowLoopHardCap records a hard-cap stop and sends the run to human decision.
func (s *Store) BlockWorkflowLoopHardCap(runID string, loopCap WorkflowLoopHardCap, at time.Time) (Status, Event, error) {
	return s.BlockWorkflowLoopHardCapContext(context.Background(), runID, loopCap, at)
}

// BlockWorkflowLoopHardCapContext records a hard-cap stop unless ctx is canceled before commit.
func (s *Store) BlockWorkflowLoopHardCapContext(ctx context.Context, runID string, loopCap WorkflowLoopHardCap, at time.Time) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if err := validateWorkflowLoopHardCap(loopCap); err != nil {
		return Status{}, Event{}, err
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("block workflow loop hard cap context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt != nil {
			return stableerr.Errorf("run %q has active attempt %q; loop hard-cap block is not allowed", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if run.Status.State != stateRunning {
			return stableerr.Errorf("run %q state is %q, want %q for loop hard-cap block", runID, run.Status.State, stateRunning)
		}
		payload, err := marshalPayload(workflowLoopHardCapPayload{Cap: loopCap, State: stateBlockedHuman})
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventWorkflowHardCap, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			applyWorkflowLoopHardCap(status, loopCap)
			status.State = stateBlockedHuman
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

// AllowWorkflowLoopHardCap records a human-reviewed one-shot override for the
// currently active workflow loop hard-cap block and returns the run to running.
func (s *Store) AllowWorkflowLoopHardCap(runID, humanAction string, at time.Time) (Status, Event, error) {
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	overrideAction := strings.TrimSpace(humanAction)
	if overrideAction == "" {
		return Status{}, Event{}, stableerr.New("workflow loop hard-cap override human action is required")
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.State != stateBlockedHuman {
			return stableerr.Errorf("run %q has no active workflow loop hard-cap block; state is %q", runID, run.Status.State)
		}
		block := run.Status.WorkflowLoop.HardCapBlock
		if block == nil {
			return stableerr.Errorf("run %q has no active workflow loop hard-cap block", runID)
		}
		if run.Status.WorkflowLoop.PendingHardCapOverride != nil {
			return stableerr.Errorf("run %q already has a pending workflow loop hard-cap override", runID)
		}
		override := WorkflowLoopHardCapOverride{
			Workflow:            block.Workflow,
			TargetState:         block.BlockedState,
			CountBeforeOverride: block.CurrentCount,
			CountAfterOverride:  block.ProspectiveCount,
			Soft:                block.Soft,
			Hard:                block.Hard,
			HumanAction:         overrideAction,
			Reason:              block.Reason,
		}
		if err := validateWorkflowLoopHardCapOverride(override); err != nil {
			return err
		}
		payload, err := marshalPayload(workflowLoopHardCapOverridePayload{Override: override, State: stateRunning})
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventWorkflowHardCapOverride, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			applyWorkflowLoopHardCapOverride(status, override)
			status.State = stateRunning
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

// RecordStepSkip persists an audited system-owned done/skipped transition.
func (s *Store) RecordStepSkip(runID string, req RecordStepSkipRequest, validate StepSkipValidator) (Status, Event, error) {
	return s.RecordStepSkipContext(context.Background(), runID, req, validate)
}

// RecordStepSkipContext persists an audited system-owned done/skipped transition unless ctx is canceled before commit.
func (s *Store) RecordStepSkipContext(ctx context.Context, runID string, req RecordStepSkipRequest, validate StepSkipValidator) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if validate == nil {
		return Status{}, Event{}, stableerr.New("step skip validator is required")
	}
	req.StepID = strings.TrimSpace(req.StepID)
	if req.StepID == "" {
		return Status{}, Event{}, stableerr.New("step id is required")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return Status{}, Event{}, stableerr.New("skip reason is required")
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Time = normalizeTime(req.Time)
	var status Status
	var event Event
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("record step skip context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		transition, err := validate(run.Status)
		if err != nil {
			return err
		}
		if transition.State == "" {
			return stableerr.New("step skip transition state is required")
		}
		consumeAttemptID := ""
		if attempt, ok := LatestConsumableOutcome(run.Status); ok {
			consumeAttemptID = attempt.AttemptID
		}
		var workflowEntry *WorkflowStateEntry
		if transition.WorkflowStateEntry.State != "" {
			entry, err := nextWorkflowStateEntry(run.Status, transition.WorkflowStateEntry)
			if err != nil {
				return err
			}
			workflowEntry = &entry
		}
		payload, err := marshalPayload(workflowStepSkippedPayload{
			StepID:             req.StepID,
			Status:             attemptStatusDone,
			Result:             "skipped",
			Reason:             req.Reason,
			Source:             req.Source,
			ConsumeAttemptID:   consumeAttemptID,
			State:              transition.State,
			WorkflowStateEntry: workflowEntry,
		})
		if err != nil {
			return err
		}
		event = Event{Time: req.Time, Type: eventWorkflowStepSkipped, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			if workflowEntry != nil {
				applyWorkflowStateEntry(status, *workflowEntry)
			}
			applyAttemptOutcomeConsumption(status, event, consumeAttemptID)
			applyStepSkipped(status, SkippedStep{
				StepID:        req.StepID,
				Status:        attemptStatusDone,
				Result:        "skipped",
				Reason:        req.Reason,
				EventSequence: event.Sequence,
				Time:          event.Time,
				Source:        req.Source,
			})
			status.State = transition.State
			status.RetryLineage = nil
			status.Continued = nil
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

// ResolveHumanBlock records human attestation that an external blocker was
// resolved and returns a non-loop blocked_for_human run to running.
func (s *Store) ResolveHumanBlock(runID, reason string, at time.Time) (Status, Event, error) {
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Status{}, Event{}, stableerr.New("--reason is required for --resolve-block and must be non-empty after trimming")
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.WorkflowLoop.HardCapBlock != nil {
			return stableerr.Errorf("run %q is blocked by a workflow-loop hard cap; next action is orc run continue %s --allow-loop-cap after human review", runID, runID)
		}
		if run.Status.ActiveAttempt != nil {
			return stableerr.Errorf("run %q has active attempt %q; wait, recover, or inspect before continuing", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if run.Status.State != stateBlockedHuman {
			return stableerr.Errorf("run %q state is %q; run is not in a resumable blocked state; inspect the run or start a new workflow as appropriate", runID, run.Status.State)
		}
		attempt, ok := latestResolvableBlockedAttempt(run.Status)
		if !ok {
			return stableerr.Errorf("run %q has no terminal blocked attempt that can be resolved; inspect the run or start a new workflow", runID)
		}
		payload, err := marshalPayload(runContinuedPayload{
			Mode:              ContinueModeResolveBlock,
			PreviousState:     stateBlockedHuman,
			NewState:          stateRunning,
			Reason:            reason,
			ResolvedAttemptID: attempt.AttemptID,
			ResolvedStepID:    attempt.StepID,
			ResolvedStatus:    attempt.Status,
			ResolvedResult:    attempt.Result,
		})
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventRunContinued, Payload: payload}
		status, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			applyRunContinued(status, runContinuedPayload{
				Mode:              ContinueModeResolveBlock,
				PreviousState:     stateBlockedHuman,
				NewState:          stateRunning,
				Reason:            reason,
				ResolvedAttemptID: attempt.AttemptID,
				ResolvedStepID:    attempt.StepID,
				ResolvedStatus:    attempt.Status,
				ResolvedResult:    attempt.Result,
			})
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

func nextWorkflowStateEntry(status Status, req WorkflowStateEntryRequest) (WorkflowStateEntry, error) {
	if req.State == "" {
		return WorkflowStateEntry{}, stableerr.New("workflow state entry state is required")
	}
	count := status.WorkflowLoop.Counts[req.State] + 1
	return WorkflowStateEntry{
		Workflow:      status.Workflow,
		State:         req.State,
		Count:         count,
		Repeated:      count > 1,
		PreviousState: req.PreviousState,
		TriggerStatus: req.TriggerStatus,
		TriggerResult: req.TriggerResult,
	}, nil
}

func applyWorkflowStateEntry(status *Status, entry WorkflowStateEntry) {
	if status.WorkflowLoop.Counts == nil {
		status.WorkflowLoop.Counts = map[string]int{}
	}
	status.WorkflowLoop.Counts[entry.State] = entry.Count
	status.WorkflowLoop.Entries = append(status.WorkflowLoop.Entries, entry)
	if entry.Repeated && !slices.Contains(status.WorkflowLoop.RepeatedStates, entry.State) {
		status.WorkflowLoop.RepeatedStates = append(status.WorkflowLoop.RepeatedStates, entry.State)
	}
}

func applyWorkflowLoopSoftCap(status *Status, loopCap WorkflowLoopSoftCap) {
	if workflowSoftCapRecorded(status.WorkflowLoop.SoftCapWarnings, loopCap.Workflow, loopCap.State) {
		return
	}
	status.WorkflowLoop.SoftCapWarnings = append(status.WorkflowLoop.SoftCapWarnings, loopCap)
}

func workflowSoftCapRecorded(caps []WorkflowLoopSoftCap, workflow, state string) bool {
	return slices.ContainsFunc(caps, func(existing WorkflowLoopSoftCap) bool {
		return existing.Workflow == workflow && existing.State == state
	})
}

func applyWorkflowLoopHardCap(status *Status, loopCap WorkflowLoopHardCap) {
	status.WorkflowLoop.HardCapBlock = &loopCap
}

func applyWorkflowLoopHardCapOverride(status *Status, override WorkflowLoopHardCapOverride) {
	status.WorkflowLoop.PendingHardCapOverride = &override
	status.WorkflowLoop.HardCapBlock = nil
}

func applyStepSkipped(status *Status, skipped SkippedStep) {
	status.SkippedSteps = append(status.SkippedSteps, skipped)
}

func applyRunContinued(status *Status, payload runContinuedPayload) {
	status.State = payload.NewState
	status.Continued = &RunContinuation{
		Mode:              payload.Mode,
		Reason:            payload.Reason,
		ResolvedAttemptID: payload.ResolvedAttemptID,
		ResolvedStepID:    payload.ResolvedStepID,
		ResolvedStatus:    payload.ResolvedStatus,
		ResolvedResult:    payload.ResolvedResult,
	}
}

func validateRunContinuedPayload(status Status, payload runContinuedPayload) error {
	switch {
	case payload.Mode != ContinueModeResolveBlock:
		return stableerr.Errorf("run continued mode = %q, want %q", payload.Mode, ContinueModeResolveBlock)
	case payload.PreviousState != stateBlockedHuman:
		return stableerr.Errorf("run continued previous_state = %q, want %q", payload.PreviousState, stateBlockedHuman)
	case payload.NewState != stateRunning:
		return stableerr.Errorf("run continued new_state = %q, want %q", payload.NewState, stateRunning)
	case strings.TrimSpace(payload.Reason) == "":
		return stableerr.New("run continued reason is required")
	case payload.Reason != strings.TrimSpace(payload.Reason):
		return stableerr.New("run continued reason must be trimmed")
	case status.State != stateBlockedHuman:
		return stableerr.Errorf("run continued requires state %q, got %q", stateBlockedHuman, status.State)
	case status.ActiveAttempt != nil:
		return stableerr.Errorf("run continued while attempt %q is active", status.ActiveAttempt.AttemptID)
	case status.WorkflowLoop.HardCapBlock != nil:
		return stableerr.New("run continued resolve_block is not valid for active workflow-loop hard-cap block")
	}
	attempt, ok := latestResolvableBlockedAttempt(status)
	if !ok {
		return stableerr.New("run continued resolve_block requires latest terminal blocked attempt")
	}
	if payload.ResolvedAttemptID != attempt.AttemptID ||
		payload.ResolvedStepID != attempt.StepID ||
		payload.ResolvedStatus != attempt.Status ||
		payload.ResolvedResult != attempt.Result {
		return stableerr.New("run continued resolved attempt fields do not match latest terminal blocked attempt")
	}
	return nil
}

func validateWorkflowStepSkippedPayload(status Status, event Event, payload workflowStepSkippedPayload) (SkippedStep, error) {
	reason := strings.TrimSpace(payload.Reason)
	switch {
	case payload.StepID == "":
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped step_id is required", event.Sequence)
	case payload.Status != attemptStatusDone:
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped status = %q, want %s", event.Sequence, payload.Status, attemptStatusDone)
	case payload.Result != "skipped":
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped result = %q, want skipped", event.Sequence, payload.Result)
	case reason == "":
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped reason is required", event.Sequence)
	case payload.Reason != reason:
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped reason must be trimmed", event.Sequence)
	case payload.State == "":
		return SkippedStep{}, stableerr.Errorf("event %d workflow.step_skipped state is required", event.Sequence)
	case status.ActiveAttempt != nil:
		return SkippedStep{}, stableerr.Errorf("event %d skips step while attempt %q is active", event.Sequence, status.ActiveAttempt.AttemptID)
	case status.State != stateRunning:
		return SkippedStep{}, stableerr.Errorf("event %d skips step while run state is %q, want %q", event.Sequence, status.State, stateRunning)
	}
	if err := validateAttemptOutcomeConsumption(status, payload.ConsumeAttemptID); err != nil {
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped consume_attempt_id: %w", event.Sequence, err)
	}
	return SkippedStep{
		StepID:        payload.StepID,
		Status:        payload.Status,
		Result:        payload.Result,
		Reason:        payload.Reason,
		EventSequence: event.Sequence,
		Time:          event.Time,
		Source:        payload.Source,
	}, nil
}

func validateWorkflowLoopSoftCap(loopCap WorkflowLoopSoftCap) error {
	switch {
	case loopCap.Workflow == "":
		return stableerr.New("workflow loop soft cap workflow is required")
	case loopCap.State == "":
		return stableerr.New("workflow loop soft cap state is required")
	case loopCap.Count <= 0:
		return stableerr.Errorf("workflow loop soft cap count must be > 0, got %d", loopCap.Count)
	case loopCap.Soft <= 0:
		return stableerr.Errorf("workflow loop soft cap soft must be > 0, got %d", loopCap.Soft)
	case loopCap.Hard <= 0:
		return stableerr.Errorf("workflow loop soft cap hard must be > 0, got %d", loopCap.Hard)
	}
	return nil
}

func validateWorkflowLoopHardCap(loopCap WorkflowLoopHardCap) error {
	switch {
	case loopCap.Workflow == "":
		return stableerr.New("workflow loop hard cap workflow is required")
	case loopCap.BlockedState == "":
		return stableerr.New("workflow loop hard cap blocked target state is required")
	case loopCap.CurrentCount < 0:
		return stableerr.Errorf("workflow loop hard cap current count must be >= 0, got %d", loopCap.CurrentCount)
	case loopCap.ProspectiveCount <= loopCap.CurrentCount:
		return stableerr.Errorf("workflow loop hard cap prospective count must be greater than current count, got prospective=%d current=%d", loopCap.ProspectiveCount, loopCap.CurrentCount)
	case loopCap.Soft <= 0:
		return stableerr.Errorf("workflow loop hard cap soft must be > 0, got %d", loopCap.Soft)
	case loopCap.Hard <= 0:
		return stableerr.Errorf("workflow loop hard cap hard must be > 0, got %d", loopCap.Hard)
	case loopCap.Reason != WorkflowLoopHardCapReason:
		return stableerr.Errorf("workflow loop hard cap reason = %q, want %q", loopCap.Reason, WorkflowLoopHardCapReason)
	}
	return nil
}

func validateWorkflowLoopHardCapOverride(override WorkflowLoopHardCapOverride) error {
	switch {
	case override.Workflow == "":
		return stableerr.New("workflow loop hard-cap override workflow is required")
	case override.TargetState == "":
		return stableerr.New("workflow loop hard-cap override target state is required")
	case override.CountBeforeOverride < 0:
		return stableerr.Errorf("workflow loop hard-cap override count before must be >= 0, got %d", override.CountBeforeOverride)
	case override.CountAfterOverride <= override.CountBeforeOverride:
		return stableerr.Errorf("workflow loop hard-cap override count after must be greater than count before, got after=%d before=%d", override.CountAfterOverride, override.CountBeforeOverride)
	case override.Soft <= 0:
		return stableerr.Errorf("workflow loop hard-cap override soft must be > 0, got %d", override.Soft)
	case override.Hard <= 0:
		return stableerr.Errorf("workflow loop hard-cap override hard must be > 0, got %d", override.Hard)
	case strings.TrimSpace(override.HumanAction) == "":
		return stableerr.New("workflow loop hard-cap override human action is required")
	case override.Reason != WorkflowLoopHardCapReason:
		return stableerr.Errorf("workflow loop hard-cap override reason = %q, want %q", override.Reason, WorkflowLoopHardCapReason)
	}
	return nil
}

func validateWorkflowLoopHardCapOverrideConsumption(status Status, entry WorkflowStateEntry, override WorkflowLoopHardCapOverride) error {
	if err := validateWorkflowLoopHardCapOverride(override); err != nil {
		return err
	}
	pending := status.WorkflowLoop.PendingHardCapOverride
	switch {
	case pending == nil:
		return stableerr.New("workflow loop hard-cap override consumption requires pending override")
	case *pending != override:
		return stableerr.New("workflow loop hard-cap override consumption does not match pending override")
	case entry.Workflow != override.Workflow:
		return stableerr.Errorf("workflow loop hard-cap override workflow = %q, want %q", override.Workflow, entry.Workflow)
	case entry.State != override.TargetState:
		return stableerr.Errorf("workflow loop hard-cap override target state = %q, want %q", override.TargetState, entry.State)
	case entry.Count != override.CountAfterOverride:
		return stableerr.Errorf("workflow loop hard-cap override count after = %d, want workflow entry count %d", override.CountAfterOverride, entry.Count)
	case status.WorkflowLoop.Counts[entry.State] != override.CountBeforeOverride:
		return stableerr.Errorf("workflow loop hard-cap override count before = %d, want current count %d", override.CountBeforeOverride, status.WorkflowLoop.Counts[entry.State])
	}
	return nil
}

func applyReplayedWorkflowStateEntry(status *Status, event Event, entry *WorkflowStateEntry) error {
	if entry == nil {
		return nil
	}
	switch {
	case entry.Workflow != status.Workflow:
		return stableerr.Errorf("event %d workflow state entry workflow %q does not match status workflow %q", event.Sequence, entry.Workflow, status.Workflow)
	case entry.State == "":
		return stableerr.Errorf("event %d workflow state entry state is required", event.Sequence)
	case entry.Count != status.WorkflowLoop.Counts[entry.State]+1:
		return stableerr.Errorf("event %d workflow state entry %q count = %d, want %d", event.Sequence, entry.State, entry.Count, status.WorkflowLoop.Counts[entry.State]+1)
	case entry.Repeated != (entry.Count > 1):
		return stableerr.Errorf("event %d workflow state entry %q repeated = %t, want %t", event.Sequence, entry.State, entry.Repeated, entry.Count > 1)
	}
	applyWorkflowStateEntry(status, *entry)
	return nil
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
