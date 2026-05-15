package runstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// StartAttempt records a new starting worker attempt for a running run.
func (s *Store) StartAttempt(runID string, req StartAttemptRequest) (Attempt, Event, error) {
	return s.StartAttemptContext(context.Background(), runID, req)
}

// StartAttemptContext records a new starting worker attempt for a running run unless ctx is canceled before the attempt commits.
func (s *Store) StartAttemptContext(ctx context.Context, runID string, req StartAttemptRequest) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, fmt.Errorf("start attempt context: %w", err)
	}
	req.Time = normalizeTime(req.Time)
	attempt, err := newStartedAttempt(runID, req)
	if err != nil {
		return Attempt{}, Event{}, err
	}
	var out Attempt
	var event Event
	err = s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start attempt context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.State != stateRunning {
			return stableerr.Errorf("run %q state is %q, want %q to start attempt", runID, run.Status.State, stateRunning)
		}
		if run.Status.ActiveAttempt != nil {
			return stableerr.Errorf("run %q already has active attempt %q", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if slices.ContainsFunc(run.Status.Attempts, func(existing Attempt) bool {
			return existing.AttemptID == attempt.AttemptID
		}) {
			return stableerr.Errorf("run %q already has attempt %q", runID, attempt.AttemptID)
		}
		routing := attemptStartRoutingFromFields(req.ConsumeAttemptID, req.RetryLineage, req.SupersedeReason)
		if err := validateAttemptStartRouting(run.Status, routing); err != nil {
			return fmt.Errorf("run %q %w", runID, err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start attempt context: %w", err)
		}
		var workflowEntry *WorkflowStateEntry
		if req.WorkflowStateEntry.State != "" {
			entry, err := nextWorkflowStateEntry(run.Status, req.WorkflowStateEntry)
			if err != nil {
				return err
			}
			if req.ConsumeWorkflowLoopHardCapOverride != nil {
				if err := validateWorkflowLoopHardCapOverrideConsumption(run.Status, entry, *req.ConsumeWorkflowLoopHardCapOverride); err != nil {
					return err
				}
			}
			workflowEntry = &entry
		} else if req.ConsumeWorkflowLoopHardCapOverride != nil {
			return stableerr.New("workflow loop hard-cap override consumption requires workflow state entry")
		}
		payload, err := marshalPayload(attemptStartedPayload{
			Attempt:                             attempt,
			ConsumeAttemptID:                    routing.ConsumeAttemptID,
			RetryLineage:                        cloneRetryLineagePtr(routing.RetryLineage),
			SupersedeReason:                     routing.SupersedeReason,
			WorkflowStateEntry:                  workflowEntry,
			ConsumedWorkflowLoopHardCapOverride: req.ConsumeWorkflowLoopHardCapOverride,
		})
		if err != nil {
			return err
		}
		event = Event{Time: req.Time, Type: eventAttemptStarted, Payload: payload}
		status, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			attempt.StartedAt = event.Time
			if workflowEntry != nil {
				applyWorkflowStateEntry(status, *workflowEntry)
			}
			if req.ConsumeWorkflowLoopHardCapOverride != nil {
				status.WorkflowLoop.PendingHardCapOverride = nil
			}
			status.Continued = nil
			applyAttemptStartRouting(status, event.Time, attempt.AttemptID, routing)
			status.ActiveAttempt = &attempt
			status.Attempts = append(status.Attempts, attempt)
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		event = committedEvent
		if err != nil {
			if statusBackedEventPossiblyCommitted(err) {
				out = *status.ActiveAttempt
				return err
			}
			return err
		}
		out = *status.ActiveAttempt
		return nil
	})
	if err != nil {
		if out.AttemptID != "" {
			return out, event, err
		}
		return Attempt{}, Event{}, err
	}
	return out, event, nil
}

// RecordAttemptPrompt links a prompt artifact to the current active attempt.
func (s *Store) RecordAttemptPrompt(runID string, req AttemptPromptRequest) (Attempt, Event, error) {
	return s.RecordAttemptPromptContext(context.Background(), runID, req)
}

// RecordAttemptPromptContext links a prompt artifact unless ctx is canceled before commit.
func (s *Store) RecordAttemptPromptContext(ctx context.Context, runID string, req AttemptPromptRequest) (Attempt, Event, error) {
	payload := attemptPromptedPayload{
		AttemptID: req.AttemptID,
		PromptRef: req.PromptRef,
	}
	return s.updateActiveAttemptContext(ctx, runID, req.AttemptID, req.Time, eventAttemptPrompted, func(status Status, attempt *Attempt) (any, error) {
		if err := applyAttemptPromptRef(status, attempt, req.AttemptID, req.PromptRef); err != nil {
			return nil, err
		}
		return payload, nil
	})
}

// RecordAttemptLog links a log artifact to the current active attempt.
func (s *Store) RecordAttemptLog(runID string, req AttemptLogRequest) (Attempt, Event, error) {
	return s.RecordAttemptLogContext(context.Background(), runID, req)
}

// RecordAttemptLogContext links a log artifact unless ctx is canceled before commit.
func (s *Store) RecordAttemptLogContext(ctx context.Context, runID string, req AttemptLogRequest) (Attempt, Event, error) {
	payload := attemptLoggedPayload{
		AttemptID: req.AttemptID,
		LogRef:    req.LogRef,
	}
	return s.updateActiveAttemptContext(ctx, runID, req.AttemptID, req.Time, eventAttemptLogged, func(status Status, attempt *Attempt) (any, error) {
		if err := applyAttemptLogRef(status, attempt, req.AttemptID, req.LogRef); err != nil {
			return nil, err
		}
		return payload, nil
	})
}

// RecordAttemptProcess records worker process metadata for the current active attempt.
func (s *Store) RecordAttemptProcess(runID string, req AttemptProcessRequest) (Attempt, Event, error) {
	return s.RecordAttemptProcessContext(context.Background(), runID, req)
}

// RecordAttemptProcessContext records worker process metadata unless ctx is canceled before the process event commits.
func (s *Store) RecordAttemptProcessContext(ctx context.Context, runID string, req AttemptProcessRequest) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, stableerr.New("context is required")
	}
	if req.PID <= 0 {
		return Attempt{}, Event{}, stableerr.New("process id must be > 0")
	}
	payload := attemptProcessPayload{
		AttemptID:        req.AttemptID,
		PID:              req.PID,
		ProcessStartTime: req.ProcessStartTime,
	}
	return s.updateActiveAttemptContext(ctx, runID, req.AttemptID, req.Time, eventAttemptProcess, func(_ Status, attempt *Attempt) (any, error) {
		if err := applyAttemptProcessMetadata(attempt, req.AttemptID, req.PID, req.ProcessStartTime); err != nil {
			return nil, err
		}
		return payload, nil
	})
}

// FinishAttempt terminalizes the current active attempt.
func (s *Store) FinishAttempt(runID string, req FinishAttemptRequest) (Attempt, Event, error) {
	return s.FinishAttemptContext(context.Background(), runID, req)
}

// FinishAttemptContext terminalizes the current active attempt unless ctx is canceled before the terminal event commits.
func (s *Store) FinishAttemptContext(ctx context.Context, runID string, req FinishAttemptRequest) (Attempt, Event, error) {
	return s.terminalizeAttempt(ctx, runID, req, eventAttemptFinished, false)
}

// RecoverAttempt terminalizes an unverifiable active attempt during launcher restart recovery.
func (s *Store) RecoverAttempt(runID string, req FinishAttemptRequest) (Attempt, Event, error) {
	return s.RecoverAttemptContext(context.Background(), runID, req)
}

// RecoverAttemptContext terminalizes an unverifiable active attempt unless ctx is canceled before commit.
func (s *Store) RecoverAttemptContext(ctx context.Context, runID string, req FinishAttemptRequest) (Attempt, Event, error) {
	return s.terminalizeAttempt(ctx, runID, req, eventAttemptRecovered, true)
}

// RecordAttemptWarning records a process warning without changing attempt outcome.
func (s *Store) RecordAttemptWarning(runID string, warning AttemptWarning) (Status, Event, error) {
	return s.RecordAttemptWarningContext(context.Background(), runID, warning)
}

// RecordAttemptWarningContext records a process warning unless ctx is canceled before commit.
func (s *Store) RecordAttemptWarningContext(ctx context.Context, runID string, warning AttemptWarning) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	warning.Time = normalizeTime(warning.Time)
	payload, err := marshalPayload(attemptWarningPayload{Warning: warning})
	if err != nil {
		return Status{}, Event{}, err
	}
	event := Event{Time: warning.Time, Type: eventAttemptWarning, Payload: payload}
	var status Status
	err = s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("record attempt warning context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if err := validateAttemptWarning(run.Status, warning); err != nil {
			return fmt.Errorf("run %q %w", runID, err)
		}
		var committedEvent Event
		status, committedEvent, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			warning.Time = event.Time
			status.Warnings = append(status.Warnings, warning)
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		event = committedEvent
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

func (s *Store) terminalizeAttempt(ctx context.Context, runID string, req FinishAttemptRequest, eventType string, recovered bool) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, stableerr.New("context is required")
	}
	if err := validateFinishedAttempt(req); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, fmt.Errorf("terminalize attempt: %w", err)
	}
	outcome := terminalOutcomeFromFinishRequest(req, recovered)
	return s.updateActiveAttemptContext(ctx, runID, req.AttemptID, req.Time, eventType, func(status Status, attempt *Attempt) (any, error) {
		logRef, err := applyTerminalAttemptOutcome(status, attempt, outcome)
		if err != nil {
			return nil, err
		}
		effective := outcome
		effective.LogRef = logRef
		return effective.payload(req.AttemptID), nil
	})
}

func terminalOutcomeFromFinishRequest(req FinishAttemptRequest, recovered bool) terminalAttemptOutcome {
	return terminalAttemptOutcome{
		State:     req.State,
		Status:    req.Status,
		Result:    req.Result,
		ExitCode:  req.ExitCode,
		ExitState: req.ExitState,
		LogRef:    req.LogRef,
		Recovered: recovered,
	}
}

func terminalOutcomeFromPayload(payload attemptFinishedPayload, recovered bool) terminalAttemptOutcome {
	return terminalAttemptOutcome{
		State:     payload.State,
		Status:    payload.Status,
		Result:    payload.Result,
		ExitCode:  payload.ExitCode,
		ExitState: payload.ExitState,
		LogRef:    payload.LogRef,
		Recovered: recovered,
	}
}

type terminalAttemptOutcome struct {
	State     string
	Status    string
	Result    string
	ExitCode  *int
	ExitState string
	LogRef    *ArtifactRef
	Recovered bool
}

func (outcome terminalAttemptOutcome) payload(attemptID string) attemptFinishedPayload {
	return attemptFinishedPayload{
		AttemptID: attemptID,
		State:     outcome.State,
		Status:    outcome.Status,
		Result:    outcome.Result,
		ExitCode:  outcome.ExitCode,
		ExitState: outcome.ExitState,
		LogRef:    outcome.LogRef,
	}
}

func applyAttemptPromptRef(status Status, attempt *Attempt, attemptID string, promptRef ArtifactRef) error {
	if attempt.State != AttemptStateStarting {
		return stableerr.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.PromptRef != nil {
		return stableerr.Errorf("attempt %q already has prompt ref", attemptID)
	}
	if err := validateRecordedArtifactRef(status, promptRef, KindPrompt); err != nil {
		return err
	}
	ref := promptRef
	attempt.PromptRef = &ref
	return nil
}

func applyAttemptLogRef(status Status, attempt *Attempt, attemptID string, logRef ArtifactRef) error {
	if attempt.State != AttemptStateStarting {
		return stableerr.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.LogRef != nil {
		return stableerr.Errorf("attempt %q already has log ref", attemptID)
	}
	if err := validateRecordedArtifactRef(status, logRef, KindLog); err != nil {
		return err
	}
	ref := logRef
	attempt.LogRef = &ref
	return nil
}

func applyAttemptProcessMetadata(attempt *Attempt, attemptID string, pid int, processStartTime string) error {
	if pid <= 0 {
		return stableerr.New("process id must be > 0")
	}
	if attempt.State != AttemptStateStarting {
		return stableerr.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.PID != 0 {
		return stableerr.Errorf("attempt %q already has process metadata", attemptID)
	}
	if attempt.PromptRef == nil {
		return stableerr.Errorf("attempt %q prompt ref is required before process start", attemptID)
	}
	if attempt.LogRef == nil {
		return stableerr.Errorf("attempt %q log ref is required before process start", attemptID)
	}
	if err := validateProcessStartTime(processStartTime); err != nil {
		return err
	}
	attempt.State = AttemptStateActive
	attempt.PID = pid
	attempt.ProcessStartTime = processStartTime
	return nil
}

func applyTerminalAttemptOutcome(status Status, attempt *Attempt, outcome terminalAttemptOutcome) (*ArtifactRef, error) {
	logRef := outcome.LogRef
	if logRef == nil {
		logRef = attempt.LogRef
	}
	if logRef != nil {
		if err := validateRecordedArtifactRef(status, *logRef, KindLog); err != nil {
			return nil, err
		}
	}
	if err := validateAttemptTerminalizationHasProcessContext(*attempt, outcome.State); err != nil {
		return nil, err
	}
	attempt.State = outcome.State
	attempt.Status = outcome.Status
	attempt.Result = outcome.Result
	attempt.ExitCode = outcome.ExitCode
	attempt.ExitState = outcome.ExitState
	attempt.LogRef = logRef
	attempt.Recovered = outcome.Recovered
	return logRef, nil
}

func applyAttemptReport(status Status, attempt *Attempt, state string, report Report, req RecordReportRequest) error {
	switch {
	case attempt.State != AttemptStateActive && (!req.AllowStartingAttempt || attempt.State != AttemptStateStarting):
		return stableerr.Errorf("attempt %q state %q, want active", attempt.AttemptID, attempt.State)
	case report.RunID != attempt.RunID:
		return stableerr.Errorf("report run_id %q does not match active attempt run_id %q", report.RunID, attempt.RunID)
	case report.StepID != attempt.StepID:
		return stableerr.Errorf("report step_id %q does not match active attempt step_id %q", report.StepID, attempt.StepID)
	case report.AgentID != attempt.AgentID:
		return stableerr.Errorf("report agent_id %q does not match active attempt agent_id %q", report.AgentID, attempt.AgentID)
	case report.AttemptID != attempt.AttemptID:
		return stableerr.Errorf("report attempt_id %q does not match active attempt attempt_id %q", report.AttemptID, attempt.AttemptID)
	}
	if report.ReportRef != nil {
		if err := validateArtifactRef(*report.ReportRef, 0); err != nil {
			return err
		}
		if report.ReportRef.Kind != KindReport {
			return stableerr.Errorf("artifact %s kind %q, want %q", report.ReportRef.Path, report.ReportRef.Kind, KindReport)
		}
		ref := *report.ReportRef
		report.ReportRef = &ref
	}
	attempt.State = state
	attempt.Status = report.Status
	attempt.Result = report.Result
	attempt.ExitCode = req.ExitCode
	attempt.ExitState = req.ExitState
	logRef := req.LogRef
	if logRef == nil {
		logRef = attempt.LogRef
	}
	if logRef != nil {
		if err := validateRecordedArtifactRef(status, *logRef, KindLog); err != nil {
			return err
		}
		ref := *logRef
		attempt.LogRef = &ref
	}
	attempt.ReportRef = report.ReportRef
	report.RunID = attempt.RunID
	report.StepID = attempt.StepID
	report.AgentID = attempt.AgentID
	report.AttemptID = attempt.AttemptID
	attempt.Report = &report
	return nil
}

func (s *Store) updateActiveAttemptContext(ctx context.Context, runID, attemptID string, at time.Time, eventType string, apply func(Status, *Attempt) (any, error)) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, fmt.Errorf("update active attempt context: %w", err)
	}
	at = normalizeTime(at)
	if attemptID == "" {
		return Attempt{}, Event{}, stableerr.New("attempt id is required")
	}
	var out Attempt
	var event Event
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update active attempt context: %w", err)
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt == nil {
			return stableerr.Errorf("run %q has no active attempt", runID)
		}
		if run.Status.ActiveAttempt.AttemptID != attemptID {
			return stableerr.Errorf("run %q active attempt is %q, not %q", runID, run.Status.ActiveAttempt.AttemptID, attemptID)
		}
		attempt := *run.Status.ActiveAttempt
		payload, err := apply(run.Status, &attempt)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update active attempt context: %w", err)
		}
		content, err := marshalPayload(payload)
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventType, Payload: content}
		status, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			if event.Type == eventAttemptFinished || event.Type == eventAttemptRecovered || event.Type == eventAttemptReported {
				finishedAt := event.Time
				attempt.FinishedAt = &finishedAt
			}
			status.ActiveAttempt = &attempt
			for i := len(status.Attempts) - 1; i >= 0; i-- {
				if status.Attempts[i].AttemptID == attemptID {
					status.Attempts[i] = attempt
					break
				}
			}
			if attempt.State != AttemptStateActive && attempt.State != AttemptStateStarting {
				status.ActiveAttempt = nil
			}
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		event = committedEvent
		if err != nil {
			if statusBackedEventPossiblyCommitted(err) {
				out = currentOrLatestAttempt(status, attemptID)
				return err
			}
			return err
		}
		out = currentOrLatestAttempt(status, attemptID)
		return nil
	})
	if err != nil {
		if out.AttemptID != "" {
			return out, event, err
		}
		return Attempt{}, Event{}, err
	}
	return out, event, nil
}

func newStartedAttempt(runID string, req StartAttemptRequest) (Attempt, error) {
	switch {
	case req.StepID == "":
		return Attempt{}, stableerr.New("step id is required")
	case req.AgentID == "":
		return Attempt{}, stableerr.New("agent id is required")
	case req.AttemptID == "":
		return Attempt{}, stableerr.New("attempt id is required")
	case req.Timeout <= 0:
		return Attempt{}, stableerr.New("timeout must be > 0")
	case req.ReportExitGrace <= 0:
		return Attempt{}, stableerr.New("report exit grace must be > 0")
	}
	return Attempt{
		RunID:                 runID,
		StepID:                req.StepID,
		AgentID:               req.AgentID,
		AttemptID:             req.AttemptID,
		ConfigSnapshotVersion: req.ConfigSnapshotVersion,
		State:                 AttemptStateStarting,
		Timeout:               req.Timeout.String(),
		ReportExitGrace:       req.ReportExitGrace.String(),
		StartedAt:             normalizeTime(req.Time),
	}, nil
}

func validateFinishedAttempt(req FinishAttemptRequest) error {
	if req.AttemptID == "" {
		return stableerr.New("attempt id is required")
	}
	return validateTerminalAttemptOutcomeFields(req.State, req.Status, req.Result, "attempt")
}

func validateReportTerminalization(state string, report Report) error {
	switch {
	case report.RunID == "":
		return stableerr.New("report run id is required")
	case report.StepID == "":
		return stableerr.New("report step id is required")
	case report.AgentID == "":
		return stableerr.New("report agent id is required")
	case report.AttemptID == "":
		return stableerr.New("report attempt id is required")
	case report.Status == "":
		return stableerr.New("report status is required")
	case report.Result == "":
		return stableerr.New("report result is required")
	case report.Summary == "":
		return stableerr.New("report summary is required")
	case state != AttemptStateReported && state != AttemptStateInvalidReport:
		return stableerr.Errorf("report state %q is not terminal", state)
	case state == AttemptStateInvalidReport && (report.Status != attemptStatusFailed || report.Result != AttemptResultInvalidReport):
		return stableerr.Errorf("report terminal outcome %s/%s with state %q is invalid", report.Status, report.Result, state)
	default:
		return nil
	}
}

func validateTerminalAttemptOutcomeFields(state, status, result, subject string) error {
	if state == "" || status == "" || result == "" {
		return stableerr.Errorf("%s state/status/result are required", subject)
	}
	if !terminalAttemptState(state) {
		return stableerr.Errorf("%s state %q is not terminal", subject, state)
	}
	if !validTerminalAttemptOutcome(state, status, result) {
		return stableerr.Errorf("%s terminal outcome %s/%s with state %q is invalid", subject, status, result, state)
	}
	return nil
}

func validTerminalAttemptOutcome(state, status, result string) bool {
	switch state {
	case AttemptStateMissingReport:
		return status == attemptStatusFailed && result == AttemptResultMissingReport
	case AttemptStateProcessError:
		return status == attemptStatusFailed && result == AttemptResultProcessError
	case AttemptStateTimedOut:
		return status == attemptStatusFailed && result == AttemptResultTimeout
	default:
		return false
	}
}

func validateAttemptTerminalizationHasProcessContext(attempt Attempt, terminalState string) error {
	if attempt.PID == 0 && terminalState != AttemptStateProcessError {
		return stableerr.Errorf("attempt %q has no process metadata; terminal state %q is not allowed before process start", attempt.AttemptID, terminalState)
	}
	return nil
}

func terminalAttemptState(state string) bool {
	switch state {
	case AttemptStateMissingReport, AttemptStateProcessError, AttemptStateTimedOut, AttemptStateReported, AttemptStateInvalidReport:
		return true
	default:
		return false
	}
}

func latestAttempt(attempts []Attempt, attemptID string) Attempt {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].AttemptID == attemptID {
			return attempts[i]
		}
	}
	return Attempt{}
}

func currentOrLatestAttempt(status Status, attemptID string) Attempt {
	if status.ActiveAttempt != nil {
		return *status.ActiveAttempt
	}
	return latestAttempt(status.Attempts, attemptID)
}

// LatestConsumableOutcome returns the latest terminal attempt that workflow
// routing can consume.
func LatestConsumableOutcome(status Status) (Attempt, bool) {
	if status.State != stateRunning || status.ActiveAttempt != nil || len(status.Attempts) == 0 {
		return Attempt{}, false
	}
	attempt := status.Attempts[len(status.Attempts)-1]
	if status.Continued != nil &&
		status.Continued.Mode == ContinueModeResolveBlock &&
		status.Continued.ResolvedAttemptID == attempt.AttemptID {
		return Attempt{}, false
	}
	if !terminalRoutingOutcome(attempt) {
		return Attempt{}, false
	}
	return attempt, true
}

// ResolvedHumanBlockStep returns the workflow step selected by a durable
// resolve-block continuation marker.
func ResolvedHumanBlockStep(status Status) (string, bool) {
	if status.State != stateRunning || status.ActiveAttempt != nil || status.Continued == nil {
		return "", false
	}
	if status.Continued.Mode != ContinueModeResolveBlock || status.Continued.ResolvedStepID == "" {
		return "", false
	}
	return status.Continued.ResolvedStepID, true
}

// ResolvedHumanBlockOutcome returns the terminal attempt resolved by a durable
// resolve-block continuation marker. The outcome is not consumable for routing,
// but it remains the trigger for workflow-loop accounting on the retry launch.
func ResolvedHumanBlockOutcome(status Status) (Attempt, bool) {
	if status.State != stateRunning || status.ActiveAttempt != nil || status.Continued == nil || len(status.Attempts) == 0 {
		return Attempt{}, false
	}
	continued := status.Continued
	if continued.Mode != ContinueModeResolveBlock {
		return Attempt{}, false
	}
	attempt := status.Attempts[len(status.Attempts)-1]
	if attempt.AttemptID != continued.ResolvedAttemptID ||
		attempt.StepID != continued.ResolvedStepID ||
		attempt.Status != continued.ResolvedStatus ||
		attempt.Result != continued.ResolvedResult {
		return Attempt{}, false
	}
	if !terminalRoutingOutcome(attempt) {
		return Attempt{}, false
	}
	return attempt, true
}

func latestResolvableBlockedAttempt(status Status) (Attempt, bool) {
	if status.State != stateBlockedHuman || status.ActiveAttempt != nil || len(status.Attempts) == 0 {
		return Attempt{}, false
	}
	attempt := status.Attempts[len(status.Attempts)-1]
	if !terminalRoutingOutcome(attempt) {
		return Attempt{}, false
	}
	if !latestWorkflowEntryMatchesBlockedAttempt(status, attempt) {
		return Attempt{}, false
	}
	return attempt, true
}

func latestWorkflowEntryMatchesBlockedAttempt(status Status, attempt Attempt) bool {
	if len(status.WorkflowLoop.Entries) == 0 {
		return false
	}
	entry := status.WorkflowLoop.Entries[len(status.WorkflowLoop.Entries)-1]
	return entry.State == stateBlockedHuman &&
		entry.PreviousState == attempt.StepID &&
		entry.TriggerStatus == attempt.Status &&
		entry.TriggerResult == attempt.Result
}

func unconsumedLauncherAttemptOutcome(attempt Attempt) bool {
	if attempt.State == AttemptStateReported {
		return false
	}
	return terminalRoutingOutcome(attempt)
}

func terminalRoutingOutcome(attempt Attempt) bool {
	if attempt.ConsumedByEvent != 0 {
		return false
	}
	if attempt.SupersededBy != "" {
		return false
	}
	if attempt.State == AttemptStateReported && attempt.Status != "" && attempt.Result != "" {
		return true
	}
	if validTerminalAttemptOutcome(attempt.State, attempt.Status, attempt.Result) {
		return true
	}
	if attempt.State == AttemptStateInvalidReport {
		return attempt.Status == attemptStatusFailed && attempt.Result == AttemptResultInvalidReport
	}
	return false
}

func applyAttemptStartRouting(status *Status, at time.Time, newAttemptID string, routing attemptStartRouting) {
	if routing.RetryLineage != nil {
		supersededAt := at
		for i := len(status.Attempts) - 1; i >= 0; i-- {
			if status.Attempts[i].AttemptID == routing.ConsumeAttemptID {
				status.Attempts[i].SupersededBy = newAttemptID
				status.Attempts[i].SupersededAt = &supersededAt
				status.Attempts[i].SupersededReason = routing.SupersedeReason
				break
			}
		}
	}
	status.RetryLineage = cloneRetryLineagePtr(routing.RetryLineage)
}

func applyAttemptOutcomeConsumption(status *Status, event Event, attemptID string) {
	if attemptID == "" {
		return
	}
	for i := len(status.Attempts) - 1; i >= 0; i-- {
		if status.Attempts[i].AttemptID == attemptID {
			status.Attempts[i].ConsumedByEvent = event.Sequence
			return
		}
	}
}

func validateRetryLineage(retry RetryLineage) error {
	if retry.StepID == "" {
		return stableerr.New("retry lineage step_id is required")
	}
	for pair, count := range retry.Counts {
		if pair == "" {
			return stableerr.New("retry lineage pair is required")
		}
		if count < 0 {
			return stableerr.Errorf("retry count for %q must be >= 0, got %d", pair, count)
		}
	}
	return nil
}

type attemptStartRouting struct {
	ConsumeAttemptID string
	RetryLineage     *RetryLineage
	SupersedeReason  string
}

func attemptStartRoutingFromFields(consumeAttemptID string, retryLineage *RetryLineage, supersedeReason string) attemptStartRouting {
	return attemptStartRouting{
		ConsumeAttemptID: consumeAttemptID,
		RetryLineage:     retryLineage,
		SupersedeReason:  supersedeReason,
	}
}

func validateAttemptStartRouting(status Status, routing attemptStartRouting) error {
	latest, hasLatest := LatestConsumableOutcome(status)
	if hasLatest && unconsumedLauncherAttemptOutcome(latest) && routing.ConsumeAttemptID != latest.AttemptID {
		return stableerr.Errorf("has unconsumed launcher outcome %s/%s for attempt %q", latest.Status, latest.Result, latest.AttemptID)
	}
	if routing.ConsumeAttemptID != "" && (!hasLatest || latest.AttemptID != routing.ConsumeAttemptID) {
		return stableerr.Errorf("latest outcome attempt is not %q", routing.ConsumeAttemptID)
	}
	if routing.RetryLineage != nil {
		if routing.ConsumeAttemptID == "" {
			return stableerr.New("retry lineage requires consume_attempt_id")
		}
		if err := validateRetryLineage(*routing.RetryLineage); err != nil {
			return fmt.Errorf("retry lineage: %w", err)
		}
	}
	return nil
}

func validateAttemptOutcomeConsumption(status Status, attemptID string) error {
	if attemptID == "" {
		return nil
	}
	latest, ok := LatestConsumableOutcome(status)
	if !ok {
		return stableerr.Errorf("latest outcome attempt is not %q", attemptID)
	}
	if latest.AttemptID != attemptID {
		return stableerr.Errorf("latest outcome attempt is %q, want %q", latest.AttemptID, attemptID)
	}
	return nil
}

func cloneRetryLineagePtr(retry *RetryLineage) *RetryLineage {
	if retry == nil {
		return nil
	}
	return &RetryLineage{StepID: retry.StepID, Counts: maps.Clone(retry.Counts)}
}

func validateStartedAttemptEvent(event Event, attempt Attempt, runID string) error {
	switch {
	case attempt.RunID != runID:
		return stableerr.Errorf("event %d attempt run_id %q does not match", event.Sequence, attempt.RunID)
	case attempt.StepID == "":
		return stableerr.Errorf("event %d attempt step_id is required", event.Sequence)
	case attempt.AgentID == "":
		return stableerr.Errorf("event %d attempt agent_id is required", event.Sequence)
	case attempt.AttemptID == "":
		return stableerr.Errorf("event %d attempt attempt_id is required", event.Sequence)
	case attempt.State != AttemptStateStarting:
		return stableerr.Errorf("event %d attempt state %q, want starting", event.Sequence, attempt.State)
	case attempt.Status != "":
		return stableerr.Errorf("event %d attempt status must be empty for starting attempt", event.Sequence)
	case attempt.Result != "":
		return stableerr.Errorf("event %d attempt result must be empty for starting attempt", event.Sequence)
	case attempt.PID != 0:
		return stableerr.Errorf("event %d attempt pid must be empty for starting attempt", event.Sequence)
	case attempt.ProcessStartTime != "":
		return stableerr.Errorf("event %d attempt process_start_time must be empty for starting attempt", event.Sequence)
	case attempt.ExitCode != nil:
		return stableerr.Errorf("event %d attempt exit_code must be empty for starting attempt", event.Sequence)
	case attempt.ExitState != "":
		return stableerr.Errorf("event %d attempt exit_state must be empty for starting attempt", event.Sequence)
	case attempt.PromptRef != nil:
		return stableerr.Errorf("event %d attempt prompt_ref must be empty for starting attempt", event.Sequence)
	case attempt.LogRef != nil:
		return stableerr.Errorf("event %d attempt log_ref must be empty for starting attempt", event.Sequence)
	case attempt.ReportRef != nil:
		return stableerr.Errorf("event %d attempt report_ref must be empty for starting attempt", event.Sequence)
	case attempt.Report != nil:
		return stableerr.Errorf("event %d attempt report must be empty for starting attempt", event.Sequence)
	case attempt.FinishedAt != nil:
		return stableerr.Errorf("event %d attempt finished_at must be empty for starting attempt", event.Sequence)
	case attempt.Recovered:
		return stableerr.Errorf("event %d attempt recovered must be false for starting attempt", event.Sequence)
	case attempt.Timeout == "":
		return stableerr.Errorf("event %d attempt timeout is required", event.Sequence)
	case attempt.ReportExitGrace == "":
		return stableerr.Errorf("event %d attempt report_exit_grace is required", event.Sequence)
	case !attempt.StartedAt.Equal(event.Time):
		return stableerr.Errorf("event %d attempt started_at does not match event time", event.Sequence)
	default:
		timeout, err := time.ParseDuration(attempt.Timeout)
		if err != nil || timeout <= 0 {
			return stableerr.Errorf("event %d attempt timeout must be > 0", event.Sequence)
		}
		grace, err := time.ParseDuration(attempt.ReportExitGrace)
		if err != nil || grace <= 0 {
			return stableerr.Errorf("event %d attempt report_exit_grace must be > 0", event.Sequence)
		}
		return nil
	}
}

func validateProcessStartTime(value string) error {
	if value == "" {
		return stableerr.New("process_start_time is required")
	}
	if len(value) > 32 {
		return stableerr.New("process_start_time is too long")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return stableerr.Errorf("process_start_time %q must be decimal digits", value)
		}
	}
	return nil
}

func validateAttemptWarning(status Status, warning AttemptWarning) error {
	if warning.AttemptID == "" {
		return stableerr.New("attempt_id is required")
	}
	if warning.Kind == "" {
		return stableerr.New("kind is required")
	}
	if !slices.ContainsFunc(status.Attempts, func(attempt Attempt) bool {
		return attempt.AttemptID == warning.AttemptID
	}) {
		return stableerr.Errorf("has no attempt %q", warning.AttemptID)
	}
	return nil
}
