package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	configDirName         = "config"
	configCurrentName     = "current.json"
	configResolvedName    = "resolved.json"
	configManifestName    = "manifest.json"
	configSnapshotVersion = 1
)

// ConfigSnapshot describes one fully materialized run config snapshot version.
type ConfigSnapshot struct {
	Version  int
	Resolved []byte
	Manifest []byte
}

type configCurrent struct {
	SchemaVersion int    `json:"schema_version"`
	Version       int    `json:"version"`
	VersionDir    string `json:"version_dir"`
}

// ConfigSnapshotRefreshValidator validates a locked run before refresh publication.
type ConfigSnapshotRefreshValidator func(run *Run, current CurrentConfigSnapshot) error

type configSnapshotRefreshedPayload struct {
	OldVersion            int    `json:"old_version"`
	OldVersionDir         string `json:"old_version_dir"`
	NewVersion            int    `json:"new_version"`
	NewVersionDir         string `json:"new_version_dir"`
	ManifestHashAlgorithm string `json:"manifest_hash_algorithm"`
	ManifestHash          string `json:"manifest_hash"`
	Source                string `json:"source"`
}

// WriteInitialConfigSnapshot persists config snapshot version 000001 for runID.
func (s *Store) WriteInitialConfigSnapshot(runID string, snapshot ConfigSnapshot) error {
	return s.WriteInitialConfigSnapshotContext(context.Background(), runID, snapshot)
}

// WriteInitialConfigSnapshotContext persists config snapshot version 000001 for runID unless ctx is canceled.
func (s *Store) WriteInitialConfigSnapshotContext(ctx context.Context, runID string, snapshot ConfigSnapshot) error {
	if snapshot.Version == 0 {
		snapshot.Version = 1
	}
	if snapshot.Version != 1 {
		return fmt.Errorf("initial config snapshot version = %d, want 1", snapshot.Version)
	}
	return s.writeConfigSnapshot(ctx, runID, snapshot, true)
}

// RefreshConfigSnapshot publishes the next config snapshot and records the refresh event under the run lock.
func (s *Store) RefreshConfigSnapshot(runID string, req RefreshConfigSnapshotRequest, validate ConfigSnapshotRefreshValidator) (ConfigSnapshotRefresh, error) {
	return s.RefreshConfigSnapshotContext(context.Background(), runID, req, validate)
}

// RefreshConfigSnapshotContext publishes the next config snapshot unless ctx is canceled before commit.
func (s *Store) RefreshConfigSnapshotContext(ctx context.Context, runID string, req RefreshConfigSnapshotRequest, validate ConfigSnapshotRefreshValidator) (ConfigSnapshotRefresh, error) {
	if ctx == nil {
		return ConfigSnapshotRefresh{}, errors.New("context is required")
	}
	if req.Source == "" {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot refresh source is required")
	}
	if req.ManifestHashAlgorithm == "" {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot refresh manifest hash algorithm is required")
	}
	if req.ManifestHash == "" {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot refresh manifest hash is required")
	}
	if validate == nil {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot refresh validator is required")
	}
	if err := validateRunID(runID); err != nil {
		return ConfigSnapshotRefresh{}, err
	}
	if len(req.Snapshot.Resolved) == 0 {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot resolved.json content is required")
	}
	if len(req.Snapshot.Manifest) == 0 {
		return ConfigSnapshotRefresh{}, errors.New("config snapshot manifest.json content is required")
	}

	var refresh ConfigSnapshotRefresh
	err := s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		configDir := filepath.Join(run.Path, configDirName)
		currentPath := filepath.Join(configDir, configCurrentName)
		current, err := readConfigCurrentFile(currentPath)
		if err != nil {
			return fmt.Errorf("run %q config snapshot %s: %w", runID, configCurrentName, err)
		}
		if err := validate(run, CurrentConfigSnapshot{Version: current.Version, VersionDir: current.VersionDir}); err != nil {
			return err
		}
		if req.Snapshot.Version != current.Version+1 {
			return fmt.Errorf("run %q refresh config snapshot version = %d, want %d", runID, req.Snapshot.Version, current.Version+1)
		}
		newVersionDir, err := configVersionDir(req.Snapshot.Version)
		if err != nil {
			return err
		}
		if err := s.writeConfigSnapshotLocked(run, req.Snapshot); err != nil {
			return err
		}
		payload := configSnapshotRefreshedPayload{
			OldVersion:            current.Version,
			OldVersionDir:         current.VersionDir,
			NewVersion:            req.Snapshot.Version,
			NewVersionDir:         newVersionDir,
			ManifestHashAlgorithm: req.ManifestHashAlgorithm,
			ManifestHash:          req.ManifestHash,
			Source:                req.Source,
		}
		eventPayload, err := marshalPayload(payload)
		if err != nil {
			return err
		}
		event := Event{Time: req.Time, Type: EventConfigSnapshotRefreshed, Payload: eventPayload}
		_, event, err = commitStatusBackedEvent(runID, run, event, func(status *Status, event Event) {
			status.UpdatedAt = event.Time
			status.LastSequence = event.Sequence
		})
		if err != nil {
			return err
		}
		refresh = ConfigSnapshotRefresh{
			OldVersion:            current.Version,
			OldVersionDir:         current.VersionDir,
			NewVersion:            req.Snapshot.Version,
			NewVersionDir:         newVersionDir,
			ManifestHashAlgorithm: req.ManifestHashAlgorithm,
			ManifestHash:          req.ManifestHash,
			Source:                req.Source,
			Event:                 event,
		}
		return nil
	})
	if err != nil {
		return ConfigSnapshotRefresh{}, err
	}
	return refresh, nil
}

func (s *Store) writeConfigSnapshot(ctx context.Context, runID string, snapshot ConfigSnapshot, initial bool) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := validateRunID(runID); err != nil {
		return err
	}
	if len(snapshot.Resolved) == 0 {
		return errors.New("config snapshot resolved.json content is required")
	}
	if len(snapshot.Manifest) == 0 {
		return errors.New("config snapshot manifest.json content is required")
	}
	return s.withRunLockContext(ctx, runID, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		if initial {
			currentPath := filepath.Join(run.Path, configDirName, configCurrentName)
			if _, err := os.Lstat(currentPath); err == nil {
				return fmt.Errorf("run %q config snapshot current.json already exists", runID)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("run %q config snapshot current.json: %w", runID, err)
			}
		}
		return s.writeConfigSnapshotLocked(run, snapshot)
	})
}

func (s *Store) writeConfigSnapshotLocked(run *Run, snapshot ConfigSnapshot) error {
	versionDir, err := configVersionDir(snapshot.Version)
	if err != nil {
		return err
	}
	currentContent, err := json.MarshalIndent(configCurrent{
		SchemaVersion: configSnapshotVersion,
		Version:       snapshot.Version,
		VersionDir:    versionDir,
	}, "", "  ")
	if err != nil {
		return err
	}
	currentContent = append(currentContent, '\n')
	configDir := filepath.Join(run.Path, configDirName)
	currentPath := filepath.Join(configDir, configCurrentName)
	versionPath := filepath.Join(configDir, versionDir)
	if err := ensureConfigSnapshotDir(configDir, versionPath); err != nil {
		return fmt.Errorf("run %q config snapshot %s: %w", run.ID, versionDir, err)
	}
	resolvedPath := filepath.Join(versionPath, configResolvedName)
	manifestPath := filepath.Join(versionPath, configManifestName)
	if err := writeNewRegularFile(resolvedPath, snapshot.Resolved); err != nil {
		return fmt.Errorf("run %q config snapshot %s/%s: %w", run.ID, versionDir, configResolvedName, err)
	}
	if err := writeNewRegularFile(manifestPath, snapshot.Manifest); err != nil {
		return fmt.Errorf("run %q config snapshot %s/%s: %w", run.ID, versionDir, configManifestName, err)
	}
	if err := writeAtomic(currentPath, currentContent); err != nil {
		return fmt.Errorf("run %q config snapshot %s: %w", run.ID, configCurrentName, err)
	}
	return nil
}

func readConfigCurrentFile(path string) (configCurrent, error) {
	if err := validateRegularFile(path, configCurrentName); err != nil {
		return configCurrent{}, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path is derived from a validated run directory.
	if err != nil {
		return configCurrent{}, err
	}
	var current configCurrent
	if err := json.Unmarshal(content, &current); err != nil {
		return configCurrent{}, fmt.Errorf("decode current snapshot: %w", err)
	}
	if current.SchemaVersion != configSnapshotVersion {
		return configCurrent{}, fmt.Errorf("schema_version = %d, want %d", current.SchemaVersion, configSnapshotVersion)
	}
	if current.Version <= 0 {
		return configCurrent{}, errors.New("version must be positive")
	}
	wantDir, err := configVersionDir(current.Version)
	if err != nil {
		return configCurrent{}, err
	}
	if current.VersionDir != wantDir {
		return configCurrent{}, fmt.Errorf("version_dir = %q, want %q", current.VersionDir, wantDir)
	}
	return current, nil
}

func configVersionDir(version int) (string, error) {
	if version <= 0 || version > 999999 {
		return "", fmt.Errorf("config snapshot version %d is outside supported range 1..999999", version)
	}
	return fmt.Sprintf("%06d", version), nil
}

func ensureConfigSnapshotDir(configDir, versionPath string) error {
	if err := ensureDir(configDir); err != nil {
		return err
	}
	versionName := filepath.Base(versionPath)
	if _, err := strconv.Atoi(versionName); err != nil || len(versionName) != 6 {
		return fmt.Errorf("invalid config snapshot version directory %q", versionName)
	}
	info, err := os.Lstat(versionPath)
	if os.IsNotExist(err) {
		return os.Mkdir(versionPath, 0o750) // #nosec G301,G703 -- versionPath is scoped under a validated run directory.
	}
	if err != nil {
		return err
	}
	return validateDirInfo(versionPath, info)
}

func writeNewRegularFile(path string, content []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", filepath.Base(path))
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(path, content)
}
