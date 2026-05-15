package runstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// Create creates a new run directory with an initial event and status file.
func (s *Store) Create(req CreateRunRequest) (*Run, error) {
	return s.CreateContext(context.Background(), req)
}

// CreateContext creates a new run directory unless ctx is canceled before publication.
func (s *Store) CreateContext(ctx context.Context, req CreateRunRequest) (*Run, error) {
	if ctx == nil {
		return nil, stableerr.New("context is required")
	}
	if req.Workflow == "" {
		return nil, stableerr.New("workflow is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create context: %w", err)
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
	if err := initializeTempRunLayout(tempDir, runID); err != nil {
		return nil, err
	}

	status, event, err := initialRunState(runID, req, now)
	if err != nil {
		return nil, err
	}
	if err := writeInitialEventLog(filepath.Join(tempDir, eventsName), event); err != nil {
		return nil, fmt.Errorf("run %q events: %w", runID, err)
	}
	status.LastSequence = event.Sequence
	if err := writeStatus(filepath.Join(tempDir, statusName), status); err != nil {
		return nil, fmt.Errorf("run %q status: %w", runID, err)
	}
	unlockRuns, err := s.lockRunsDir(ctx)
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
				return s.CreateContext(ctx, req)
			}
			return nil, stableerr.Errorf("run %q already exists", runID)
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

func initializeTempRunLayout(tempDir, runID string) error {
	for _, dir := range artifactDirs() {
		if err := os.Mkdir(filepath.Join(tempDir, dir), 0o750); err != nil {
			return fmt.Errorf("create run %q artifact directory %s: %w", runID, dir, err)
		}
	}
	if err := os.Mkdir(filepath.Join(tempDir, configDirName), 0o750); err != nil {
		return fmt.Errorf("create run %q config directory: %w", runID, err)
	}
	if err := writeAtomic(filepath.Join(tempDir, followupsName), nil); err != nil {
		return fmt.Errorf("create run %q followups.md: %w", runID, err)
	}
	return createTempRunLock(tempDir, runID)
}

func createTempRunLock(tempDir, runID string) error {
	// Materialize the final per-run lock file in the temporary layout before
	// publication; the runs-directory lock below owns publication itself.
	lockFile, err := os.OpenFile(filepath.Join(tempDir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to a newly created temporary run directory.
	if err != nil {
		return fmt.Errorf("create run %q lock: %w", runID, err)
	}
	if err := lockFile.Close(); err != nil {
		return fmt.Errorf("create run %q lock: %w", runID, err)
	}
	return nil
}

func initialRunState(runID string, req CreateRunRequest, now time.Time) (Status, Event, error) {
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
			return Status{}, Event{}, err
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
		return Status{}, Event{}, err
	}
	return status, Event{
		SchemaVersion: schemaVersion,
		Sequence:      1,
		Time:          now,
		RunID:         runID,
		Type:          eventRunCreated,
		Payload:       payload,
	}, nil
}

type runDirReservation struct {
	path      string
	file      *os.File
	published bool
}

func reserveRunDir(runDir string) (*runDirReservation, error) {
	if err := os.Mkdir(runDir, 0o750); err != nil {
		return nil, fmt.Errorf("reserve run dir: %w", err)
	}
	reservation := &runDirReservation{path: runDir}
	file, err := os.OpenFile(filepath.Join(runDir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G304,G703 -- lock path is scoped to an atomically reserved run directory.
	if err != nil {
		reservation.cleanup()
		return nil, fmt.Errorf("reserve run dir: %w", err)
	}
	reservation.file = file
	fd := int(file.Fd()) // #nosec G115 -- file descriptors fit int on supported Linux targets.
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		reservation.cleanup()
		return nil, fmt.Errorf("reserve run dir: %w", err)
	}
	return reservation, nil
}

func publishReservedRunDir(tempDir string, reservation *runDirReservation) error {
	if reservation == nil {
		return stableerr.New("run directory reservation is required")
	}
	if err := os.Remove(filepath.Join(tempDir, ".lock")); err != nil {
		return fmt.Errorf("publish reserved run dir: %w", err)
	}
	for _, name := range append(artifactDirs(), configDirName, followupsName, eventsName, statusName) {
		if err := os.Rename(filepath.Join(tempDir, name), filepath.Join(reservation.path, name)); err != nil {
			return fmt.Errorf("publish reserved run dir: %w", err)
		}
	}
	if err := os.Remove(tempDir); err != nil {
		return fmt.Errorf("publish reserved run dir: %w", err)
	}
	return nil
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
	return s.LoadContext(context.Background(), runID)
}

// LoadContext recovers structured run state unless ctx is canceled before the run lock is acquired.
func (s *Store) LoadContext(ctx context.Context, runID string) (*Run, error) {
	if ctx == nil {
		return nil, stableerr.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	var run *Run
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("load context: %w", err)
		}
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
		return nil, stableerr.Errorf("run %q status.json run_id %q does not match", runID, status.RunID)
	}
	events, err := readEvents(filepath.Join(runDir, eventsName), runID)
	if err != nil {
		return nil, fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}
	if len(events) == 0 {
		return nil, stableerr.Errorf("run %q events.jsonl: no events", runID)
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
