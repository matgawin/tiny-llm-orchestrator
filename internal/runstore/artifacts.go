package runstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// WriteArtifact persists an artifact under the run directory and records it in the event log.
func (s *Store) WriteArtifact(runID string, artifact Artifact) (ArtifactRef, error) {
	return s.WriteArtifactContext(context.Background(), runID, artifact)
}

// WriteArtifactContext persists an artifact unless ctx is canceled before the artifact commits.
func (s *Store) WriteArtifactContext(ctx context.Context, runID string, artifact Artifact) (ArtifactRef, error) {
	return s.writeArtifactWithStage(ctx, runID, artifact, nil, stageArtifact)
}

// WriteArtifactFromFile persists an artifact by streaming content from sourcePath.
func (s *Store) WriteArtifactFromFile(runID string, artifact Artifact, sourcePath string) (ArtifactRef, error) {
	return s.WriteArtifactFromFileContext(context.Background(), runID, artifact, sourcePath)
}

// WriteArtifactFromFileContext persists an artifact by streaming content from sourcePath unless ctx is canceled before commit.
func (s *Store) WriteArtifactFromFileContext(ctx context.Context, runID string, artifact Artifact, sourcePath string) (ArtifactRef, error) {
	if sourcePath == "" {
		return ArtifactRef{}, stableerr.New("artifact source path is required")
	}

	return s.writeArtifactWithStage(ctx, runID, artifact, nil, func(path string, artifact Artifact) (stagedArtifact, error) {
		return stageArtifactFromFile(path, artifact, sourcePath)
	})
}

// WriteArtifactIfState persists an artifact only while the run is in the expected state.
func (s *Store) WriteArtifactIfState(runID, expectedState string, artifact Artifact) (ArtifactRef, error) {
	return s.WriteArtifactIfStateContext(context.Background(), runID, expectedState, artifact)
}

// WriteArtifactIfStateContext persists an artifact only while the run is in the expected state unless ctx is canceled before commit.
func (s *Store) WriteArtifactIfStateContext(ctx context.Context, runID, expectedState string, artifact Artifact) (ArtifactRef, error) {
	if expectedState == "" {
		return ArtifactRef{}, stableerr.New("expected state is required")
	}

	return s.writeArtifact(ctx, runID, artifact, func(run *Run) error {
		if run.Status.State != expectedState {
			return &StateMismatchError{RunID: run.ID, Got: run.Status.State, Want: expectedState}
		}

		return nil
	})
}

func (s *Store) writeArtifact(ctx context.Context, runID string, artifact Artifact, validate func(*Run) error) (ArtifactRef, error) {
	return s.writeArtifactWithStage(ctx, runID, artifact, validate, stageArtifact)
}

func (s *Store) writeArtifactWithStage(ctx context.Context, runID string, artifact Artifact, validate func(*Run) error, stage func(string, Artifact) (stagedArtifact, error)) (ArtifactRef, error) {
	if ctx == nil {
		return ArtifactRef{}, stableerr.New("context is required")
	}

	if err := validateRunID(runID); err != nil {
		return ArtifactRef{}, err
	}

	var ref ArtifactRef

	err := s.withRunLockContext(ctx, runID, func() error {
		written, err := s.commitArtifactWrite(ctx, runID, artifact, validate, stage)
		ref = written

		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return ref, err
	}

	return ref, nil
}

func (s *Store) commitArtifactWrite(ctx context.Context, runID string, artifact Artifact, validate func(*Run) error, stage func(string, Artifact) (stagedArtifact, error)) (ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactRef{}, fmt.Errorf("write artifact with stage: %w", err)
	}

	run, err := s.load(runID)
	if err != nil {
		return ArtifactRef{}, err
	}

	if validate != nil {
		if err := validate(run); err != nil {
			return ArtifactRef{}, err
		}
	}

	ref, staged, err := s.stageArtifactWrite(run, artifact, stage)
	if err != nil {
		return ArtifactRef{}, err
	}
	defer staged.cleanup()

	event, err := artifactWrittenEvent(runID, run, artifact.Time, ref)
	if err != nil {
		return ArtifactRef{}, err
	}

	if err := commitArtifactAndEvent(runID, run, ref, staged, event); err != nil {
		return ArtifactRef{}, err
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

func (s *Store) stageArtifactWrite(run *Run, artifact Artifact, stage func(string, Artifact) (stagedArtifact, error)) (ArtifactRef, stagedArtifact, error) {
	sequence := nextEventSequence(run)

	relPath, err := artifactPath(artifact.Kind, artifact.Name, sequence)
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}

	if err := validateRelativeArtifactPath(relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}

	if err := validateArtifactWriteAllowed(run.Status.Artifacts, artifact.Kind, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, err
	}

	ref := ArtifactRef{
		Kind:          artifact.Kind,
		Path:          relPath,
		Name:          artifact.Name,
		EventSequence: sequence,
	}

	path := filepath.Join(run.Path, filepath.FromSlash(relPath))
	if err := ensureArtifactParentDir(run.Path, relPath); err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}

	staged, err := stage(path, artifact)
	if err != nil {
		return ArtifactRef{}, stagedArtifact{}, fmt.Errorf("run %q artifact %s: %w", run.ID, relPath, err)
	}

	return ref, staged, nil
}

func artifactWrittenEvent(runID string, run *Run, at time.Time, ref ArtifactRef) (Event, error) {
	payload, err := marshalPayload(artifactWrittenPayload{Artifact: ref})
	if err != nil {
		return Event{}, err
	}

	return prepareRunEvent(runID, run, Event{
		Time:    at,
		Type:    eventArtifactWritten,
		Payload: payload,
	}), nil
}

func commitArtifactAndEvent(runID string, run *Run, ref ArtifactRef, staged stagedArtifact, event Event) error {
	if err := staged.commit(); err != nil {
		return fmt.Errorf("run %q artifact %s: %w", runID, ref.Path, err)
	}

	if err := appendEvent(filepath.Join(run.Path, eventsName), event); err != nil {
		if eventAppendPossiblyCommitted(err) {
			return fmt.Errorf("run %q events.jsonl: %w", runID, err)
		}

		if rollbackErr := staged.rollback(); rollbackErr != nil {
			return fmt.Errorf("run %q events.jsonl: %w", runID, errors.Join(err, fmt.Errorf("rollback artifact %s: %w", ref.Path, rollbackErr)))
		}

		return fmt.Errorf("run %q events.jsonl: %w", runID, err)
	}

	return nil
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

// ReadArtifact reads a persisted artifact through a validated run-store path.
func (s *Store) ReadArtifact(runID string, ref ArtifactRef) ([]byte, error) {
	return s.ReadArtifactContext(context.Background(), runID, ref)
}

// ReadArtifactContext reads a persisted artifact unless ctx is canceled before the run lock is acquired.
func (s *Store) ReadArtifactContext(ctx context.Context, runID string, ref ArtifactRef) ([]byte, error) {
	if ctx == nil {
		return nil, stableerr.New("context is required")
	}

	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	var content []byte

	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read artifact context: %w", err)
		}

		run, err := s.load(runID)
		if err != nil {
			return err
		}

		path, err := resolveRecordedArtifactPath(run, ref)
		if err != nil {
			return err
		}

		content, err = os.ReadFile(path) // #nosec G304,G703 -- path is scoped to the validated run directory.
		if err != nil {
			return fmt.Errorf("read artifact context: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return content, nil
}

// OpenArtifactAppend opens a recorded artifact for append through a validated run-store path.
func (s *Store) OpenArtifactAppend(runID string, ref ArtifactRef) (*os.File, error) {
	return s.OpenArtifactAppendContext(context.Background(), runID, ref)
}

// OpenArtifactAppendContext opens a recorded artifact for append unless ctx is canceled before the run lock is acquired.
func (s *Store) OpenArtifactAppendContext(ctx context.Context, runID string, ref ArtifactRef) (*os.File, error) {
	if ctx == nil {
		return nil, stableerr.New("context is required")
	}

	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	if ref.Kind != KindLog {
		return nil, stableerr.Errorf("artifact %s kind %q, want %q", ref.Path, ref.Kind, KindLog)
	}

	var file *os.File

	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("open artifact append context: %w", err)
		}

		run, err := s.load(runID)
		if err != nil {
			return err
		}

		path, err := resolveRecordedArtifactPath(run, ref)
		if err != nil {
			return err
		}

		if run.Status.ActiveAttempt == nil || run.Status.ActiveAttempt.LogRef == nil || *run.Status.ActiveAttempt.LogRef != ref {
			return stableerr.Errorf("artifact %s is not the current active attempt log for run %q", ref.Path, runID)
		}

		opened, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, runFilePerm) // #nosec G304,G703 -- path is scoped to the validated run directory and opened without following final-component symlinks.
		if err != nil {
			return fmt.Errorf("open artifact append context: %w", err)
		}

		info, err := opened.Stat()
		if err != nil {
			_ = opened.Close()
			return fmt.Errorf("open artifact append context: %w", err)
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
		return "", stableerr.Errorf("artifact %s is not recorded for run %q", ref.Path, run.ID)
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

func validateRecordedArtifactRef(status Status, ref ArtifactRef, kind ArtifactKind) error {
	if err := validateArtifactRef(ref, 0); err != nil {
		return err
	}

	if ref.Kind != kind {
		return stableerr.Errorf("artifact %s kind %q, want %q", ref.Path, ref.Kind, kind)
	}

	if !slices.Contains(status.Artifacts, ref) {
		return stableerr.Errorf("artifact %s is not recorded for run %q", ref.Path, status.RunID)
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
		return stableerr.Errorf("event_sequence %d does not match", ref.EventSequence)
	}

	if err := validateArtifactPathForKind(ref); err != nil {
		return err
	}

	return nil
}

func validateArtifactPathForKind(ref ArtifactRef) error {
	spec, ok := artifactSpec(ref.Kind)
	if !ok {
		return stableerr.Errorf("unsupported artifact kind %q", ref.Kind)
	}

	if spec.fixedPath != "" {
		if ref.Path != spec.fixedPath {
			return stableerr.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
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
				return stableerr.Errorf("artifact %s for kind %q has already been written", path, kind)
			}
		}
	case KindReport, KindPrompt, KindLog, KindSnapshot, KindSummary, KindFollowup:
	}

	return nil
}

func validateNumberedArtifactPath(ref ArtifactRef, dir, ext string) error {
	prefix := fmt.Sprintf("%s/%06d-", dir, ref.EventSequence)
	if !strings.HasPrefix(ref.Path, prefix) || !strings.HasSuffix(ref.Path, ext) {
		return stableerr.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
	}

	name := strings.TrimPrefix(ref.Path, dir+"/")
	if strings.Contains(name, "/") {
		return stableerr.Errorf("artifact path %q does not match kind %q", ref.Path, ref.Kind)
	}

	return nil
}
