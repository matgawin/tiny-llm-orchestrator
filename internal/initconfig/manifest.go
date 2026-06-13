package initconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"tiny-llm-orchestrator/orc/internal/config"

	"github.com/goccy/go-yaml"
)

const scaffoldManifestPath = ".orc/scaffold.lock.yaml"

var (
	errUnsupportedScaffoldManifest = errors.New("manifest has unsupported version or setup_version")
	errInvalidScaffoldManifestFile = errors.New("manifest contains an invalid file entry")
	errDuplicateScaffoldManifest   = errors.New("manifest contains duplicate file entries")
)

// ScaffoldManifestFile records one Orc-managed scaffold content identity.
type ScaffoldManifestFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// ScaffoldManifest is the v1 project-local scaffold ownership manifest.
type ScaffoldManifest struct {
	Version      int                    `yaml:"version"`
	SetupVersion int                    `yaml:"setup_version"`
	Files        []ScaffoldManifestFile `yaml:"files"`
}

// ScaffoldManifestPath returns the project-relative scaffold ownership
// manifest path.
func ScaffoldManifestPath() string {
	return scaffoldManifestPath
}

// ManagedScaffoldFiles returns the scaffold files tracked by the ownership
// manifest. The manifest intentionally excludes config, .orc/runs, .gitignore,
// and AGENTS.md.
func ManagedScaffoldFiles(files []ScaffoldFile) []ScaffoldFile {
	var managed []ScaffoldFile

	for _, file := range files {
		if !IsManagedScaffoldManifestPath(file.Path) {
			continue
		}

		managed = append(managed, ScaffoldFile{
			Path:    file.Path,
			Content: append([]byte(nil), file.Content...),
		})
	}

	slices.SortFunc(managed, func(a, b ScaffoldFile) int { return strings.Compare(a.Path, b.Path) })

	return managed
}

// IsManagedScaffoldManifestPath reports whether path is in the manifest-owned
// scaffold descriptor scope.
func IsManagedScaffoldManifestPath(path string) bool {
	return strings.HasPrefix(path, ".orc/agents/") ||
		strings.HasPrefix(path, ".orc/workflows/") ||
		strings.HasPrefix(path, ".orc/runtimes/")
}

// ScaffoldManifestContent renders the v1 scaffold ownership manifest.
func ScaffoldManifestContent(files []ScaffoldManifestFile) []byte {
	entries := append([]ScaffoldManifestFile(nil), files...)
	slices.SortFunc(entries, func(a, b ScaffoldManifestFile) int { return strings.Compare(a.Path, b.Path) })

	var out strings.Builder
	out.WriteString("version: 1\n")
	out.WriteString("setup_version: ")
	out.WriteString(strconv.Itoa(config.CurrentSetupVersion))
	out.WriteString("\n")
	out.WriteString("files:\n")

	for _, file := range entries {
		out.WriteString("  - path: ")
		out.WriteString(file.Path)
		out.WriteString("\n")
		out.WriteString("    sha256: ")
		out.WriteString(file.SHA256)
		out.WriteString("\n")
	}

	return []byte(out.String())
}

// ManifestFilesForScaffold returns manifest entries for exact scaffold bytes.
func ManifestFilesForScaffold(files []ScaffoldFile) []ScaffoldManifestFile {
	out := make([]ScaffoldManifestFile, 0, len(files))
	for _, file := range files {
		out = append(out, ScaffoldManifestFile{
			Path:   file.Path,
			SHA256: SHA256Hex(file.Content),
		})
	}

	slices.SortFunc(out, func(a, b ScaffoldManifestFile) int { return strings.Compare(a.Path, b.Path) })

	return out
}

// SHA256Hex returns the SHA-256 hex digest of exact file bytes.
func SHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ParseScaffoldManifest parses and validates the v1 scaffold ownership
// manifest. Returned hashes are keyed by project-relative path.
func ParseScaffoldManifest(content []byte) (ScaffoldManifest, map[string]string, error) {
	var manifest ScaffoldManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return ScaffoldManifest{}, nil, fmt.Errorf("parse scaffold manifest: %w", err)
	}

	if manifest.Version != 1 || manifest.SetupVersion != config.CurrentSetupVersion {
		return ScaffoldManifest{}, nil, errUnsupportedScaffoldManifest
	}

	hashByPath := make(map[string]string, len(manifest.Files))
	for i, file := range manifest.Files {
		normalizedSHA256 := strings.ToLower(file.SHA256)
		if !IsManagedScaffoldManifestPath(file.Path) || !isSHA256Hex(normalizedSHA256) {
			return ScaffoldManifest{}, nil, errInvalidScaffoldManifestFile
		}

		if _, ok := hashByPath[file.Path]; ok {
			return ScaffoldManifest{}, nil, errDuplicateScaffoldManifest
		}

		manifest.Files[i].SHA256 = normalizedSHA256
		hashByPath[file.Path] = normalizedSHA256
	}

	return manifest, hashByPath, nil
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}

	_, err := hex.DecodeString(value)

	return err == nil
}
