package runstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

func readEvents(path, runID string) ([]Event, error) {
	if err := validateRegularFile(path, eventsName); err != nil {
		return nil, err
	}

	file, err := os.Open(path) // #nosec G304,G703 -- path is scoped to the run directory.
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}

	defer func() {
		_ = file.Close()
	}()

	var events []Event

	reader := bufio.NewReader(file)
	line := 0

	for {
		content, err := reader.ReadBytes('\n')
		if len(content) == 0 && errors.Is(err, io.EOF) {
			break
		}

		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read events: %w", err)
		}

		if errors.Is(err, io.EOF) {
			return nil, stableerr.Errorf("line %d: missing trailing newline", line+1)
		}

		line++

		var event Event
		if err := json.Unmarshal(content, &event); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		if len(event.Payload) == 0 {
			return nil, stableerr.Errorf("line %d: payload is required", line)
		}

		if event.SchemaVersion != schemaVersion {
			return nil, stableerr.Errorf("line %d: unsupported schema_version %d", line, event.SchemaVersion)
		}

		if event.RunID != runID {
			return nil, stableerr.Errorf("line %d: run_id %q does not match", line, event.RunID)
		}

		if event.Sequence != line {
			return nil, stableerr.Errorf("line %d: sequence %d is not ordered", line, event.Sequence)
		}

		if event.Type == "" {
			return nil, stableerr.Errorf("line %d: type is required", line)
		}

		if event.Time.IsZero() {
			return nil, stableerr.Errorf("line %d: time is required", line)
		}

		events = append(events, event)
	}

	return events, nil
}

func replayStatus(events []Event, status Status) (Status, error) {
	createdPayload, err := validateRunCreated(events[0], status)
	if err != nil {
		return Status{}, err
	}

	replayed := Status{
		SchemaVersion: schemaVersion,
		RunID:         status.RunID,
		Workflow:      status.Workflow,
		State:         stateRunning,
		CreatedAt:     events[0].Time,
		UpdatedAt:     events[0].Time,
		LastSequence:  events[len(events)-1].Sequence,
		Artifacts:     []ArtifactRef{},
		Attempts:      []Attempt{},
		Warnings:      []AttemptWarning{},
	}
	if err := applyReplayedWorkflowStateEntry(&replayed, events[0], createdPayload.WorkflowStateEntry); err != nil {
		return Status{}, err
	}

	for _, event := range events[1:] {
		replayed.UpdatedAt = event.Time
		if err := applyReplayedEvent(&replayed, event); err != nil {
			return Status{}, err
		}
	}

	return replayed, nil
}

func applyReplayedEvent(status *Status, event Event) error {
	switch event.Type {
	case eventRunCreated:
		return stableerr.Errorf("event %d duplicate %s event", event.Sequence, eventRunCreated)
	case eventStatusUpdated:
		return applyReplayedStatusUpdated(status, event)
	case eventArtifactWritten:
		return applyReplayedArtifactWritten(status, event)
	case eventAttemptStarted:
		return applyReplayedAttemptStarted(status, event)
	case eventAttemptPrompted:
		return applyReplayedAttemptPrompted(status, event)
	case eventAttemptLogged:
		return applyReplayedAttemptLogged(status, event)
	case eventAttemptProcess:
		return applyReplayedAttemptProcess(status, event)
	case eventAttemptFinished, eventAttemptRecovered:
		return applyReplayedAttemptFinished(status, event)
	case eventAttemptReported:
		return applyReplayedAttemptReported(status, event)
	case eventAttemptWarning:
		return applyReplayedAttemptWarningEvent(status, event)
	case eventReportIgnored:
		return validateReplayedReportIgnored(event)
	case eventRunContinued:
		return applyReplayedRunContinued(status, event)
	case eventWorkflowStepSkipped:
		return applyReplayedWorkflowStepSkipped(status, event)
	case eventWorkflowSoftCap:
		return applyReplayedWorkflowSoftCap(status, event)
	case eventWorkflowHardCap:
		return applyReplayedWorkflowHardCap(status, event)
	case eventWorkflowHardCapOverride:
		return applyReplayedWorkflowHardCapOverride(status, event)
	default:
		return nil
	}
}

func applyReplayedStatusUpdated(status *Status, event Event) error {
	var payload statusUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d status payload: %w", event.Sequence, err)
	}

	if payload.State == "" {
		return stableerr.Errorf("event %d status state is required", event.Sequence)
	}

	if status.ActiveAttempt != nil && payload.State != stateRunning {
		return stableerr.Errorf("event %d updates run state to %q while attempt %q is active", event.Sequence, payload.State, status.ActiveAttempt.AttemptID)
	}

	if err := applyReplayedWorkflowStateEntry(status, event, payload.WorkflowStateEntry); err != nil {
		return err
	}

	status.State = payload.State

	return nil
}

func applyReplayedArtifactWritten(status *Status, event Event) error {
	var payload artifactWrittenPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d artifact payload: %w", event.Sequence, err)
	}

	if err := validateArtifactEventRef(event, payload.Artifact); err != nil {
		return err
	}

	status.Artifacts = append(status.Artifacts, payload.Artifact)

	return nil
}

func applyReplayedAttemptStarted(status *Status, event Event) error {
	var payload attemptStartedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.started payload: %w", event.Sequence, err)
	}

	routing := attemptStartRoutingFromFields(payload.ConsumeAttemptID, payload.RetryLineage, payload.SupersedeReason)
	if err := validateReplayedAttemptStart(*status, event, payload, routing); err != nil {
		return err
	}

	if err := applyReplayedWorkflowStateEntry(status, event, payload.WorkflowStateEntry); err != nil {
		return err
	}

	if payload.ConsumedWorkflowLoopHardCapOverride != nil {
		status.WorkflowLoop.PendingHardCapOverride = nil
	}

	status.Continued = nil
	attempt := payload.Attempt
	applyAttemptStartRouting(status, event.Time, attempt.AttemptID, routing)
	status.ActiveAttempt = &attempt
	status.Attempts = append(status.Attempts, attempt)

	return nil
}

func validateReplayedAttemptStart(status Status, event Event, payload attemptStartedPayload, routing attemptStartRouting) error {
	if status.State != stateRunning {
		return stableerr.Errorf("event %d starts attempt while run state is %q, want %q", event.Sequence, status.State, stateRunning)
	}

	if status.ActiveAttempt != nil {
		return stableerr.Errorf("event %d starts attempt %q while attempt %q is active", event.Sequence, payload.Attempt.AttemptID, status.ActiveAttempt.AttemptID)
	}

	if err := validateAttemptStartRouting(status, routing); err != nil {
		return fmt.Errorf("event %d attempt routing: %w", event.Sequence, err)
	}

	if payload.ConsumedWorkflowLoopHardCapOverride != nil {
		if payload.WorkflowStateEntry == nil {
			return stableerr.Errorf("event %d consumes workflow loop hard cap override without workflow state entry", event.Sequence)
		}

		if err := validateWorkflowLoopHardCapOverrideConsumption(status, *payload.WorkflowStateEntry, *payload.ConsumedWorkflowLoopHardCapOverride); err != nil {
			return fmt.Errorf("event %d workflow loop hard cap override consumption: %w", event.Sequence, err)
		}
	}

	if err := validateStartedAttemptEvent(event, payload.Attempt, status.RunID); err != nil {
		return err
	}

	if slices.ContainsFunc(status.Attempts, func(existing Attempt) bool {
		return existing.AttemptID == payload.Attempt.AttemptID
	}) {
		return stableerr.Errorf("event %d duplicate attempt %q", event.Sequence, payload.Attempt.AttemptID)
	}

	return nil
}

func applyReplayedAttemptPrompted(status *Status, event Event) error {
	var payload attemptPromptedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.prompted payload: %w", event.Sequence, err)
	}

	return updateReplayedActiveAttempt(status, event, payload.AttemptID, func(attempt *Attempt) error {
		return applyAttemptPromptRef(*status, attempt, payload.AttemptID, payload.PromptRef)
	})
}

func applyReplayedAttemptLogged(status *Status, event Event) error {
	var payload attemptLoggedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.logged payload: %w", event.Sequence, err)
	}

	return updateReplayedActiveAttempt(status, event, payload.AttemptID, func(attempt *Attempt) error {
		return applyAttemptLogRef(*status, attempt, payload.AttemptID, payload.LogRef)
	})
}

func applyReplayedAttemptProcess(status *Status, event Event) error {
	var payload attemptProcessPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.process_started payload: %w", event.Sequence, err)
	}

	return updateReplayedActiveAttempt(status, event, payload.AttemptID, func(attempt *Attempt) error {
		return applyAttemptProcessMetadata(attempt, payload.AttemptID, payload.PID, payload.ProcessStartTime)
	})
}

func applyReplayedAttemptFinished(status *Status, event Event) error {
	var payload attemptFinishedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d %s payload: %w", event.Sequence, event.Type, err)
	}

	return finishReplayedActiveAttempt(status, event, payload, event.Type == eventAttemptRecovered)
}

func applyReplayedAttemptReported(status *Status, event Event) error {
	var payload attemptReportedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.reported payload: %w", event.Sequence, err)
	}

	return reportReplayedActiveAttempt(status, event, payload)
}

func applyReplayedAttemptWarningEvent(status *Status, event Event) error {
	var payload attemptWarningPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d attempt.warning payload: %w", event.Sequence, err)
	}

	return applyReplayedAttemptWarning(status, event, payload.Warning)
}

func validateReplayedReportIgnored(event Event) error {
	var payload reportIgnoredPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d report.ignored payload: %w", event.Sequence, err)
	}

	if payload.Reason == "" {
		return stableerr.Errorf("event %d report ignored reason is required", event.Sequence)
	}

	if payload.RunID != "" && payload.RunID != event.RunID {
		return stableerr.Errorf("event %d report ignored run_id %q does not match event run_id %q", event.Sequence, payload.RunID, event.RunID)
	}

	return nil
}

func applyReplayedRunContinued(status *Status, event Event) error {
	var payload runContinuedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d run.continued payload: %w", event.Sequence, err)
	}

	if err := validateRunContinuedPayload(*status, payload); err != nil {
		return fmt.Errorf("event %d run.continued payload: %w", event.Sequence, err)
	}

	applyRunContinued(status, payload)

	return nil
}

func applyReplayedWorkflowStepSkipped(status *Status, event Event) error {
	var payload workflowStepSkippedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d workflow.step_skipped payload: %w", event.Sequence, err)
	}

	skipped, err := validateWorkflowStepSkippedPayload(*status, event, payload)
	if err != nil {
		return err
	}

	if err := applyReplayedWorkflowStateEntry(status, event, payload.WorkflowStateEntry); err != nil {
		return err
	}

	applyAttemptOutcomeConsumption(status, event, payload.ConsumeAttemptID)
	applyStepSkipped(status, skipped)
	status.State = payload.State
	status.RetryLineage = nil
	status.Continued = nil

	return nil
}

func applyReplayedWorkflowSoftCap(status *Status, event Event) error {
	var payload workflowLoopSoftCapPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d workflow.loop_soft_cap payload: %w", event.Sequence, err)
	}

	if err := validateWorkflowLoopSoftCap(payload.Cap); err != nil {
		return fmt.Errorf("event %d workflow.loop_soft_cap payload: %w", event.Sequence, err)
	}

	if payload.Cap.Workflow != status.Workflow {
		return stableerr.Errorf("event %d workflow loop soft cap workflow %q does not match status workflow %q", event.Sequence, payload.Cap.Workflow, status.Workflow)
	}

	applyWorkflowLoopSoftCap(status, payload.Cap)

	return nil
}

func applyReplayedWorkflowHardCap(status *Status, event Event) error {
	var payload workflowLoopHardCapPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d workflow.loop_hard_cap payload: %w", event.Sequence, err)
	}

	if err := validateWorkflowLoopHardCap(payload.Cap); err != nil {
		return fmt.Errorf("event %d workflow.loop_hard_cap payload: %w", event.Sequence, err)
	}

	if err := validateReplayedWorkflowHardCap(*status, event, payload); err != nil {
		return err
	}

	applyWorkflowLoopHardCap(status, payload.Cap)
	status.State = stateBlockedHuman

	return nil
}

func validateReplayedWorkflowHardCap(status Status, event Event, payload workflowLoopHardCapPayload) error {
	if payload.Cap.Workflow != status.Workflow {
		return stableerr.Errorf("event %d workflow loop hard cap workflow %q does not match status workflow %q", event.Sequence, payload.Cap.Workflow, status.Workflow)
	}

	if payload.State != stateBlockedHuman {
		return stableerr.Errorf("event %d workflow loop hard cap state = %q, want %q", event.Sequence, payload.State, stateBlockedHuman)
	}

	if status.ActiveAttempt != nil {
		return stableerr.Errorf("event %d blocks workflow loop while attempt %q is active", event.Sequence, status.ActiveAttempt.AttemptID)
	}

	return nil
}

func applyReplayedWorkflowHardCapOverride(status *Status, event Event) error {
	var payload workflowLoopHardCapOverridePayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event %d workflow.loop_hard_cap_override payload: %w", event.Sequence, err)
	}

	if err := validateWorkflowLoopHardCapOverride(payload.Override); err != nil {
		return fmt.Errorf("event %d workflow.loop_hard_cap_override payload: %w", event.Sequence, err)
	}

	if err := validateReplayedWorkflowHardCapOverride(*status, event, payload); err != nil {
		return err
	}

	applyWorkflowLoopHardCapOverride(status, payload.Override)
	status.State = stateRunning

	return nil
}

func validateReplayedWorkflowHardCapOverride(status Status, event Event, payload workflowLoopHardCapOverridePayload) error {
	if payload.Override.Workflow != status.Workflow {
		return stableerr.Errorf("event %d workflow loop hard cap override workflow %q does not match status workflow %q", event.Sequence, payload.Override.Workflow, status.Workflow)
	}

	if payload.State != stateRunning {
		return stableerr.Errorf("event %d workflow loop hard cap override state = %q, want %q", event.Sequence, payload.State, stateRunning)
	}

	block := status.WorkflowLoop.HardCapBlock
	if status.State != stateBlockedHuman || block == nil {
		return stableerr.Errorf("event %d workflow loop hard cap override requires active hard-cap block", event.Sequence)
	}

	if block.Workflow != payload.Override.Workflow ||
		block.BlockedState != payload.Override.TargetState ||
		block.CurrentCount != payload.Override.CountBeforeOverride ||
		block.ProspectiveCount != payload.Override.CountAfterOverride ||
		block.Soft != payload.Override.Soft ||
		block.Hard != payload.Override.Hard ||
		block.Reason != payload.Override.Reason {
		return stableerr.Errorf("event %d workflow loop hard cap override does not match active hard-cap block", event.Sequence)
	}

	return nil
}

func updateReplayedActiveAttempt(status *Status, event Event, attemptID string, update func(*Attempt) error) error {
	if attemptID == "" {
		return stableerr.Errorf("event %d attempt_id is required", event.Sequence)
	}

	if status.ActiveAttempt == nil {
		return stableerr.Errorf("event %d has no active attempt", event.Sequence)
	}

	if status.ActiveAttempt.AttemptID != attemptID {
		return stableerr.Errorf("event %d targets attempt %q while %q is active", event.Sequence, attemptID, status.ActiveAttempt.AttemptID)
	}

	attempt := *status.ActiveAttempt
	if err := update(&attempt); err != nil {
		return fmt.Errorf("event %d attempt %q: %w", event.Sequence, attemptID, err)
	}

	status.ActiveAttempt = &attempt
	for i := len(status.Attempts) - 1; i >= 0; i-- {
		if status.Attempts[i].AttemptID == attemptID {
			status.Attempts[i] = attempt
			return nil
		}
	}

	return stableerr.Errorf("event %d attempt %q not found in history", event.Sequence, attemptID)
}

func finishReplayedActiveAttempt(status *Status, event Event, payload attemptFinishedPayload, recovered bool) error {
	if err := validateTerminalAttemptOutcomeFields(payload.State, payload.Status, payload.Result, "terminal attempt"); err != nil {
		return fmt.Errorf("event %d %w", event.Sequence, err)
	}

	outcome := terminalOutcomeFromPayload(payload, recovered)
	if err := updateReplayedActiveAttempt(status, event, payload.AttemptID, func(attempt *Attempt) error {
		finishedAt := event.Time
		_, err := applyTerminalAttemptOutcome(*status, attempt, outcome)
		attempt.FinishedAt = &finishedAt

		return err
	}); err != nil {
		return err
	}

	status.ActiveAttempt = nil

	return nil
}

func reportReplayedActiveAttempt(status *Status, event Event, payload attemptReportedPayload) error {
	if err := validateReportTerminalization(payload.State, payload.Report); err != nil {
		return fmt.Errorf("event %d %w", event.Sequence, err)
	}

	if payload.AttemptID == "" {
		return stableerr.Errorf("event %d attempt_id is required", event.Sequence)
	}

	if payload.Report.AttemptID != payload.AttemptID {
		return stableerr.Errorf("event %d report attempt_id %q does not match", event.Sequence, payload.Report.AttemptID)
	}

	if err := updateReplayedActiveAttempt(status, event, payload.AttemptID, func(attempt *Attempt) error {
		finishedAt := event.Time

		req := RecordReportRequest{
			ExitCode:             payload.ExitCode,
			ExitState:            payload.ExitState,
			LogRef:               payload.LogRef,
			AllowStartingAttempt: attempt.State == AttemptStateStarting,
		}
		if err := applyAttemptReport(*status, attempt, payload.State, payload.Report, req); err != nil {
			return err
		}

		attempt.FinishedAt = &finishedAt

		return nil
	}); err != nil {
		return err
	}

	if payload.Report.ReportRef != nil {
		status.Artifacts = append(status.Artifacts, *payload.Report.ReportRef)
	}

	for _, ref := range payload.FollowupRefs {
		if err := validateArtifactRef(ref, event.Sequence); err != nil {
			return fmt.Errorf("event %d followup artifact %s: %w", event.Sequence, ref.Path, err)
		}

		if ref.Kind != KindFollowup {
			return stableerr.Errorf("event %d followup artifact %s kind %q, want %q", event.Sequence, ref.Path, ref.Kind, KindFollowup)
		}

		status.Artifacts = append(status.Artifacts, ref)
	}

	status.ActiveAttempt = nil

	return nil
}

func applyReplayedAttemptWarning(status *Status, event Event, warning AttemptWarning) error {
	if err := validateAttemptWarning(*status, warning); err != nil {
		return fmt.Errorf("event %d warning: %w", event.Sequence, err)
	}

	if !warning.Time.Equal(event.Time) {
		return stableerr.Errorf("event %d warning time does not match event time", event.Sequence)
	}

	status.Warnings = append(status.Warnings, warning)

	return nil
}

func validateRunCreated(event Event, status Status) (createRunPayload, error) {
	if event.Sequence != 1 || event.Type != eventRunCreated {
		return createRunPayload{}, stableerr.Errorf("line 1: expected %s event", eventRunCreated)
	}

	var payload createRunPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return createRunPayload{}, fmt.Errorf("event 1 run.created payload: %w", err)
	}

	if payload.Workflow != status.Workflow {
		return createRunPayload{}, stableerr.Errorf("event 1 workflow %q does not match status.json workflow %q", payload.Workflow, status.Workflow)
	}

	return payload, nil
}
