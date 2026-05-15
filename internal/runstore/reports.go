package runstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecordAttemptReport terminalizes the current active attempt with a structured worker report.
func (s *Store) RecordAttemptReport(runID string, req RecordReportRequest) (Attempt, Event, error) {
	return s.RecordAttemptReportContext(context.Background(), runID, req)
}

// RecordAttemptReportContext terminalizes the current active attempt with a structured worker report unless ctx is canceled before commit.
func (s *Store) RecordAttemptReportContext(ctx context.Context, runID string, req RecordReportRequest) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, errors.New("context is required")
	}
	report := req.Report
	state := req.State
	if state == "" {
		state = AttemptStateReported
	}
	if err := validateReportTerminalization(state, report); err != nil {
		return Attempt{}, Event{}, err
	}

	var out Attempt
	var event Event
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt == nil {
			return &ReportTargetError{
				RunID:  runID,
				Reason: "report does not target current active attempt",
				Err:    fmt.Errorf("run %q has no active attempt", runID),
			}
		}
		if run.Status.ActiveAttempt.AttemptID != report.AttemptID {
			return &ReportTargetError{
				RunID:  runID,
				Reason: "report does not target current active attempt",
				Err:    fmt.Errorf("run %q active attempt is %q, not %q", runID, run.Status.ActiveAttempt.AttemptID, report.AttemptID),
			}
		}
		attempt := *run.Status.ActiveAttempt
		if !req.ReportContentSet && report.ReportRef != nil {
			return errors.New("report_ref cannot be supplied by callers; provide report content for the run store to stage")
		}
		eventTime := normalizeTime(req.Time)
		eventSequence := nextEventSequence(run)
		var staged []stagedArtifact
		if req.ReportContentSet {
			ref, stagedReport, err := s.stageReportArtifactForEvent(run, req.ReportName, req.ReportContent, eventSequence)
			if err != nil {
				return err
			}
			staged = append(staged, stagedReport)
			report.ReportRef = &ref
		}
		followupRefs, stagedFollowup, err := s.stageReportFollowupsForEvent(run, report, state, eventTime, eventSequence)
		if err != nil {
			cleanupStagedArtifacts(staged)
			return err
		}
		if stagedFollowup != nil {
			staged = append(staged, *stagedFollowup)
		}
		defer cleanupStagedArtifacts(staged)
		if err := applyAttemptReport(run.Status, &attempt, state, report, req); err != nil {
			return err
		}
		payload, err := marshalPayload(attemptReportedPayload{
			AttemptID:    report.AttemptID,
			State:        state,
			Report:       *attempt.Report,
			ExitCode:     attempt.ExitCode,
			ExitState:    attempt.ExitState,
			LogRef:       attempt.LogRef,
			FollowupRefs: followupRefs,
		})
		if err != nil {
			return err
		}
		event = Event{Time: eventTime, Type: eventAttemptReported, Payload: payload}
		var committed []stagedArtifact
		for _, artifact := range staged {
			if err := artifact.commit(); err != nil {
				return errors.Join(err, rollbackStagedArtifacts(committed))
			}
			committed = append(committed, artifact)
		}
		status, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			finishedAt := event.Time
			attempt.FinishedAt = &finishedAt
			for i := len(status.Attempts) - 1; i >= 0; i-- {
				if status.Attempts[i].AttemptID == report.AttemptID {
					status.Attempts[i] = attempt
					break
				}
			}
			status.ActiveAttempt = nil
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
			if attempt.ReportRef != nil {
				status.Artifacts = append(status.Artifacts, *attempt.ReportRef)
			}
			status.Artifacts = append(status.Artifacts, followupRefs...)
		})
		event = committedEvent
		if err != nil {
			if statusBackedEventPossiblyCommitted(err) {
				out = currentOrLatestAttempt(status, report.AttemptID)
				return err
			}
			if rollbackErr := rollbackStagedArtifacts(committed); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			return err
		}
		out = currentOrLatestAttempt(status, report.AttemptID)
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

func (s *Store) stageReportFollowupsForEvent(run *Run, report Report, state string, at time.Time, sequence int) ([]ArtifactRef, *stagedArtifact, error) {
	if state != AttemptStateReported || len(report.Followups) == 0 {
		return nil, nil, nil
	}
	var content []byte
	for _, followup := range report.Followups {
		entry, err := formatFollowupEntry(RecordFollowupRequest{
			Followup:  followup,
			Source:    FollowupSourceReport,
			StepID:    report.StepID,
			AgentID:   report.AgentID,
			AttemptID: report.AttemptID,
			Time:      at,
		})
		if err != nil {
			return nil, nil, err
		}
		content = append(content, entry...)
	}
	ref, staged, err := s.stageFollowupArtifactForEvent(run, FollowupSourceReport, content, sequence)
	if err != nil {
		return nil, nil, err
	}
	return []ArtifactRef{ref}, &staged, nil
}

func (s *Store) stageFollowupArtifactForEvent(run *Run, source FollowupSource, content []byte, sequence int) (ArtifactRef, stagedArtifact, error) {
	relPath, err := artifactPath(KindFollowup, string(source), sequence)
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	if err := validateRelativeArtifactPath(relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	if err := validateArtifactWriteAllowed(run.Status.Artifacts, KindFollowup, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	ref := ArtifactRef{
		Kind:          KindFollowup,
		Path:          relPath,
		Name:          string(source),
		EventSequence: sequence,
	}
	path := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := ensureArtifactParentDir(run.Path, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}
	staged, err := stageArtifact(path, Artifact{Kind: KindFollowup, Name: string(source), Content: content})
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}
	return ref, staged, nil
}

func (s *Store) stageReportArtifactForEvent(run *Run, name string, content []byte, sequence int) (ArtifactRef, stagedArtifact, error) {
	relPath, err := artifactPath(KindReport, name, sequence)
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	if err := validateRelativeArtifactPath(relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	if err := validateArtifactWriteAllowed(run.Status.Artifacts, KindReport, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	ref := ArtifactRef{
		Kind:          KindReport,
		Path:          relPath,
		Name:          name,
		EventSequence: sequence,
	}
	path := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := ensureArtifactParentDir(run.Path, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}
	if err := validateArtifactFile(path); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}
	if existing, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is a validated report artifact path scoped to the run directory.
		if !bytes.Equal(existing, content) {
			return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s already exists with different content", run.ID, relPath)
		}
		noop := func() error { return nil }
		return ref, stagedArtifact{commit: noop, rollback: noop, cleanup: func() {}}, nil
	} else if !os.IsNotExist(err) {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}
	staged, err := stageArtifact(path, Artifact{Kind: KindReport, Name: name, Content: content})
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}
	return ref, staged, nil
}

// RecordIgnoredReport records a report that did not target the active attempt.
func (s *Store) RecordIgnoredReport(runID string, req IgnoreReportRequest) (Event, error) {
	return s.RecordIgnoredReportContext(context.Background(), runID, req)
}

// RecordIgnoredReportContext records a report that did not target the active attempt unless ctx is canceled before commit.
func (s *Store) RecordIgnoredReportContext(ctx context.Context, runID string, req IgnoreReportRequest) (Event, error) {
	if ctx == nil {
		return Event{}, errors.New("context is required")
	}
	if req.Reason == "" {
		return Event{}, errors.New("reason is required")
	}
	if req.RunID != "" && req.RunID != runID {
		return Event{}, fmt.Errorf("report ignored run_id %q does not match run %q", req.RunID, runID)
	}
	payload, err := marshalPayload(reportIgnoredPayload{
		RunID:     req.RunID,
		StepID:    req.StepID,
		AgentID:   req.AgentID,
		AttemptID: req.AttemptID,
		Reason:    req.Reason,
		Errors:    req.Errors,
	})
	if err != nil {
		return Event{}, err
	}
	event := Event{Time: req.Time, Type: eventReportIgnored, Payload: payload}
	var committed Event
	err = s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		_, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		committed = committedEvent
		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return committed, err
		}
		return Event{}, err
	}
	return committed, nil
}
