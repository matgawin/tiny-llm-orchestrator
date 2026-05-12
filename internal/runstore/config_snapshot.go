package runstore

import (
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

// WriteInitialConfigSnapshot persists config snapshot version 000001 for runID.
func (s *Store) WriteInitialConfigSnapshot(runID string, snapshot ConfigSnapshot) error {
	if snapshot.Version == 0 {
		snapshot.Version = 1
	}
	if snapshot.Version != 1 {
		return fmt.Errorf("initial config snapshot version = %d, want 1", snapshot.Version)
	}
	return s.writeConfigSnapshot(runID, snapshot, true)
}

func (s *Store) writeConfigSnapshot(runID string, snapshot ConfigSnapshot, initial bool) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	if len(snapshot.Resolved) == 0 {
		return errors.New("config snapshot resolved.json content is required")
	}
	if len(snapshot.Manifest) == 0 {
		return errors.New("config snapshot manifest.json content is required")
	}
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

	return s.withRunLock(runID, func() error {
		run, err := s.load(runID)
		if err != nil {
			return err
		}
		configDir := filepath.Join(run.Path, configDirName)
		currentPath := filepath.Join(configDir, configCurrentName)
		if initial {
			if _, err := os.Lstat(currentPath); err == nil {
				return fmt.Errorf("run %q config snapshot current.json already exists", runID)
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("run %q config snapshot current.json: %w", runID, err)
			}
		}
		versionPath := filepath.Join(configDir, versionDir)
		if err := ensureConfigSnapshotDir(configDir, versionPath); err != nil {
			return fmt.Errorf("run %q config snapshot %s: %w", runID, versionDir, err)
		}
		resolvedPath := filepath.Join(versionPath, configResolvedName)
		manifestPath := filepath.Join(versionPath, configManifestName)
		if err := writeNewRegularFile(resolvedPath, snapshot.Resolved); err != nil {
			return fmt.Errorf("run %q config snapshot %s/%s: %w", runID, versionDir, configResolvedName, err)
		}
		if err := writeNewRegularFile(manifestPath, snapshot.Manifest); err != nil {
			return fmt.Errorf("run %q config snapshot %s/%s: %w", runID, versionDir, configManifestName, err)
		}
		if err := writeAtomic(currentPath, currentContent); err != nil {
			return fmt.Errorf("run %q config snapshot %s: %w", runID, configCurrentName, err)
		}
		return nil
	})
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
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(path, content)
}
