package configsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const (
	configDirName      = "config"
	configCurrentName  = "current.json"
	configResolvedName = "resolved.json"
	configManifestName = "manifest.json"
	versionDirLength   = 6
)

// LoadedSnapshot is the validated, current config snapshot for an existing run.
type LoadedSnapshot struct {
	Version    int
	VersionDir string
	Project    *config.Project
}

// Inspection is the current snapshot metadata operators can inspect without
// loading live .orc files.
type Inspection struct {
	Version               int
	VersionDir            string
	ResolvedPath          string
	ManifestPath          string
	CreatedAt             time.Time
	ManifestHashAlgorithm string
	ManifestHash          string
	SourceHashAlgorithm   string
	SourceFileCount       int
	SourceHash            string
}

type currentSnapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Version       int    `json:"version"`
	VersionDir    string `json:"version_dir"`
}

// LoadCurrent loads the run's pinned current config snapshot. It never falls
// back to live .orc files; missing or corrupt snapshots are run-store errors.
func LoadCurrent(run *runstore.Run) (LoadedSnapshot, error) {
	if run == nil {
		return LoadedSnapshot{}, stableerr.Errorf("run is required")
	}

	configDir := filepath.Join(run.Path, configDirName)
	currentPath := filepath.Join(configDir, configCurrentName)

	current, err := readCurrent(run.ID, currentPath)
	if err != nil {
		return LoadedSnapshot{}, err
	}

	versionPath := filepath.Join(configDir, current.VersionDir)
	resolvedPath := filepath.Join(versionPath, configResolvedName)
	manifestPath := filepath.Join(versionPath, configManifestName)

	project, err := readResolved(run.ID, resolvedPath)
	if err != nil {
		return LoadedSnapshot{}, err
	}

	if err := validateManifest(run.ID, manifestPath, current, run.Status.Workflow); err != nil {
		return LoadedSnapshot{}, err
	}

	if project.Workflows == nil {
		return LoadedSnapshot{}, snapshotPathError(run.ID, resolvedPath, "project workflows are missing")
	}

	if _, ok := project.Workflows[run.Status.Workflow]; !ok {
		return LoadedSnapshot{}, snapshotPathError(run.ID, resolvedPath, fmt.Sprintf("workflow %q from run is not present in resolved project", run.Status.Workflow))
	}

	return LoadedSnapshot{
		Version:    current.Version,
		VersionDir: current.VersionDir,
		Project:    project,
	}, nil
}

// InspectCurrent loads current snapshot audit metadata without reading live
// project config or the snapshot resolved runtime contract.
func InspectCurrent(run *runstore.Run) (Inspection, error) {
	if run == nil {
		return Inspection{}, stableerr.Errorf("run is required")
	}

	configDir := filepath.Join(run.Path, configDirName)
	currentPath := filepath.Join(configDir, configCurrentName)

	current, err := readCurrent(run.ID, currentPath)
	if err != nil {
		return Inspection{}, err
	}

	versionPath := filepath.Join(configDir, current.VersionDir)
	manifestPath := filepath.Join(versionPath, configManifestName)

	content, manifest, err := readManifest(run.ID, manifestPath)
	if err != nil {
		return Inspection{}, err
	}

	if manifest.Version != current.Version {
		return Inspection{}, snapshotPathError(run.ID, manifestPath, fmt.Sprintf("version = %d, want %d", manifest.Version, current.Version))
	}

	if manifest.VersionDir != current.VersionDir {
		return Inspection{}, snapshotPathError(run.ID, manifestPath, fmt.Sprintf("version_dir = %q, want %q", manifest.VersionDir, current.VersionDir))
	}

	return Inspection{
		Version:               current.Version,
		VersionDir:            current.VersionDir,
		ResolvedPath:          filepath.ToSlash(filepath.Join("config", current.VersionDir, configResolvedName)),
		ManifestPath:          filepath.ToSlash(filepath.Join("config", current.VersionDir, configManifestName)),
		CreatedAt:             manifest.CreatedAt,
		ManifestHashAlgorithm: hashAlgorithm,
		ManifestHash:          ManifestHash(content),
		SourceHashAlgorithm:   manifest.HashAlgorithm,
		SourceFileCount:       len(manifest.SourceFiles),
		SourceHash:            sourceHash(manifest.SourceFiles),
	}, nil
}

func readCurrent(runID, path string) (currentSnapshot, error) {
	content, err := readRegularSnapshotFile(path)
	if err != nil {
		return currentSnapshot{}, snapshotPathError(runID, path, err.Error())
	}

	var current currentSnapshot
	if err := json.Unmarshal(content, &current); err != nil {
		return currentSnapshot{}, snapshotPathError(runID, path, fmt.Sprintf("decode current snapshot: %v", err))
	}

	if current.SchemaVersion != schemaVersion {
		return currentSnapshot{}, snapshotPathError(runID, path, fmt.Sprintf("schema_version = %d, want %d", current.SchemaVersion, schemaVersion))
	}

	if current.Version <= 0 {
		return currentSnapshot{}, snapshotPathError(runID, path, "version must be positive")
	}

	wantDir := fmt.Sprintf("%06d", current.Version)
	if current.VersionDir != wantDir {
		return currentSnapshot{}, snapshotPathError(runID, path, fmt.Sprintf("version_dir = %q, want %q", current.VersionDir, wantDir))
	}

	if err := validateVersionDirName(current.VersionDir); err != nil {
		return currentSnapshot{}, snapshotPathError(runID, path, err.Error())
	}

	return current, nil
}

func readResolved(runID, path string) (*config.Project, error) {
	content, err := readRegularSnapshotFile(path)
	if err != nil {
		return nil, snapshotPathError(runID, path, err.Error())
	}

	var resolved resolvedSnapshot
	if err := json.Unmarshal(content, &resolved); err != nil {
		return nil, snapshotPathError(runID, path, fmt.Sprintf("decode resolved snapshot: %v", err))
	}

	if resolved.SchemaVersion != schemaVersion {
		return nil, snapshotPathError(runID, path, fmt.Sprintf("schema_version = %d, want %d", resolved.SchemaVersion, schemaVersion))
	}

	if resolved.Project == nil {
		return nil, snapshotPathError(runID, path, "project is missing")
	}

	return resolved.Project, nil
}

func validateManifest(runID, path string, current currentSnapshot, workflowName string) error {
	_, manifest, err := readManifest(runID, path)
	if err != nil {
		return err
	}

	if manifest.Version != current.Version {
		return snapshotPathError(runID, path, fmt.Sprintf("version = %d, want %d", manifest.Version, current.Version))
	}

	if manifest.VersionDir != current.VersionDir {
		return snapshotPathError(runID, path, fmt.Sprintf("version_dir = %q, want %q", manifest.VersionDir, current.VersionDir))
	}

	if manifest.Workflow != workflowName {
		return snapshotPathError(runID, path, fmt.Sprintf("workflow = %q, want run workflow %q", manifest.Workflow, workflowName))
	}

	return nil
}

func readManifest(runID, path string) ([]byte, manifestSnapshot, error) {
	content, err := readRegularSnapshotFile(path)
	if err != nil {
		return nil, manifestSnapshot{}, snapshotPathError(runID, path, err.Error())
	}

	var manifest manifestSnapshot
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, manifestSnapshot{}, snapshotPathError(runID, path, fmt.Sprintf("decode manifest: %v", err))
	}

	if manifest.SchemaVersion != schemaVersion {
		return nil, manifestSnapshot{}, snapshotPathError(runID, path, fmt.Sprintf("schema_version = %d, want %d", manifest.SchemaVersion, schemaVersion))
	}

	return content, manifest, nil
}

func sourceHash(entries []sourceFileEntry) string {
	sum := sha256.New()
	for _, entry := range entries {
		sum.Write([]byte(entry.Path))
		sum.Write([]byte{0})
		sum.Write([]byte(strings.ToLower(entry.SHA256)))
		sum.Write([]byte{0})
	}

	return hex.EncodeToString(sum.Sum(nil))
}

func readRegularSnapshotFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read regular snapshot file: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, stableerr.Errorf("not a regular file")
	}

	content, err := os.ReadFile(path) // #nosec G304 -- path is derived from a validated run directory.
	if err != nil {
		return nil, fmt.Errorf("read config snapshot file %s: %w", path, err)
	}

	return content, nil
}

func validateVersionDirName(name string) error {
	if len(name) != versionDirLength {
		return stableerr.Errorf("invalid version_dir %q", name)
	}

	if _, err := strconv.Atoi(name); err != nil {
		return stableerr.Errorf("invalid version_dir %q", name)
	}

	return nil
}

func snapshotPathError(runID, path, detail string) error {
	return stableerr.Errorf("run %q config snapshot %s: %s", runID, filepath.ToSlash(path), detail)
}
