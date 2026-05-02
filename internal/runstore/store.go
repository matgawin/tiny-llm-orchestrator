package runstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Open returns a run store rooted at projectRoot.
func Open(projectRoot string) (*Store, error) {
	if projectRoot == "" {
		return nil, errors.New("project root is required")
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	return &Store{
		orcDir:  filepath.Join(root, orcDirName),
		runsDir: filepath.Join(root, orcDirName, runsDirName),
	}, nil
}

// Create creates a new run directory with an initial event and status file.
func (s *Store) Create(req CreateRunRequest) (*Run, error) {
	if req.Workflow == "" {
		return nil, errors.New("workflow is required")
	}
	now := normalizeTime(req.Time)
	runID := req.RunID
	generated := runID == ""
	if generated {
		var err error
		runID, err = generatedRunID(now, req.Workflow, req.TaskSlug)
		if err != nil {
			return nil, err
		}
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	runDir := s.runDir(runID)
	if err := ensureRunsDir(s.orcDir, s.runsDir); err != nil {
		return nil, fmt.Errorf("create runs directory: %w", err)
	}
	if err := os.Mkdir(runDir, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			if generated {
				return s.Create(req)
			}
			return nil, fmt.Errorf("run %q already exists", runID)
		}
		return nil, fmt.Errorf("create run directory %q: %w", runID, err)
	}
	for _, dir := range artifactDirs() {
		if err := os.Mkdir(filepath.Join(runDir, dir), 0o750); err != nil {
			return nil, cleanupCreatedRunDir(runID, runDir, fmt.Errorf("create run %q artifact directory %s: %w", runID, dir, err))
		}
	}
	if err := writeAtomic(filepath.Join(runDir, followupsName), nil); err != nil {
		return nil, cleanupCreatedRunDir(runID, runDir, fmt.Errorf("create run %q followups.md: %w", runID, err))
	}

	payload, err := marshalPayload(createRunPayload{Workflow: req.Workflow, TaskSlug: req.TaskSlug})
	if err != nil {
		return nil, err
	}
	event := Event{
		SchemaVersion: schemaVersion,
		Sequence:      1,
		Time:          now,
		RunID:         runID,
		Type:          eventRunCreated,
		Payload:       payload,
	}
	if err := writeInitialEventLog(filepath.Join(runDir, eventsName), event); err != nil {
		return nil, cleanupCreatedRunDir(runID, runDir, fmt.Errorf("run %q events: %w", runID, err))
	}
	status := Status{
		SchemaVersion: schemaVersion,
		RunID:         runID,
		Workflow:      req.Workflow,
		State:         stateRunning,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastSequence:  event.Sequence,
		Artifacts:     []ArtifactRef{},
	}
	if err := writeStatus(filepath.Join(runDir, statusName), status); err != nil {
		return nil, cleanupCreatedRunDir(runID, runDir, fmt.Errorf("run %q status: %w", runID, err))
	}
	return &Run{
		ID:     runID,
		Path:   runDir,
		Status: status,
		Events: []Event{event},
	}, nil
}

// Load recovers structured run state from an existing run directory.
func (s *Store) Load(runID string) (*Run, error) {
	return s.load(runID)
}

func (s *Store) load(runID string) (*Run, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	if err := validateRunsDir(s.orcDir, s.runsDir); err != nil {
		return nil, fmt.Errorf("runs directory: %w", err)
	}
	runDir := s.runDir(runID)
	if err := validateDir(runDir); err != nil {
		return nil, fmt.Errorf("run %q directory: %w", runID, err)
	}
	if err := validateRunLayout(runDir); err != nil {
		return nil, fmt.Errorf("run %q layout: %w", runID, err)
	}
	status, err := readStatus(filepath.Join(runDir, statusName))
	if err != nil {
		return nil, fmt.Errorf("run %q status.json: %w", runID, err)
	}
	if status.RunID != runID {
		return nil, fmt.Errorf("run %q status.json run_id %q does not match", runID, status.RunID)
	}
	events, err := readEvents(filepath.Join(runDir, eventsName), runID)
	if err != nil {
		return nil, fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("run %q events.jsonl: no events", runID)
	}
	replayedStatus, err := replayStatus(events, status)
	if err != nil {
		return nil, fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}
	if err := validateArtifacts(runDir, replayedStatus.Artifacts); err != nil {
		return nil, fmt.Errorf("run %q status.json: %w", runID, err)
	}
	return &Run{
		ID:     runID,
		Path:   runDir,
		Status: replayedStatus,
		Events: events,
	}, nil
}

// AppendEvent appends a caller event and advances status metadata.
func (s *Store) AppendEvent(runID string, event Event) (Event, error) {
	if err := validateRunID(runID); err != nil {
		return Event{}, err
	}
	run, err := s.load(runID)
	if err != nil {
		return Event{}, err
	}
	if event.Type == "" {
		return Event{}, errors.New("event type is required")
	}
	if reservedEventType(event.Type) {
		return Event{}, fmt.Errorf("event type %q is store-owned; use the dedicated runstore API", event.Type)
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	_, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
		status.UpdatedAt = event.Time
		status.LastSequence = event.Sequence
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
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if update.State == "" {
		return Status{}, Event{}, errors.New("state is required")
	}
	run, err := s.load(runID)
	if err != nil {
		return Status{}, Event{}, err
	}
	payload, err := marshalPayload(statusUpdatedPayload{State: update.State})
	if err != nil {
		return Status{}, Event{}, err
	}
	event := Event{
		Time:    update.Time,
		Type:    eventStatusUpdated,
		Payload: payload,
	}
	status, event, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
		status.State = update.State
		status.UpdatedAt = event.Time
		status.LastSequence = event.Sequence
	})
	if err != nil {
		if statusBackedEventPossiblyCommitted(err) {
			return status, event, err
		}
		return Status{}, Event{}, err
	}
	return status, event, nil
}

// WriteArtifact persists an artifact under the run directory and records it in the event log.
func (s *Store) WriteArtifact(runID string, artifact Artifact) (ArtifactRef, error) {
	if err := validateRunID(runID); err != nil {
		return ArtifactRef{}, err
	}
	run, err := s.load(runID)
	if err != nil {
		return ArtifactRef{}, err
	}
	sequence := nextEventSequence(run)
	relPath, err := artifactPath(artifact.Kind, artifact.Name, sequence)
	if err != nil {
		return ArtifactRef{}, err
	}
	if err := validateRelativeArtifactPath(relPath); err != nil {
		return ArtifactRef{}, err
	}
	if err := validateArtifactWriteAllowed(run.Status.Artifacts, artifact.Kind, relPath); err != nil {
		return ArtifactRef{}, err
	}
	ref := ArtifactRef{
		Kind:          artifact.Kind,
		Path:          relPath,
		Name:          artifact.Name,
		EventSequence: sequence,
	}
	path := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := ensureArtifactParentDir(run.Path, relPath); err != nil {
		return ArtifactRef{}, fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
	}
	staged, err := stageArtifact(path, artifact)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
	}
	defer staged.cleanup()
	payload, err := marshalPayload(artifactWrittenPayload{Artifact: ref})
	if err != nil {
		return ArtifactRef{}, err
	}
	event := prepareRunEvent(runID, run, Event{
		Time:    artifact.Time,
		Type:    eventArtifactWritten,
		Payload: payload,
	})
	if err := staged.commit(); err != nil {
		return ArtifactRef{}, fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
	}
	if err := appendEvent(filepath.Join(run.Path, eventsName), event); err != nil {
		if eventAppendPossiblyCommitted(err) {
			return ref, fmt.Errorf("run %q events.jsonl: %w", runID, err)
		}
		if rollbackErr := staged.rollback(); rollbackErr != nil {
			return ArtifactRef{}, fmt.Errorf("run %q events.jsonl: %w", runID, errors.Join(err, fmt.Errorf("rollback artifact %s: %w", relPath, rollbackErr)))
		}
		return ArtifactRef{}, fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}
	status := run.Status
	status.Artifacts = append(status.Artifacts, ref)
	status.UpdatedAt = event.Time
	status.LastSequence = event.Sequence
	if err := writeStatusForRun(runID, run.Path, status); err != nil {
		return ref, err
	}
	return ref, nil
}

func (s *Store) runDir(runID string) string {
	return filepath.Join(s.runsDir, runID)
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
	case eventRunCreated, eventStatusUpdated, eventArtifactWritten:
		return true
	default:
		return false
	}
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func appendEvent(path string, event Event) error {
	if err := validateRegularFile(path, eventsName); err != nil {
		return err
	}
	content, err := marshalEventLine(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return err
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
		return nil, err
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
		return err
	}
	content = append(content, '\n')
	return writeAtomic(path, content)
}

func readStatus(path string) (Status, error) {
	if err := validateRegularFile(path, statusName); err != nil {
		return Status{}, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(content, &status); err != nil {
		return Status{}, err
	}
	if status.SchemaVersion != schemaVersion {
		return Status{}, fmt.Errorf("unsupported schema_version %d", status.SchemaVersion)
	}
	if status.RunID == "" {
		return Status{}, errors.New("run_id is required")
	}
	if status.Workflow == "" {
		return Status{}, errors.New("workflow is required")
	}
	if status.CreatedAt.IsZero() {
		return Status{}, errors.New("created_at is required")
	}
	if status.UpdatedAt.IsZero() {
		return Status{}, errors.New("updated_at is required")
	}
	return status, nil
}

func readEvents(path, runID string) ([]Event, error) {
	if err := validateRegularFile(path, eventsName); err != nil {
		return nil, err
	}
	file, err := os.Open(path) // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return nil, err
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
			return nil, err
		}
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("line %d: missing trailing newline", line+1)
		}
		line++
		var event Event
		if err := json.Unmarshal(content, &event); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(event.Payload) == 0 {
			return nil, fmt.Errorf("line %d: payload is required", line)
		}
		if event.SchemaVersion != schemaVersion {
			return nil, fmt.Errorf("line %d: unsupported schema_version %d", line, event.SchemaVersion)
		}
		if event.RunID != runID {
			return nil, fmt.Errorf("line %d: run_id %q does not match", line, event.RunID)
		}
		if event.Sequence != line {
			return nil, fmt.Errorf("line %d: sequence %d is not ordered", line, event.Sequence)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("line %d: type is required", line)
		}
		if event.Time.IsZero() {
			return nil, fmt.Errorf("line %d: time is required", line)
		}
		events = append(events, event)
	}
	return events, nil
}

func validateArtifacts(runDir string, refs []ArtifactRef) error {
	for _, ref := range refs {
		if err := validateArtifactRef(ref, 0); err != nil {
			return fmt.Errorf("artifact %s: %w", ref.Path, err)
		}
		if err := validateArtifactParentDir(runDir, ref.Path); err != nil {
			return err
		}
		path := filepath.Join(runDir, filepath.FromSlash(ref.Path))
		if err := validateRegularFile(path, "artifact "+ref.Path); err != nil {
			return err
		}
	}
	return nil
}

func replayStatus(events []Event, status Status) (Status, error) {
	if err := validateRunCreated(events[0], status); err != nil {
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
	}
	for _, event := range events[1:] {
		replayed.UpdatedAt = event.Time
		switch event.Type {
		case eventRunCreated:
			return Status{}, fmt.Errorf("event %d duplicate %s event", event.Sequence, eventRunCreated)
		case eventStatusUpdated:
			var payload statusUpdatedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d status payload: %w", event.Sequence, err)
			}
			if payload.State == "" {
				return Status{}, fmt.Errorf("event %d status state is required", event.Sequence)
			}
			replayed.State = payload.State
		case eventArtifactWritten:
			var payload artifactWrittenPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d artifact payload: %w", event.Sequence, err)
			}
			if err := validateArtifactEventRef(event, payload.Artifact); err != nil {
				return Status{}, err
			}
			replayed.Artifacts = append(replayed.Artifacts, payload.Artifact)
		}
	}
	return replayed, nil
}

func validateRunCreated(event Event, status Status) error {
	if event.Sequence != 1 || event.Type != eventRunCreated {
		return fmt.Errorf("line 1: expected %s event", eventRunCreated)
	}
	var payload createRunPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("event 1 run.created payload: %w", err)
	}
	if payload.Workflow != status.Workflow {
		return fmt.Errorf("event 1 workflow %q does not match status.json workflow %q", payload.Workflow, status.Workflow)
	}
	return nil
}

func validateArtifactEventRef(event Event, ref ArtifactRef) error {
	if err := validateArtifactRef(ref, event.Sequence); err != nil {
		return fmt.Errorf("event %d artifact %s: %w", event.Sequence, ref.Path, err)
	}
	return nil
}

func validateArtifactRef(ref ArtifactRef, eventSequence int) error {
	if err := validateRelativeArtifactPath(ref.Path); err != nil {
		return err
	}
	if eventSequence > 0 && ref.EventSequence != eventSequence {
		return fmt.Errorf("event_sequence %d does not match", ref.EventSequence)
	}
	if err := validateArtifactPathForKind(ref); err != nil {
		return err
	}
	return nil
}

func validateArtifactPathForKind(ref ArtifactRef) error {
	spec, ok := artifactSpec(ref.Kind)
	if !ok {
		return fmt.Errorf("unsupported artifact kind %q", ref.Kind)
	}
	if spec.fixedPath != "" {
		if ref.Path != spec.fixedPath {
			return fmt.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
		}
		return nil
	}
	return validateNumberedArtifactPath(ref, spec.dir, spec.ext)
}

func validateArtifactWriteAllowed(existing []ArtifactRef, kind ArtifactKind, path string) error {
	switch kind {
	case KindTaskContext, KindTaskSnapshot:
		for _, ref := range existing {
			if ref.Kind == kind && ref.Path == path {
				return fmt.Errorf("artifact %s for kind %q has already been written", path, kind)
			}
		}
	}
	return nil
}

func validateNumberedArtifactPath(ref ArtifactRef, dir, ext string) error {
	prefix := fmt.Sprintf("%s/%06d-", dir, ref.EventSequence)
	if !strings.HasPrefix(ref.Path, prefix) || !strings.HasSuffix(ref.Path, ext) {
		return fmt.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
	}
	name := strings.TrimPrefix(ref.Path, dir+"/")
	if strings.Contains(name, "/") {
		return fmt.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
	}
	return nil
}
