package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// AppendEvent appends a caller event and advances status metadata.
func (s *Store) AppendEvent(runID string, event Event) (Event, error) {
	if err := validateRunID(runID); err != nil {
		return Event{}, err
	}

	if event.Type == "" {
		return Event{}, stableerr.New("event type is required")
	}

	if reservedEventType(event.Type) {
		return Event{}, stableerr.Errorf("event type %q is store-owned; use the dedicated runstore API", event.Type)
	}

	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}

	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}

		_, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		event = committedEvent

		return err
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return event, err
		}

		return Event{}, err
	}

	return event, nil
}

// UpdateStatus appends a status event and materializes latest run status.
func (s *Store) UpdateStatus(runID string, update StatusUpdate) (Status, Event, error) {
	return s.UpdateStatusContext(context.Background(), runID, update)
}

// UpdateStatusContext appends a status event unless ctx is canceled before commit.
func (s *Store) UpdateStatusContext(ctx context.Context, runID string, update StatusUpdate) (Status, Event, error) {
	if ctx == nil {
		return Status{}, Event{}, stableerr.New("context is required")
	}

	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}

	if update.State == "" {
		return Status{}, Event{}, stableerr.New("state is required")
	}

	event := Event{
		Time: update.Time,
		Type: eventStatusUpdated,
	}

	var status Status

	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update status context: %w", err)
		}

		run, err := s.load(runID)
		if err != nil {
			return err
		}

		if run.Status.ActiveAttempt != nil && update.State != stateRunning {
			return stableerr.Errorf("run %q has active attempt %q; state update to %q is not allowed", runID, run.Status.ActiveAttempt.AttemptID, update.State)
		}

		var workflowEntry *WorkflowStateEntry

		if update.WorkflowStateEntry.State != "" {
			entry, err := nextWorkflowStateEntry(run.Status, update.WorkflowStateEntry)
			if err != nil {
				return err
			}

			workflowEntry = &entry
		}

		payload, err := marshalPayload(statusUpdatedPayload{State: update.State, WorkflowStateEntry: workflowEntry})
		if err != nil {
			return err
		}

		event.Payload = payload

		var committedEvent Event

		status, committedEvent, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			if workflowEntry != nil {
				applyWorkflowStateEntry(status, *workflowEntry)
			}

			status.State = update.State
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

func commitStatusBackedEvent(runID string, run *Run, event Event, updateStatus func(*Status, Event)) (Status, Event, error) {
	event = prepareRunEvent(runID, run, event)
	status := run.Status
	updateStatus(&status, event)

	if err := appendEvent(filepath.Join(run.Path, eventsName), event); err != nil {
		return status, event, fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}

	if err := writeStatusForRun(runID, run.Path, status); err != nil {
		return status, event, err
	}

	return status, event, nil
}

func prepareRunEvent(runID string, run *Run, event Event) Event {
	event.SchemaVersion = schemaVersion
	event.Sequence = nextEventSequence(run)
	event.Time = normalizeTime(event.Time)
	event.RunID = runID

	return event
}

func nextEventSequence(run *Run) int {
	return run.Status.LastSequence + 1
}

func writeStatusForRun(runID, runPath string, status Status) error {
	statusPath := filepath.Join(runPath, statusName)
	if err := writeStatus(statusPath, status); err != nil {
		return &StatusMaterializationError{RunID: runID, Path: statusPath, Err: err}
	}

	return nil
}

func statusMaterializationFailed(err error) bool {
	var materializationErr *StatusMaterializationError
	return errors.As(err, &materializationErr)
}

func statusBackedEventPossiblyCommitted(err error) bool {
	return eventAppendPossiblyCommitted(err) || statusMaterializationFailed(err)
}

func reservedEventType(eventType string) bool {
	switch eventType {
	case eventRunCreated, eventStatusUpdated, eventArtifactWritten, eventAttemptStarted, eventAttemptPrompted, eventAttemptLogged, eventAttemptProcess, eventAttemptFinished, eventAttemptRecovered, eventAttemptReported, eventAttemptWarning, eventReportIgnored, eventRunContinued, eventWorkflowSoftCap, eventWorkflowHardCap, eventWorkflowHardCapOverride, eventWorkflowStepSkipped, EventConfigSnapshotRefreshed:
		return true
	default:
		return false
	}
}

func marshalPayload(payload any) (json.RawMessage, error) {
	content, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}

	return json.RawMessage(content), nil
}

func writeInitialEventLog(path string, event Event) error {
	content, err := marshalEventLine(event)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, runFilePerm) // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return fmt.Errorf("write initial event log: %w", err)
	}

	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write initial event log: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("write initial event log: %w", err)
	}

	return nil
}

func appendEvent(path string, event Event) error {
	if err := validateRegularFile(path, eventsName); err != nil {
		return err
	}

	content, err := marshalEventLine(event)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, runFilePerm) // #nosec G304,G703 -- path is scoped to the run directory.
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}

	return writeEventContent(path, file, content)
}

func writeEventContent(path string, writer io.WriteCloser, content []byte) error {
	if _, err := writer.Write(content); err != nil {
		_ = writer.Close()
		return &EventAppendError{Path: path, PossiblyAppended: true, Err: err}
	}

	if err := writer.Close(); err != nil {
		return &EventAppendError{Path: path, PossiblyAppended: true, Err: err}
	}

	return nil
}

func marshalEventLine(event Event) ([]byte, error) {
	content, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal event line: %w", err)
	}

	return append(content, '\n'), nil
}

func eventAppendPossiblyCommitted(err error) bool {
	var appendErr *EventAppendError
	return errors.As(err, &appendErr) && appendErr.PossiblyAppended
}

func writeStatus(path string, status Status) error {
	content, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	content = append(content, '\n')

	return writeAtomic(path, content)
}

func readStatus(path string) (Status, error) {
	if err := validateRegularFile(path, statusName); err != nil {
		return Status{}, err
	}

	content, err := os.ReadFile(path) // #nosec G304,G703 -- path is scoped to the run directory.
	if err != nil {
		return Status{}, fmt.Errorf("read status: %w", err)
	}

	var status Status
	if err := json.Unmarshal(content, &status); err != nil {
		return Status{}, fmt.Errorf("read status: %w", err)
	}

	if status.SchemaVersion != schemaVersion {
		return Status{}, stableerr.Errorf("unsupported schema_version %d", status.SchemaVersion)
	}

	if status.RunID == "" {
		return Status{}, stableerr.New("run_id is required")
	}

	if status.Workflow == "" {
		return Status{}, stableerr.New("workflow is required")
	}

	if status.CreatedAt.IsZero() {
		return Status{}, stableerr.New("created_at is required")
	}

	if status.UpdatedAt.IsZero() {
		return Status{}, stableerr.New("updated_at is required")
	}

	return status, nil
}
