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
)

const (
	schemaVersion  = 1
	reasonRunStart = "run_start"
	hashAlgorithm  = "sha256"
)

// BuildInitial returns the version 1 run config snapshot files for project.
func BuildInitial(project *config.Project, workflowName string, at time.Time) (runstore.ConfigSnapshot, error) {
	if project == nil {
		return runstore.ConfigSnapshot{}, fmt.Errorf("project config is required")
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
	Workflow      string            `json:"workflow"`
	HashAlgorithm string            `json:"hash_algorithm"`
	SourceFiles   []sourceFileEntry `json:"source_files"`
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
	sourceFiles, err := collectSourceFiles(project)
	if err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(manifestSnapshot{
		SchemaVersion: schemaVersion,
		Version:       1,
		VersionDir:    "000001",
		CreatedAt:     normalizeTime(at),
		Reason:        reasonRunStart,
		Workflow:      workflowName,
		HashAlgorithm: hashAlgorithm,
		SourceFiles:   sourceFiles,
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
		return "", err
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
