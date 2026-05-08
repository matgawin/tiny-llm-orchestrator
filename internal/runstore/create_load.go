package runstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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

func (s *Store) runDir(runID string) string {
	return filepath.Join(s.runsDir, runID)
}
