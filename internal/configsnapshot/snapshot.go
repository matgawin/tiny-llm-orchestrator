// Package configsnapshot builds durable run-start config snapshot files.
package configsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/vcs"
)

const (
	schemaVersion  = 1
	reasonRunStart = "run_start"
	reasonRefresh  = "refresh_config"
	hashAlgorithm  = "sha256"
)

// BuildInitial returns the version 1 run config snapshot files for project.
func BuildInitial(project *config.Project, workflowName string, at time.Time) (runstore.ConfigSnapshot, error) {
	if project == nil {
		return runstore.ConfigSnapshot{}, stableerr.Errorf("project config is required")
	}
	resolved, err := marshalResolved(project)
	if err != nil {
		return runstore.ConfigSnapshot{}, err
	}
	manifest, err := marshalManifest(project, workflowName, at)
	if err != nil {
		return runstore.ConfigSnapshot{}, err
	}
	return runstore.ConfigSnapshot{
		Version:  1,
		Resolved: resolved,
		Manifest: manifest,
	}, nil
}

// BuildRefresh returns the next run config snapshot files for an explicit refresh.
func BuildRefresh(project *config.Project, workflowName string, version int, source string, vcsSnapshot vcs.Snapshot, at time.Time) (runstore.ConfigSnapshot, error) {
	if project == nil {
		return runstore.ConfigSnapshot{}, stableerr.Errorf("project config is required")
	}
	if version <= 1 {
		return runstore.ConfigSnapshot{}, stableerr.Errorf("refresh config snapshot version = %d, want > 1", version)
	}
	if source == "" {
		return runstore.ConfigSnapshot{}, stableerr.Errorf("refresh config snapshot source is required")
	}
	versionDir := fmt.Sprintf("%06d", version)
	resolved, err := marshalResolved(project)
	if err != nil {
		return runstore.ConfigSnapshot{}, err
	}
	manifest, err := marshalManifestVersion(project, workflowName, version, versionDir, reasonRefresh, source, &vcsSnapshot, at)
	if err != nil {
		return runstore.ConfigSnapshot{}, err
	}
	return runstore.ConfigSnapshot{
		Version:  version,
		Resolved: resolved,
		Manifest: manifest,
	}, nil
}

// ManifestHash returns the SHA-256 hash over committed manifest.json bytes.
func ManifestHash(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:])
}

type resolvedSnapshot struct {
	SchemaVersion int             `json:"schema_version"`
	Project       *config.Project `json:"project"`
}

type manifestSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	Version       int               `json:"version"`
	VersionDir    string            `json:"version_dir"`
	CreatedAt     time.Time         `json:"created_at"`
	Reason        string            `json:"reason"`
	Source        string            `json:"source,omitempty"`
	Workflow      string            `json:"workflow"`
	HashAlgorithm string            `json:"hash_algorithm"`
	SourceFiles   []sourceFileEntry `json:"source_files"`
	VCSSnapshot   *vcs.Snapshot     `json:"vcs_snapshot,omitempty"`
	VCSHash       string            `json:"vcs_hash,omitempty"`
}

type sourceFileEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SourceID  string `json:"source_id,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
}

func marshalResolved(project *config.Project) ([]byte, error) {
	content, err := json.MarshalIndent(resolvedSnapshot{
		SchemaVersion: schemaVersion,
		Project:       project,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal resolved config snapshot: %w", err)
	}
	return append(content, '\n'), nil
}

func marshalManifest(project *config.Project, workflowName string, at time.Time) ([]byte, error) {
	return marshalManifestVersion(project, workflowName, 1, "000001", reasonRunStart, "", nil, at)
}

func marshalManifestVersion(project *config.Project, workflowName string, version int, versionDir, reason, source string, vcsSnapshot *vcs.Snapshot, at time.Time) ([]byte, error) {
	sourceFiles, err := collectSourceFiles(project)
	if err != nil {
		return nil, err
	}
	var vcsHash string
	if vcsSnapshot != nil {
		content, err := json.Marshal(vcsSnapshot)
		if err != nil {
			return nil, fmt.Errorf("marshal VCS refresh snapshot for manifest hash: %w", err)
		}
		sum := sha256.Sum256(content)
		vcsHash = hex.EncodeToString(sum[:])
	}
	content, err := json.MarshalIndent(manifestSnapshot{
		SchemaVersion: schemaVersion,
		Version:       version,
		VersionDir:    versionDir,
		CreatedAt:     normalizeTime(at),
		Reason:        reason,
		Source:        source,
		Workflow:      workflowName,
		HashAlgorithm: hashAlgorithm,
		SourceFiles:   sourceFiles,
		VCSSnapshot:   vcsSnapshot,
		VCSHash:       vcsHash,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config snapshot manifest: %w", err)
	}
	return append(content, '\n'), nil
}

func collectSourceFiles(project *config.Project) ([]sourceFileEntry, error) {
	var entries []sourceFileEntry
	add := func(path, sourceID, sourceRef string) error {
		if path == "" {
			return nil
		}
		if isRunStorePath(project.Root, path) {
			return nil
		}
		relPath := relativeSourcePath(project.Root, path)
		for _, entry := range entries {
			if entry.Path == relPath {
				return nil
			}
		}
		hash, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash config source %s: %w", relPath, err)
		}
		entries = append(entries, sourceFileEntry{
			Path:      relPath,
			SHA256:    hash,
			SourceID:  sourceID,
			SourceRef: sourceRef,
		})
		return nil
	}

	if err := add(filepath.Join(project.OrcDir, "config.yaml"), "project", ".orc/config.yaml"); err != nil {
		return nil, err
	}
	for name, workflow := range project.Workflows {
		if err := add(workflow.SourcePath, "workflow:"+name, project.Config.Workflows[name].Path); err != nil {
			return nil, err
		}
	}
	for id, agent := range project.Agents {
		if err := add(agent.SourcePath, "agent:"+id, project.Config.Agents[id]); err != nil {
			return nil, err
		}
	}
	for id, runtime := range project.Runtimes {
		if err := add(runtime.SourcePath, "runtime:"+id, project.Config.Runtimes[id]); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(entries, func(a, b sourceFileEntry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

func hashFile(path string) (string, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- config loader already resolved source files under validated .orc paths.
	if err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func relativeSourcePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func isRunStorePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == ".orc/runs" || strings.HasPrefix(rel, ".orc/runs/")
}

func normalizeTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}
