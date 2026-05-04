package runstore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	runLocks              sync.Map
	runLockWaitObserverMu sync.Mutex
	runLockWaitObserver   func(string)
)

type contextRunLock struct {
	ch chan struct{}
}

func newContextRunLock() *contextRunLock {
	return &contextRunLock{ch: make(chan struct{}, 1)}
}

func (l *contextRunLock) lock(ctx context.Context, lockName string) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	default:
		observeRunLockWait(lockName)
		select {
		case l.ch <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *contextRunLock) unlock() {
	select {
	case <-l.ch:
	default:
	}
}

// SetRunLockWaitObserverForTest installs a test observer called when a run-lock
// wait path observes contention. It returns a cleanup function.
func SetRunLockWaitObserverForTest(observer func(string)) func() {
	runLockWaitObserverMu.Lock()
	previous := runLockWaitObserver
	runLockWaitObserver = observer
	runLockWaitObserverMu.Unlock()
	return func() {
		runLockWaitObserverMu.Lock()
		runLockWaitObserver = previous
		runLockWaitObserverMu.Unlock()
	}
}

func observeRunLockWait(lockName string) {
	runLockWaitObserverMu.Lock()
	observer := runLockWaitObserver
	runLockWaitObserverMu.Unlock()
	if observer != nil {
		observer(lockName)
	}
}

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
	if err := ensureRunsDir(s.orcDir, s.runsDir); err != nil {
		return nil, fmt.Errorf("create runs directory: %w", err)
	}
	runDir := s.runDir(runID)
	tempDir, err := os.MkdirTemp(s.runsDir, "."+runID+".tmp-")
	if err != nil {
		return nil, fmt.Errorf("create temporary run directory %q: %w", runID, err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()
	for _, dir := range artifactDirs() {
		if err := os.Mkdir(filepath.Join(tempDir, dir), 0o750); err != nil {
			return nil, fmt.Errorf("create run %q artifact directory %s: %w", runID, dir, err)
		}
	}
	if err := writeAtomic(filepath.Join(tempDir, followupsName), nil); err != nil {
		return nil, fmt.Errorf("create run %q followups.md: %w", runID, err)
	}
	// Materialize the final per-run lock file in the temporary layout before
	// publication; the runs-directory lock below owns publication itself.
	lockFile, err := os.OpenFile(filepath.Join(tempDir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to a newly created temporary run directory.
	if err != nil {
		return nil, fmt.Errorf("create run %q lock: %w", runID, err)
	}
	if err := lockFile.Close(); err != nil {
		return nil, fmt.Errorf("create run %q lock: %w", runID, err)
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
	if err := writeInitialEventLog(filepath.Join(tempDir, eventsName), event); err != nil {
		return nil, fmt.Errorf("run %q events: %w", runID, err)
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
		Attempts:      []Attempt{},
	}
	if err := writeStatus(filepath.Join(tempDir, statusName), status); err != nil {
		return nil, fmt.Errorf("run %q status: %w", runID, err)
	}
	unlockRuns, err := s.lockRunsDir(context.Background())
	if err != nil {
		return nil, fmt.Errorf("lock runs directory: %w", err)
	}
	defer func() {
		if unlockRuns != nil {
			unlockRuns()
		}
	}()
	reservation, err := reserveRunDir(runDir)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if generated {
				unlockRuns()
				unlockRuns = nil
				return s.Create(req)
			}
			return nil, fmt.Errorf("run %q already exists", runID)
		}
		return nil, fmt.Errorf("reserve run directory %q: %w", runID, err)
	}
	defer reservation.cleanup()
	if err := publishReservedRunDir(tempDir, reservation); err != nil {
		return nil, fmt.Errorf("publish run directory %q: %w", runID, err)
	}
	reservation.published = true
	cleanupTemp = false
	return &Run{
		ID:     runID,
		Path:   runDir,
		Status: status,
		Events: []Event{event},
	}, nil
}

type runDirReservation struct {
	path      string
	file      *os.File
	published bool
}

func reserveRunDir(runDir string) (*runDirReservation, error) {
	if err := os.Mkdir(runDir, 0o750); err != nil {
		return nil, err
	}
	reservation := &runDirReservation{path: runDir}
	file, err := os.OpenFile(filepath.Join(runDir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to an atomically reserved run directory.
	if err != nil {
		reservation.cleanup()
		return nil, err
	}
	reservation.file = file
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		reservation.cleanup()
		return nil, err
	}
	return reservation, nil
}

func publishReservedRunDir(tempDir string, reservation *runDirReservation) error {
	if reservation == nil {
		return errors.New("run directory reservation is required")
	}
	if err := os.Remove(filepath.Join(tempDir, ".lock")); err != nil {
		return err
	}
	for _, name := range append(artifactDirs(), followupsName, eventsName, statusName) {
		if err := os.Rename(filepath.Join(tempDir, name), filepath.Join(reservation.path, name)); err != nil {
			return err
		}
	}
	return os.Remove(tempDir)
}

func (r *runDirReservation) cleanup() {
	if r == nil {
		return
	}
	if r.file != nil {
		fd := int(r.file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = r.file.Close()
		r.file = nil
	}
	if !r.published {
		_ = os.RemoveAll(r.path)
	}
}

// Load recovers structured run state from an existing run directory.
func (s *Store) Load(runID string) (*Run, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	var run *Run
	err := s.withRunLock(runID, func() error {
		loaded, err := s.load(runID)
		if err != nil {
			return err
		}
		run = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
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

func (s *Store) lockRunsDir(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := validateRunsDir(s.orcDir, s.runsDir); err != nil {
		return nil, fmt.Errorf("runs directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(s.runsDir, ".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to the validated runs directory.
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := flockExclusiveContext(ctx, fd, "runs-directory"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func flockExclusiveContext(ctx context.Context, fd int, lockName string) error {
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		observeRunLockWait(lockName)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// AppendEvent appends a caller event and advances status metadata.
func (s *Store) AppendEvent(runID string, event Event) (Event, error) {
	if err := validateRunID(runID); err != nil {
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
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if update.State == "" {
		return Status{}, Event{}, errors.New("state is required")
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
	var status Status
	err = s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt != nil && update.State != stateRunning {
			return fmt.Errorf("run %q has active attempt %q; state update to %q is not allowed", runID, run.Status.ActiveAttempt.AttemptID, update.State)
		}
		var committedEvent Event
		status, committedEvent, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
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

// WriteArtifact persists an artifact under the run directory and records it in the event log.
func (s *Store) WriteArtifact(runID string, artifact Artifact) (ArtifactRef, error) {
	if err := validateRunID(runID); err != nil {
		return ArtifactRef{}, err
	}
	var ref ArtifactRef
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		sequence := nextEventSequence(run)
		relPath, err := artifactPath(artifact.Kind, artifact.Name, sequence)
		if err != nil {
			return err
		}
		if err := validateRelativeArtifactPath(relPath); err != nil {
			return err
		}
		if err := validateArtifactWriteAllowed(run.Status.Artifacts, artifact.Kind, relPath); err != nil {
			return err
		}
		ref = ArtifactRef{
			Kind:          artifact.Kind,
			Path:          relPath,
			Name:          artifact.Name,
			EventSequence: sequence,
		}
		path := filepath.Join(run.Path, filepath.FromSlash(relPath))
		if err := ensureArtifactParentDir(run.Path, relPath); err != nil {
			return fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
		}
		staged, err := stageArtifact(path, artifact)
		if err != nil {
			return fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
		}
		defer staged.cleanup()
		payload, err := marshalPayload(artifactWrittenPayload{Artifact: ref})
		if err != nil {
			return err
		}
		event := prepareRunEvent(runID, run, Event{
			Time:    artifact.Time,
			Type:    eventArtifactWritten,
			Payload: payload,
		})
		if err := staged.commit(); err != nil {
			return fmt.Errorf("run %q artifact %s: %w", runID, relPath, err)
		}
		if err := appendEvent(filepath.Join(run.Path, eventsName), event); err != nil {
			if eventAppendPossiblyCommitted(err) {
				return fmt.Errorf("run %q events.jsonl: %w", runID, err)
			}
			if rollbackErr := staged.rollback(); rollbackErr != nil {
				return fmt.Errorf("run %q events.jsonl: %w", runID, errors.Join(err, fmt.Errorf("rollback artifact %s: %w", relPath, rollbackErr)))
			}
			ref = ArtifactRef{}
			return fmt.Errorf("run %q events.jsonl: %w", runID, err)
		}
		status := run.Status
		status.Artifacts = append(status.Artifacts, ref)
		status.UpdatedAt = event.Time
		status.LastSequence = event.Sequence
		if err := writeStatusForRun(runID, run.Path, status); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ref, err
	}
	return ref, nil
}

// StartAttempt records a new starting worker attempt for a running run.
func (s *Store) StartAttempt(runID string, req StartAttemptRequest) (Attempt, Event, error) {
	return s.StartAttemptContext(context.Background(), runID, req)
}

// StartAttemptContext records a new starting worker attempt for a running run unless ctx is canceled before the attempt commits.
func (s *Store) StartAttemptContext(ctx context.Context, runID string, req StartAttemptRequest) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, errors.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, err
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
			return err
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.State != stateRunning {
			return fmt.Errorf("run %q state is %q, want %q to start attempt", runID, run.Status.State, stateRunning)
		}
		if run.Status.ActiveAttempt != nil {
			return fmt.Errorf("run %q already has active attempt %q", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if slices.ContainsFunc(run.Status.Attempts, func(existing Attempt) bool {
			return existing.AttemptID == attempt.AttemptID
		}) {
			return fmt.Errorf("run %q already has attempt %q", runID, attempt.AttemptID)
		}
		if pending, ok := PendingLauncherOutcome(run.Status); ok {
			return fmt.Errorf("run %q has pending worker outcome %s/%s for attempt %q", runID, pending.Status, pending.Result, pending.AttemptID)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, err := marshalPayload(attemptStartedPayload{Attempt: attempt})
		if err != nil {
			return err
		}
		event = Event{Time: req.Time, Type: eventAttemptStarted, Payload: payload}
		status, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			attempt.StartedAt = event.Time
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
	payload := attemptPromptedPayload{
		AttemptID: req.AttemptID,
		PromptRef: req.PromptRef,
	}
	return s.updateActiveAttempt(runID, req.AttemptID, req.Time, eventAttemptPrompted, func(status Status, attempt *Attempt) (any, error) {
		if err := applyAttemptPromptRef(status, attempt, req.AttemptID, req.PromptRef); err != nil {
			return nil, err
		}
		return payload, nil
	})
}

// RecordAttemptLog links a log artifact to the current active attempt.
func (s *Store) RecordAttemptLog(runID string, req AttemptLogRequest) (Attempt, Event, error) {
	payload := attemptLoggedPayload{
		AttemptID: req.AttemptID,
		LogRef:    req.LogRef,
	}
	return s.updateActiveAttempt(runID, req.AttemptID, req.Time, eventAttemptLogged, func(status Status, attempt *Attempt) (any, error) {
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
		return Attempt{}, Event{}, errors.New("context is required")
	}
	if req.PID <= 0 {
		return Attempt{}, Event{}, errors.New("process id must be > 0")
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
	return s.terminalizeAttempt(context.Background(), runID, req, eventAttemptRecovered, true)
}

func (s *Store) terminalizeAttempt(ctx context.Context, runID string, req FinishAttemptRequest, eventType string, recovered bool) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, errors.New("context is required")
	}
	if err := validateFinishedAttempt(req); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, err
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
		return fmt.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.PromptRef != nil {
		return fmt.Errorf("attempt %q already has prompt ref", attemptID)
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
		return fmt.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.LogRef != nil {
		return fmt.Errorf("attempt %q already has log ref", attemptID)
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
		return errors.New("process id must be > 0")
	}
	if attempt.State != AttemptStateStarting {
		return fmt.Errorf("attempt %q state %q, want starting", attemptID, attempt.State)
	}
	if attempt.PID != 0 {
		return fmt.Errorf("attempt %q already has process metadata", attemptID)
	}
	if attempt.PromptRef == nil {
		return fmt.Errorf("attempt %q prompt ref is required before process start", attemptID)
	}
	if attempt.LogRef == nil {
		return fmt.Errorf("attempt %q log ref is required before process start", attemptID)
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

// ReadArtifact reads a persisted artifact through a validated run-store path.
func (s *Store) ReadArtifact(runID string, ref ArtifactRef) ([]byte, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	var content []byte
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		path, err := resolveRecordedArtifactPath(run, ref)
		if err != nil {
			return err
		}
		content, err = os.ReadFile(path) // #nosec G304,G703 -- path is scoped to the validated run directory.
		return err
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

// OpenArtifactAppend opens a recorded artifact for append through a validated run-store path.
func (s *Store) OpenArtifactAppend(runID string, ref ArtifactRef) (*os.File, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	if ref.Kind != KindLog {
		return nil, fmt.Errorf("artifact %s kind %q, want %q", ref.Path, ref.Kind, KindLog)
	}
	var file *os.File
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		path, err := resolveRecordedArtifactPath(run, ref)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt == nil || run.Status.ActiveAttempt.LogRef == nil || *run.Status.ActiveAttempt.LogRef != ref {
			return fmt.Errorf("artifact %s is not the current active attempt log for run %q", ref.Path, runID)
		}
		opened, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600) // #nosec G304,G703 -- path is scoped to the validated run directory and opened without following final-component symlinks.
		if err != nil {
			return err
		}
		info, err := opened.Stat()
		if err != nil {
			_ = opened.Close()
			return err
		}
		if err := validateFileInfo("artifact "+ref.Path, info); err != nil {
			_ = opened.Close()
			return err
		}
		file = opened
		return nil
	})
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, err
	}
	return file, nil
}

func resolveRecordedArtifactPath(run *Run, ref ArtifactRef) (string, error) {
	if err := validateArtifactRef(ref, 0); err != nil {
		return "", err
	}
	if !slices.Contains(run.Status.Artifacts, ref) {
		return "", fmt.Errorf("artifact %s is not recorded for run %q", ref.Path, run.ID)
	}
	if err := validateArtifactParentDir(run.Path, ref.Path); err != nil {
		return "", err
	}
	path := filepath.Join(run.Path, filepath.FromSlash(ref.Path))
	if err := validateRegularFile(path, "artifact "+ref.Path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) updateActiveAttempt(runID, attemptID string, at time.Time, eventType string, apply func(Status, *Attempt) (any, error)) (Attempt, Event, error) {
	return s.updateActiveAttemptContext(context.Background(), runID, attemptID, at, eventType, apply)
}

func (s *Store) updateActiveAttemptContext(ctx context.Context, runID, attemptID string, at time.Time, eventType string, apply func(Status, *Attempt) (any, error)) (Attempt, Event, error) {
	if ctx == nil {
		return Attempt{}, Event{}, errors.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return Attempt{}, Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, Event{}, err
	}
	at = normalizeTime(at)
	if attemptID == "" {
		return Attempt{}, Event{}, errors.New("attempt id is required")
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
			return fmt.Errorf("run %q has no active attempt", runID)
		}
		if run.Status.ActiveAttempt.AttemptID != attemptID {
			return fmt.Errorf("run %q active attempt is %q, not %q", runID, run.Status.ActiveAttempt.AttemptID, attemptID)
		}
		attempt := *run.Status.ActiveAttempt
		payload, err := apply(run.Status, &attempt)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		content, err := marshalPayload(payload)
		if err != nil {
			return err
		}
		event = Event{Time: at, Type: eventType, Payload: content}
		status, committedEvent, err := commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			if event.Type == eventAttemptFinished || event.Type == eventAttemptRecovered {
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
		return Attempt{}, errors.New("step id is required")
	case req.AgentID == "":
		return Attempt{}, errors.New("agent id is required")
	case req.AttemptID == "":
		return Attempt{}, errors.New("attempt id is required")
	case req.Timeout <= 0:
		return Attempt{}, errors.New("timeout must be > 0")
	case req.ReportExitGrace <= 0:
		return Attempt{}, errors.New("report exit grace must be > 0")
	}
	return Attempt{
		RunID:           runID,
		StepID:          req.StepID,
		AgentID:         req.AgentID,
		AttemptID:       req.AttemptID,
		State:           AttemptStateStarting,
		Timeout:         req.Timeout.String(),
		ReportExitGrace: req.ReportExitGrace.String(),
		StartedAt:       normalizeTime(req.Time),
	}, nil
}

func validateFinishedAttempt(req FinishAttemptRequest) error {
	if req.AttemptID == "" {
		return errors.New("attempt id is required")
	}
	return validateTerminalAttemptOutcomeFields(req.State, req.Status, req.Result, "attempt")
}

func validateTerminalAttemptOutcomeFields(state, status, result, subject string) error {
	if state == "" || status == "" || result == "" {
		return fmt.Errorf("%s state/status/result are required", subject)
	}
	if !terminalAttemptState(state) {
		return fmt.Errorf("%s state %q is not terminal", subject, state)
	}
	if !validTerminalAttemptOutcome(state, status, result) {
		return fmt.Errorf("%s terminal outcome %s/%s with state %q is invalid", subject, status, result, state)
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
		return fmt.Errorf("attempt %q has no process metadata; terminal state %q is not allowed before process start", attempt.AttemptID, terminalState)
	}
	return nil
}

func terminalAttemptState(state string) bool {
	switch state {
	case AttemptStateMissingReport, AttemptStateProcessError, AttemptStateTimedOut:
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

// PendingLauncherOutcome returns the latest launcher-synthesized terminal
// attempt that has not yet been consumed by report/retry routing.
func PendingLauncherOutcome(status Status) (Attempt, bool) {
	if status.State != stateRunning || status.ActiveAttempt != nil || len(status.Attempts) == 0 {
		return Attempt{}, false
	}
	attempt := status.Attempts[len(status.Attempts)-1]
	if !validTerminalAttemptOutcome(attempt.State, attempt.Status, attempt.Result) {
		return Attempt{}, false
	}
	return attempt, true
}

func (s *Store) withRunLock(runID string, fn func() error) error {
	return s.withRunLockContext(context.Background(), runID, fn)
}

func (s *Store) withRunLockContext(ctx context.Context, runID string, fn func() error) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlockRuns, err := s.lockRunsDir(ctx)
	if err != nil {
		return err
	}
	runsReleased := false
	releaseRuns := func() {
		if !runsReleased {
			unlockRuns()
			runsReleased = true
		}
	}
	defer releaseRuns()
	runDir := s.runDir(runID)
	if err := validateDir(runDir); err != nil {
		return fmt.Errorf("run %q directory: %w", runID, err)
	}
	localLockValue, _ := runLocks.LoadOrStore(runDir, newContextRunLock())
	localLock, ok := localLockValue.(*contextRunLock)
	if !ok {
		return fmt.Errorf("run %q lock has unexpected type %T", runID, localLockValue)
	}
	if err := localLock.lock(ctx, runID); err != nil {
		return err
	}
	defer localLock.unlock()
	lockPath := filepath.Join(runDir, ".lock")
	if err := validateRunLockFile(lockPath); err != nil {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to a validated run directory and O_NOFOLLOW rejects symlinks.
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	if info, err := file.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("run %q lock is not a regular file", runID)
	}
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := flockExclusiveContext(ctx, fd, runID); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseRuns()
	return fn()
}

func validateRunLockFile(lockPath string) error {
	info, err := os.Lstat(lockPath) // #nosec G703 -- lock path is scoped to a validated run directory.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateFileInfo("run lock", info)
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
	case eventRunCreated, eventStatusUpdated, eventArtifactWritten, eventAttemptStarted, eventAttemptPrompted, eventAttemptLogged, eventAttemptProcess, eventAttemptFinished, eventAttemptRecovered:
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304,G703 -- path is scoped to the run directory.
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
	content, err := os.ReadFile(path) // #nosec G304,G703 -- path is scoped to the run directory.
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
	file, err := os.Open(path) // #nosec G304,G703 -- path is scoped to the run directory.
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
		Attempts:      []Attempt{},
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
			if replayed.ActiveAttempt != nil && payload.State != stateRunning {
				return Status{}, fmt.Errorf("event %d updates run state to %q while attempt %q is active", event.Sequence, payload.State, replayed.ActiveAttempt.AttemptID)
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
		case eventAttemptStarted:
			var payload attemptStartedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.started payload: %w", event.Sequence, err)
			}
			if replayed.State != stateRunning {
				return Status{}, fmt.Errorf("event %d starts attempt while run state is %q, want %q", event.Sequence, replayed.State, stateRunning)
			}
			if replayed.ActiveAttempt != nil {
				return Status{}, fmt.Errorf("event %d starts attempt %q while attempt %q is active", event.Sequence, payload.Attempt.AttemptID, replayed.ActiveAttempt.AttemptID)
			}
			if pending, ok := PendingLauncherOutcome(replayed); ok {
				return Status{}, fmt.Errorf("event %d starts attempt while pending worker outcome %s/%s for attempt %q is unconsumed", event.Sequence, pending.Status, pending.Result, pending.AttemptID)
			}
			attempt := payload.Attempt
			if err := validateStartedAttemptEvent(event, attempt, replayed.RunID); err != nil {
				return Status{}, err
			}
			if slices.ContainsFunc(replayed.Attempts, func(existing Attempt) bool {
				return existing.AttemptID == attempt.AttemptID
			}) {
				return Status{}, fmt.Errorf("event %d duplicate attempt %q", event.Sequence, attempt.AttemptID)
			}
			replayed.ActiveAttempt = &attempt
			replayed.Attempts = append(replayed.Attempts, attempt)
		case eventAttemptPrompted:
			var payload attemptPromptedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.prompted payload: %w", event.Sequence, err)
			}
			if err := updateReplayedActiveAttempt(&replayed, event, payload.AttemptID, func(attempt *Attempt) error {
				return applyAttemptPromptRef(replayed, attempt, payload.AttemptID, payload.PromptRef)
			}); err != nil {
				return Status{}, err
			}
		case eventAttemptLogged:
			var payload attemptLoggedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.logged payload: %w", event.Sequence, err)
			}
			if err := updateReplayedActiveAttempt(&replayed, event, payload.AttemptID, func(attempt *Attempt) error {
				return applyAttemptLogRef(replayed, attempt, payload.AttemptID, payload.LogRef)
			}); err != nil {
				return Status{}, err
			}
		case eventAttemptProcess:
			var payload attemptProcessPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.process_started payload: %w", event.Sequence, err)
			}
			if err := updateReplayedActiveAttempt(&replayed, event, payload.AttemptID, func(attempt *Attempt) error {
				return applyAttemptProcessMetadata(attempt, payload.AttemptID, payload.PID, payload.ProcessStartTime)
			}); err != nil {
				return Status{}, err
			}
		case eventAttemptFinished, eventAttemptRecovered:
			var payload attemptFinishedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d %s payload: %w", event.Sequence, event.Type, err)
			}
			if err := finishReplayedActiveAttempt(&replayed, event, payload, event.Type == eventAttemptRecovered); err != nil {
				return Status{}, err
			}
		}
	}
	return replayed, nil
}

func validateStartedAttemptEvent(event Event, attempt Attempt, runID string) error {
	switch {
	case attempt.RunID != runID:
		return fmt.Errorf("event %d attempt run_id %q does not match", event.Sequence, attempt.RunID)
	case attempt.StepID == "":
		return fmt.Errorf("event %d attempt step_id is required", event.Sequence)
	case attempt.AgentID == "":
		return fmt.Errorf("event %d attempt agent_id is required", event.Sequence)
	case attempt.AttemptID == "":
		return fmt.Errorf("event %d attempt attempt_id is required", event.Sequence)
	case attempt.State != AttemptStateStarting:
		return fmt.Errorf("event %d attempt state %q, want starting", event.Sequence, attempt.State)
	case attempt.Status != "":
		return fmt.Errorf("event %d attempt status must be empty for starting attempt", event.Sequence)
	case attempt.Result != "":
		return fmt.Errorf("event %d attempt result must be empty for starting attempt", event.Sequence)
	case attempt.PID != 0:
		return fmt.Errorf("event %d attempt pid must be empty for starting attempt", event.Sequence)
	case attempt.ProcessStartTime != "":
		return fmt.Errorf("event %d attempt process_start_time must be empty for starting attempt", event.Sequence)
	case attempt.ExitCode != nil:
		return fmt.Errorf("event %d attempt exit_code must be empty for starting attempt", event.Sequence)
	case attempt.ExitState != "":
		return fmt.Errorf("event %d attempt exit_state must be empty for starting attempt", event.Sequence)
	case attempt.PromptRef != nil:
		return fmt.Errorf("event %d attempt prompt_ref must be empty for starting attempt", event.Sequence)
	case attempt.LogRef != nil:
		return fmt.Errorf("event %d attempt log_ref must be empty for starting attempt", event.Sequence)
	case attempt.FinishedAt != nil:
		return fmt.Errorf("event %d attempt finished_at must be empty for starting attempt", event.Sequence)
	case attempt.Recovered:
		return fmt.Errorf("event %d attempt recovered must be false for starting attempt", event.Sequence)
	case attempt.Timeout == "":
		return fmt.Errorf("event %d attempt timeout is required", event.Sequence)
	case attempt.ReportExitGrace == "":
		return fmt.Errorf("event %d attempt report_exit_grace is required", event.Sequence)
	case !attempt.StartedAt.Equal(event.Time):
		return fmt.Errorf("event %d attempt started_at does not match event time", event.Sequence)
	default:
		timeout, err := time.ParseDuration(attempt.Timeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("event %d attempt timeout must be > 0", event.Sequence)
		}
		grace, err := time.ParseDuration(attempt.ReportExitGrace)
		if err != nil || grace <= 0 {
			return fmt.Errorf("event %d attempt report_exit_grace must be > 0", event.Sequence)
		}
		return nil
	}
}

func validateProcessStartTime(value string) error {
	if value == "" {
		return errors.New("process_start_time is required")
	}
	if len(value) > 32 {
		return errors.New("process_start_time is too long")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("process_start_time %q must be decimal digits", value)
		}
	}
	return nil
}

func validateRecordedArtifactRef(status Status, ref ArtifactRef, kind ArtifactKind) error {
	if err := validateArtifactRef(ref, 0); err != nil {
		return err
	}
	if ref.Kind != kind {
		return fmt.Errorf("artifact %s kind %q, want %q", ref.Path, ref.Kind, kind)
	}
	if !slices.Contains(status.Artifacts, ref) {
		return fmt.Errorf("artifact %s is not recorded for run %q", ref.Path, status.RunID)
	}
	return nil
}

func updateReplayedActiveAttempt(status *Status, event Event, attemptID string, update func(*Attempt) error) error {
	if attemptID == "" {
		return fmt.Errorf("event %d attempt_id is required", event.Sequence)
	}
	if status.ActiveAttempt == nil {
		return fmt.Errorf("event %d has no active attempt", event.Sequence)
	}
	if status.ActiveAttempt.AttemptID != attemptID {
		return fmt.Errorf("event %d targets attempt %q while %q is active", event.Sequence, attemptID, status.ActiveAttempt.AttemptID)
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
	return fmt.Errorf("event %d attempt %q not found in history", event.Sequence, attemptID)
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
