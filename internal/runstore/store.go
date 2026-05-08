package runstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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

	status := Status{
		SchemaVersion: schemaVersion,
		RunID:         runID,
		Workflow:      req.Workflow,
		State:         stateRunning,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastSequence:  1,
		Artifacts:     []ArtifactRef{},
		Attempts:      []Attempt{},
		Warnings:      []AttemptWarning{},
	}
	var workflowEntry *WorkflowStateEntry
	if req.InitialState != "" {
		entry, err := nextWorkflowStateEntry(status, WorkflowStateEntryRequest{State: req.InitialState})
		if err != nil {
			return nil, err
		}
		workflowEntry = &entry
		applyWorkflowStateEntry(&status, entry)
	}
	payload, err := marshalPayload(createRunPayload{
		Workflow:           req.Workflow,
		TaskSlug:           req.TaskSlug,
		WorkflowStateEntry: workflowEntry,
	})
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
	status.LastSequence = event.Sequence
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
	event := Event{
		Time: update.Time,
		Type: eventStatusUpdated,
	}
	var status Status
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt != nil && update.State != stateRunning {
			return fmt.Errorf("run %q has active attempt %q; state update to %q is not allowed", runID, run.Status.ActiveAttempt.AttemptID, update.State)
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

// RecordWorkflowLoopSoftCap records the first advisory soft-cap hit for a workflow state.
func (s *Store) RecordWorkflowLoopSoftCap(runID string, loopCap WorkflowLoopSoftCap, at time.Time) (Status, Event, error) {
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if err := validateWorkflowLoopSoftCap(loopCap); err != nil {
		return Status{}, Event{}, err
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLock(runID, func() error {
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
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if err := validateWorkflowLoopHardCap(loopCap); err != nil {
		return Status{}, Event{}, err
	}
	at = normalizeTime(at)
	var status Status
	var event Event
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if run.Status.ActiveAttempt != nil {
			return fmt.Errorf("run %q has active attempt %q; loop hard-cap block is not allowed", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if run.Status.State != stateRunning {
			return fmt.Errorf("run %q state is %q, want %q for loop hard-cap block", runID, run.Status.State, stateRunning)
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
		return Status{}, Event{}, errors.New("workflow loop hard-cap override human action is required")
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
			return fmt.Errorf("run %q has no active workflow loop hard-cap block; state is %q", runID, run.Status.State)
		}
		block := run.Status.WorkflowLoop.HardCapBlock
		if block == nil {
			return fmt.Errorf("run %q has no active workflow loop hard-cap block", runID)
		}
		if run.Status.WorkflowLoop.PendingHardCapOverride != nil {
			return fmt.Errorf("run %q already has a pending workflow loop hard-cap override", runID)
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
	if err := validateRunID(runID); err != nil {
		return Status{}, Event{}, err
	}
	if validate == nil {
		return Status{}, Event{}, errors.New("step skip validator is required")
	}
	req.StepID = strings.TrimSpace(req.StepID)
	if req.StepID == "" {
		return Status{}, Event{}, errors.New("step id is required")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return Status{}, Event{}, errors.New("skip reason is required")
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Time = normalizeTime(req.Time)
	var status Status
	var event Event
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		transition, err := validate(run.Status)
		if err != nil {
			return err
		}
		if transition.State == "" {
			return errors.New("step skip transition state is required")
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
			Status:             "done",
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
				Status:        "done",
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
		return Status{}, Event{}, errors.New("--reason is required for --resolve-block and must be non-empty after trimming")
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
			return fmt.Errorf("run %q is blocked by a workflow-loop hard cap; next action is orc run continue %s --allow-loop-cap after human review", runID, runID)
		}
		if run.Status.ActiveAttempt != nil {
			return fmt.Errorf("run %q has active attempt %q; wait, recover, or inspect before continuing", runID, run.Status.ActiveAttempt.AttemptID)
		}
		if run.Status.State != stateBlockedHuman {
			return fmt.Errorf("run %q state is %q; run is not in a resumable blocked state; inspect the run or start a new workflow as appropriate", runID, run.Status.State)
		}
		attempt, ok := latestResolvableBlockedAttempt(run.Status)
		if !ok {
			return fmt.Errorf("run %q has no terminal blocked attempt that can be resolved; inspect the run or start a new workflow", runID)
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

// WriteArtifact persists an artifact under the run directory and records it in the event log.
func (s *Store) WriteArtifact(runID string, artifact Artifact) (ArtifactRef, error) {
	return s.writeArtifactWithStage(runID, artifact, nil, stageArtifact)
}

// WriteArtifactFromFile persists an artifact by streaming content from sourcePath.
func (s *Store) WriteArtifactFromFile(runID string, artifact Artifact, sourcePath string) (ArtifactRef, error) {
	if sourcePath == "" {
		return ArtifactRef{}, errors.New("artifact source path is required")
	}
	return s.writeArtifactWithStage(runID, artifact, nil, func(path string, artifact Artifact) (stagedArtifact, error) {
		return stageArtifactFromFile(path, artifact, sourcePath)
	})
}

// WriteArtifactIfState persists an artifact only while the run is in the expected state.
func (s *Store) WriteArtifactIfState(runID, expectedState string, artifact Artifact) (ArtifactRef, error) {
	if expectedState == "" {
		return ArtifactRef{}, errors.New("expected state is required")
	}
	return s.writeArtifact(runID, artifact, func(run *Run) error {
		if run.Status.State != expectedState {
			return &StateMismatchError{RunID: run.ID, Got: run.Status.State, Want: expectedState}
		}
		return nil
	})
}

func (s *Store) writeArtifact(runID string, artifact Artifact, validate func(*Run) error) (ArtifactRef, error) {
	return s.writeArtifactWithStage(runID, artifact, validate, stageArtifact)
}

func (s *Store) writeArtifactWithStage(runID string, artifact Artifact, validate func(*Run) error, stage func(string, Artifact) (stagedArtifact, error)) (ArtifactRef, error) {
	if err := validateRunID(runID); err != nil {
		return ArtifactRef{}, err
	}
	var ref ArtifactRef
	err := s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if validate != nil {
			if err := validate(run); err != nil {
				return err
			}
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
		staged, err := stage(path, artifact)
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

// RecordFollowup appends one structured follow-up entry to followups.md.
func (s *Store) RecordFollowup(runID string, req RecordFollowupRequest) (ArtifactRef, error) {
	req.Time = normalizeTime(req.Time)
	content, err := formatFollowupEntry(req)
	if err != nil {
		return ArtifactRef{}, err
	}
	return s.WriteArtifact(runID, Artifact{
		Kind:    KindFollowup,
		Name:    string(req.Source),
		Content: content,
		Time:    req.Time,
	})
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
		routing := attemptStartRoutingFromFields(req.ConsumeAttemptID, req.RetryLineage, req.SupersedeReason)
		if err := validateAttemptStartRouting(run.Status, routing); err != nil {
			return fmt.Errorf("run %q %w", runID, err)
		}
		if err := ctx.Err(); err != nil {
			return err
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
			return errors.New("workflow loop hard-cap override consumption requires workflow state entry")
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

// RecordAttemptWarning records a process warning without changing attempt outcome.
func (s *Store) RecordAttemptWarning(runID string, warning AttemptWarning) (Status, Event, error) {
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
	err = s.withRunLock(runID, func() error {
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

// RecordAttemptReport terminalizes the current active attempt with a structured worker report.
func (s *Store) RecordAttemptReport(runID string, req RecordReportRequest) (Attempt, Event, error) {
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
	err := s.withRunLock(runID, func() error {
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

func cleanupStagedArtifacts(staged []stagedArtifact) {
	for _, artifact := range staged {
		artifact.cleanup()
	}
}

func rollbackStagedArtifacts(staged []stagedArtifact) error {
	var err error
	for i := len(staged) - 1; i >= 0; i-- {
		if rollbackErr := staged[i].rollback(); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
	}
	return err
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
	err = s.withRunLock(runID, func() error {
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

func applyAttemptReport(status Status, attempt *Attempt, state string, report Report, req RecordReportRequest) error {
	switch {
	case attempt.State != AttemptStateActive && (!req.AllowStartingAttempt || attempt.State != AttemptStateStarting):
		return fmt.Errorf("attempt %q state %q, want active", attempt.AttemptID, attempt.State)
	case report.RunID != attempt.RunID:
		return fmt.Errorf("report run_id %q does not match active attempt run_id %q", report.RunID, attempt.RunID)
	case report.StepID != attempt.StepID:
		return fmt.Errorf("report step_id %q does not match active attempt step_id %q", report.StepID, attempt.StepID)
	case report.AgentID != attempt.AgentID:
		return fmt.Errorf("report agent_id %q does not match active attempt agent_id %q", report.AgentID, attempt.AgentID)
	case report.AttemptID != attempt.AttemptID:
		return fmt.Errorf("report attempt_id %q does not match active attempt attempt_id %q", report.AttemptID, attempt.AttemptID)
	}
	if report.ReportRef != nil {
		if err := validateArtifactRef(*report.ReportRef, 0); err != nil {
			return err
		}
		if report.ReportRef.Kind != KindReport {
			return fmt.Errorf("artifact %s kind %q, want %q", report.ReportRef.Path, report.ReportRef.Kind, KindReport)
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

func validateReportTerminalization(state string, report Report) error {
	switch {
	case report.RunID == "":
		return errors.New("report run id is required")
	case report.StepID == "":
		return errors.New("report step id is required")
	case report.AgentID == "":
		return errors.New("report agent id is required")
	case report.AttemptID == "":
		return errors.New("report attempt id is required")
	case report.Status == "":
		return errors.New("report status is required")
	case report.Result == "":
		return errors.New("report result is required")
	case report.Summary == "":
		return errors.New("report summary is required")
	case state != AttemptStateReported && state != AttemptStateInvalidReport:
		return fmt.Errorf("report state %q is not terminal", state)
	case state == AttemptStateInvalidReport && (report.Status != attemptStatusFailed || report.Result != AttemptResultInvalidReport):
		return fmt.Errorf("report terminal outcome %s/%s with state %q is invalid", report.Status, report.Result, state)
	default:
		return nil
	}
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
		return errors.New("retry lineage step_id is required")
	}
	for pair, count := range retry.Counts {
		if pair == "" {
			return errors.New("retry lineage pair is required")
		}
		if count < 0 {
			return fmt.Errorf("retry count for %q must be >= 0, got %d", pair, count)
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
		return fmt.Errorf("has unconsumed launcher outcome %s/%s for attempt %q", latest.Status, latest.Result, latest.AttemptID)
	}
	if routing.ConsumeAttemptID != "" && (!hasLatest || latest.AttemptID != routing.ConsumeAttemptID) {
		return fmt.Errorf("latest outcome attempt is not %q", routing.ConsumeAttemptID)
	}
	if routing.RetryLineage != nil {
		if routing.ConsumeAttemptID == "" {
			return errors.New("retry lineage requires consume_attempt_id")
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
		return fmt.Errorf("latest outcome attempt is not %q", attemptID)
	}
	if latest.AttemptID != attemptID {
		return fmt.Errorf("latest outcome attempt is %q, want %q", latest.AttemptID, attemptID)
	}
	return nil
}

func cloneRetryLineagePtr(retry *RetryLineage) *RetryLineage {
	if retry == nil {
		return nil
	}
	return &RetryLineage{StepID: retry.StepID, Counts: maps.Clone(retry.Counts)}
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
	case eventRunCreated, eventStatusUpdated, eventArtifactWritten, eventAttemptStarted, eventAttemptPrompted, eventAttemptLogged, eventAttemptProcess, eventAttemptFinished, eventAttemptRecovered, eventAttemptReported, eventAttemptWarning, eventReportIgnored, eventRunContinued, eventWorkflowSoftCap, eventWorkflowHardCap, eventWorkflowHardCapOverride, eventWorkflowStepSkipped:
		return true
	default:
		return false
	}
}

func nextWorkflowStateEntry(status Status, req WorkflowStateEntryRequest) (WorkflowStateEntry, error) {
	if req.State == "" {
		return WorkflowStateEntry{}, errors.New("workflow state entry state is required")
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
		return fmt.Errorf("run continued mode = %q, want %q", payload.Mode, ContinueModeResolveBlock)
	case payload.PreviousState != stateBlockedHuman:
		return fmt.Errorf("run continued previous_state = %q, want %q", payload.PreviousState, stateBlockedHuman)
	case payload.NewState != stateRunning:
		return fmt.Errorf("run continued new_state = %q, want %q", payload.NewState, stateRunning)
	case strings.TrimSpace(payload.Reason) == "":
		return errors.New("run continued reason is required")
	case payload.Reason != strings.TrimSpace(payload.Reason):
		return errors.New("run continued reason must be trimmed")
	case status.State != stateBlockedHuman:
		return fmt.Errorf("run continued requires state %q, got %q", stateBlockedHuman, status.State)
	case status.ActiveAttempt != nil:
		return fmt.Errorf("run continued while attempt %q is active", status.ActiveAttempt.AttemptID)
	case status.WorkflowLoop.HardCapBlock != nil:
		return errors.New("run continued resolve_block is not valid for active workflow-loop hard-cap block")
	}
	attempt, ok := latestResolvableBlockedAttempt(status)
	if !ok {
		return errors.New("run continued resolve_block requires latest terminal blocked attempt")
	}
	if payload.ResolvedAttemptID != attempt.AttemptID ||
		payload.ResolvedStepID != attempt.StepID ||
		payload.ResolvedStatus != attempt.Status ||
		payload.ResolvedResult != attempt.Result {
		return errors.New("run continued resolved attempt fields do not match latest terminal blocked attempt")
	}
	return nil
}

func validateWorkflowStepSkippedPayload(status Status, event Event, payload workflowStepSkippedPayload) (SkippedStep, error) {
	reason := strings.TrimSpace(payload.Reason)
	switch {
	case payload.StepID == "":
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped step_id is required", event.Sequence)
	case payload.Status != "done":
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped status = %q, want done", event.Sequence, payload.Status)
	case payload.Result != "skipped":
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped result = %q, want skipped", event.Sequence, payload.Result)
	case reason == "":
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped reason is required", event.Sequence)
	case payload.Reason != reason:
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped reason must be trimmed", event.Sequence)
	case payload.State == "":
		return SkippedStep{}, fmt.Errorf("event %d workflow.step_skipped state is required", event.Sequence)
	case status.ActiveAttempt != nil:
		return SkippedStep{}, fmt.Errorf("event %d skips step while attempt %q is active", event.Sequence, status.ActiveAttempt.AttemptID)
	case status.State != stateRunning:
		return SkippedStep{}, fmt.Errorf("event %d skips step while run state is %q, want %q", event.Sequence, status.State, stateRunning)
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
		return errors.New("workflow loop soft cap workflow is required")
	case loopCap.State == "":
		return errors.New("workflow loop soft cap state is required")
	case loopCap.Count <= 0:
		return fmt.Errorf("workflow loop soft cap count must be > 0, got %d", loopCap.Count)
	case loopCap.Soft <= 0:
		return fmt.Errorf("workflow loop soft cap soft must be > 0, got %d", loopCap.Soft)
	case loopCap.Hard <= 0:
		return fmt.Errorf("workflow loop soft cap hard must be > 0, got %d", loopCap.Hard)
	}
	return nil
}

func validateWorkflowLoopHardCap(loopCap WorkflowLoopHardCap) error {
	switch {
	case loopCap.Workflow == "":
		return errors.New("workflow loop hard cap workflow is required")
	case loopCap.BlockedState == "":
		return errors.New("workflow loop hard cap blocked target state is required")
	case loopCap.CurrentCount < 0:
		return fmt.Errorf("workflow loop hard cap current count must be >= 0, got %d", loopCap.CurrentCount)
	case loopCap.ProspectiveCount <= loopCap.CurrentCount:
		return fmt.Errorf("workflow loop hard cap prospective count must be greater than current count, got prospective=%d current=%d", loopCap.ProspectiveCount, loopCap.CurrentCount)
	case loopCap.Soft <= 0:
		return fmt.Errorf("workflow loop hard cap soft must be > 0, got %d", loopCap.Soft)
	case loopCap.Hard <= 0:
		return fmt.Errorf("workflow loop hard cap hard must be > 0, got %d", loopCap.Hard)
	case loopCap.Reason != WorkflowLoopHardCapReason:
		return fmt.Errorf("workflow loop hard cap reason = %q, want %q", loopCap.Reason, WorkflowLoopHardCapReason)
	}
	return nil
}

func validateWorkflowLoopHardCapOverride(override WorkflowLoopHardCapOverride) error {
	switch {
	case override.Workflow == "":
		return errors.New("workflow loop hard-cap override workflow is required")
	case override.TargetState == "":
		return errors.New("workflow loop hard-cap override target state is required")
	case override.CountBeforeOverride < 0:
		return fmt.Errorf("workflow loop hard-cap override count before must be >= 0, got %d", override.CountBeforeOverride)
	case override.CountAfterOverride <= override.CountBeforeOverride:
		return fmt.Errorf("workflow loop hard-cap override count after must be greater than count before, got after=%d before=%d", override.CountAfterOverride, override.CountBeforeOverride)
	case override.Soft <= 0:
		return fmt.Errorf("workflow loop hard-cap override soft must be > 0, got %d", override.Soft)
	case override.Hard <= 0:
		return fmt.Errorf("workflow loop hard-cap override hard must be > 0, got %d", override.Hard)
	case strings.TrimSpace(override.HumanAction) == "":
		return errors.New("workflow loop hard-cap override human action is required")
	case override.Reason != WorkflowLoopHardCapReason:
		return fmt.Errorf("workflow loop hard-cap override reason = %q, want %q", override.Reason, WorkflowLoopHardCapReason)
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
		return errors.New("workflow loop hard-cap override consumption requires pending override")
	case *pending != override:
		return errors.New("workflow loop hard-cap override consumption does not match pending override")
	case entry.Workflow != override.Workflow:
		return fmt.Errorf("workflow loop hard-cap override workflow = %q, want %q", override.Workflow, entry.Workflow)
	case entry.State != override.TargetState:
		return fmt.Errorf("workflow loop hard-cap override target state = %q, want %q", override.TargetState, entry.State)
	case entry.Count != override.CountAfterOverride:
		return fmt.Errorf("workflow loop hard-cap override count after = %d, want workflow entry count %d", override.CountAfterOverride, entry.Count)
	case status.WorkflowLoop.Counts[entry.State] != override.CountBeforeOverride:
		return fmt.Errorf("workflow loop hard-cap override count before = %d, want current count %d", override.CountBeforeOverride, status.WorkflowLoop.Counts[entry.State])
	}
	return nil
}

func applyReplayedWorkflowStateEntry(status *Status, event Event, entry *WorkflowStateEntry) error {
	if entry == nil {
		return nil
	}
	switch {
	case entry.Workflow != status.Workflow:
		return fmt.Errorf("event %d workflow state entry workflow %q does not match status workflow %q", event.Sequence, entry.Workflow, status.Workflow)
	case entry.State == "":
		return fmt.Errorf("event %d workflow state entry state is required", event.Sequence)
	case entry.Count != status.WorkflowLoop.Counts[entry.State]+1:
		return fmt.Errorf("event %d workflow state entry %q count = %d, want %d", event.Sequence, entry.State, entry.Count, status.WorkflowLoop.Counts[entry.State]+1)
	case entry.Repeated != (entry.Count > 1):
		return fmt.Errorf("event %d workflow state entry %q repeated = %t, want %t", event.Sequence, entry.State, entry.Repeated, entry.Count > 1)
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

func formatFollowupEntry(req RecordFollowupRequest) ([]byte, error) {
	title := strings.TrimSpace(req.Followup.Title)
	if title == "" {
		return nil, errors.New("follow-up title is required")
	}
	source := req.Source
	switch source {
	case FollowupSourceReport:
		if strings.TrimSpace(req.StepID) == "" {
			return nil, errors.New("follow-up report step id is required")
		}
		if strings.TrimSpace(req.AgentID) == "" {
			return nil, errors.New("follow-up report agent id is required")
		}
		if strings.TrimSpace(req.AttemptID) == "" {
			return nil, errors.New("follow-up report attempt id is required")
		}
	case FollowupSourceOrchestrator:
	default:
		return nil, fmt.Errorf("follow-up source %q is not supported", source)
	}
	recordedAt := normalizeTime(req.Time)
	var out strings.Builder
	fmt.Fprintf(&out, "## %s\n\n", oneLineMetadata(title))
	fmt.Fprintf(&out, "Source: %s\n", source)
	if source == FollowupSourceReport {
		fmt.Fprintf(&out, "Step: %s\n", oneLineMetadata(req.StepID))
		fmt.Fprintf(&out, "Agent: %s\n", oneLineMetadata(req.AgentID))
		fmt.Fprintf(&out, "Attempt: %s\n", oneLineMetadata(req.AttemptID))
	}
	fmt.Fprintf(&out, "Recorded-At: %s\n", recordedAt.Format(time.RFC3339))
	details := strings.TrimSpace(req.Followup.Details)
	if details != "" {
		out.WriteString("\n")
		out.WriteString(normalizeMarkdownDetails(details))
		out.WriteString("\n")
	}
	out.WriteString("\n")
	return []byte(out.String()), nil
}

func oneLineMetadata(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func normalizeMarkdownDetails(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
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
			if err := applyReplayedWorkflowStateEntry(&replayed, event, payload.WorkflowStateEntry); err != nil {
				return Status{}, err
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
			routing := attemptStartRoutingFromFields(payload.ConsumeAttemptID, payload.RetryLineage, payload.SupersedeReason)
			if err := validateAttemptStartRouting(replayed, routing); err != nil {
				return Status{}, fmt.Errorf("event %d attempt routing: %w", event.Sequence, err)
			}
			if payload.ConsumedWorkflowLoopHardCapOverride != nil {
				if payload.WorkflowStateEntry == nil {
					return Status{}, fmt.Errorf("event %d consumes workflow loop hard cap override without workflow state entry", event.Sequence)
				}
				if err := validateWorkflowLoopHardCapOverrideConsumption(replayed, *payload.WorkflowStateEntry, *payload.ConsumedWorkflowLoopHardCapOverride); err != nil {
					return Status{}, fmt.Errorf("event %d workflow loop hard cap override consumption: %w", event.Sequence, err)
				}
			}
			if err := applyReplayedWorkflowStateEntry(&replayed, event, payload.WorkflowStateEntry); err != nil {
				return Status{}, err
			}
			if payload.ConsumedWorkflowLoopHardCapOverride != nil {
				replayed.WorkflowLoop.PendingHardCapOverride = nil
			}
			replayed.Continued = nil
			attempt := payload.Attempt
			if err := validateStartedAttemptEvent(event, attempt, replayed.RunID); err != nil {
				return Status{}, err
			}
			if slices.ContainsFunc(replayed.Attempts, func(existing Attempt) bool {
				return existing.AttemptID == attempt.AttemptID
			}) {
				return Status{}, fmt.Errorf("event %d duplicate attempt %q", event.Sequence, attempt.AttemptID)
			}
			applyAttemptStartRouting(&replayed, event.Time, attempt.AttemptID, routing)
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
		case eventAttemptReported:
			var payload attemptReportedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.reported payload: %w", event.Sequence, err)
			}
			if err := reportReplayedActiveAttempt(&replayed, event, payload); err != nil {
				return Status{}, err
			}
		case eventAttemptWarning:
			var payload attemptWarningPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d attempt.warning payload: %w", event.Sequence, err)
			}
			if err := applyReplayedAttemptWarning(&replayed, event, payload.Warning); err != nil {
				return Status{}, err
			}
		case eventReportIgnored:
			var payload reportIgnoredPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d report.ignored payload: %w", event.Sequence, err)
			}
			if payload.Reason == "" {
				return Status{}, fmt.Errorf("event %d report ignored reason is required", event.Sequence)
			}
			if payload.RunID != "" && payload.RunID != event.RunID {
				return Status{}, fmt.Errorf("event %d report ignored run_id %q does not match event run_id %q", event.Sequence, payload.RunID, event.RunID)
			}
		case eventRunContinued:
			var payload runContinuedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d run.continued payload: %w", event.Sequence, err)
			}
			if err := validateRunContinuedPayload(replayed, payload); err != nil {
				return Status{}, fmt.Errorf("event %d run.continued payload: %w", event.Sequence, err)
			}
			applyRunContinued(&replayed, payload)
		case eventWorkflowStepSkipped:
			var payload workflowStepSkippedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.step_skipped payload: %w", event.Sequence, err)
			}
			skipped, err := validateWorkflowStepSkippedPayload(replayed, event, payload)
			if err != nil {
				return Status{}, err
			}
			if err := applyReplayedWorkflowStateEntry(&replayed, event, payload.WorkflowStateEntry); err != nil {
				return Status{}, err
			}
			applyAttemptOutcomeConsumption(&replayed, event, payload.ConsumeAttemptID)
			applyStepSkipped(&replayed, skipped)
			replayed.State = payload.State
			replayed.RetryLineage = nil
			replayed.Continued = nil
		case eventWorkflowSoftCap:
			var payload workflowLoopSoftCapPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_soft_cap payload: %w", event.Sequence, err)
			}
			if err := validateWorkflowLoopSoftCap(payload.Cap); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_soft_cap payload: %w", event.Sequence, err)
			}
			if payload.Cap.Workflow != replayed.Workflow {
				return Status{}, fmt.Errorf("event %d workflow loop soft cap workflow %q does not match status workflow %q", event.Sequence, payload.Cap.Workflow, replayed.Workflow)
			}
			applyWorkflowLoopSoftCap(&replayed, payload.Cap)
		case eventWorkflowHardCap:
			var payload workflowLoopHardCapPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_hard_cap payload: %w", event.Sequence, err)
			}
			if err := validateWorkflowLoopHardCap(payload.Cap); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_hard_cap payload: %w", event.Sequence, err)
			}
			if payload.Cap.Workflow != replayed.Workflow {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap workflow %q does not match status workflow %q", event.Sequence, payload.Cap.Workflow, replayed.Workflow)
			}
			if payload.State != stateBlockedHuman {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap state = %q, want %q", event.Sequence, payload.State, stateBlockedHuman)
			}
			if replayed.ActiveAttempt != nil {
				return Status{}, fmt.Errorf("event %d blocks workflow loop while attempt %q is active", event.Sequence, replayed.ActiveAttempt.AttemptID)
			}
			applyWorkflowLoopHardCap(&replayed, payload.Cap)
			replayed.State = stateBlockedHuman
		case eventWorkflowHardCapOverride:
			var payload workflowLoopHardCapOverridePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_hard_cap_override payload: %w", event.Sequence, err)
			}
			if err := validateWorkflowLoopHardCapOverride(payload.Override); err != nil {
				return Status{}, fmt.Errorf("event %d workflow.loop_hard_cap_override payload: %w", event.Sequence, err)
			}
			if payload.Override.Workflow != replayed.Workflow {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap override workflow %q does not match status workflow %q", event.Sequence, payload.Override.Workflow, replayed.Workflow)
			}
			if payload.State != stateRunning {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap override state = %q, want %q", event.Sequence, payload.State, stateRunning)
			}
			block := replayed.WorkflowLoop.HardCapBlock
			if replayed.State != stateBlockedHuman || block == nil {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap override requires active hard-cap block", event.Sequence)
			}
			if block.Workflow != payload.Override.Workflow ||
				block.BlockedState != payload.Override.TargetState ||
				block.CurrentCount != payload.Override.CountBeforeOverride ||
				block.ProspectiveCount != payload.Override.CountAfterOverride ||
				block.Soft != payload.Override.Soft ||
				block.Hard != payload.Override.Hard ||
				block.Reason != payload.Override.Reason {
				return Status{}, fmt.Errorf("event %d workflow loop hard cap override does not match active hard-cap block", event.Sequence)
			}
			applyWorkflowLoopHardCapOverride(&replayed, payload.Override)
			replayed.State = stateRunning
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
	case attempt.ReportRef != nil:
		return fmt.Errorf("event %d attempt report_ref must be empty for starting attempt", event.Sequence)
	case attempt.Report != nil:
		return fmt.Errorf("event %d attempt report must be empty for starting attempt", event.Sequence)
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

func reportReplayedActiveAttempt(status *Status, event Event, payload attemptReportedPayload) error {
	if err := validateReportTerminalization(payload.State, payload.Report); err != nil {
		return fmt.Errorf("event %d %w", event.Sequence, err)
	}
	if payload.AttemptID == "" {
		return fmt.Errorf("event %d attempt_id is required", event.Sequence)
	}
	if payload.Report.AttemptID != payload.AttemptID {
		return fmt.Errorf("event %d report attempt_id %q does not match", event.Sequence, payload.Report.AttemptID)
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
			return fmt.Errorf("event %d followup artifact %s kind %q, want %q", event.Sequence, ref.Path, ref.Kind, KindFollowup)
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
		return fmt.Errorf("event %d warning time does not match event time", event.Sequence)
	}
	status.Warnings = append(status.Warnings, warning)
	return nil
}

func validateAttemptWarning(status Status, warning AttemptWarning) error {
	if warning.AttemptID == "" {
		return errors.New("attempt_id is required")
	}
	if warning.Kind == "" {
		return errors.New("kind is required")
	}
	if !slices.ContainsFunc(status.Attempts, func(attempt Attempt) bool {
		return attempt.AttemptID == warning.AttemptID
	}) {
		return fmt.Errorf("has no attempt %q", warning.AttemptID)
	}
	return nil
}

func validateRunCreated(event Event, status Status) (createRunPayload, error) {
	if event.Sequence != 1 || event.Type != eventRunCreated {
		return createRunPayload{}, fmt.Errorf("line 1: expected %s event", eventRunCreated)
	}
	var payload createRunPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return createRunPayload{}, fmt.Errorf("event 1 run.created payload: %w", err)
	}
	if payload.Workflow != status.Workflow {
		return createRunPayload{}, fmt.Errorf("event 1 workflow %q does not match status.json workflow %q", payload.Workflow, status.Workflow)
	}
	return payload, nil
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
