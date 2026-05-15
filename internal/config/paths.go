package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tiny-llm-orchestrator/orc/internal/stableerr"

	"github.com/goccy/go-yaml"
)

func readYAML(realOrcDir, path string, out any) error {
	content, err := readConfigFile(realOrcDir, path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(content, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func readConfigFile(realOrcDir, path string) ([]byte, error) {
	// This is the final guard before reading any config-owned file, including
	// .orc/config.yaml. Reference resolution performs earlier user-facing path
	// checks, but every read still enforces the resolved .orc boundary.
	if err := validateResolvedUnderDir(realOrcDir, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path) // #nosec G304
}

func resolveOrcRelativePath(orcDir, realOrcDir, relPath string) (string, error) {
	if relPath == "" {
		return "", stableerr.New("path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", stableerr.New("path must be relative to .orc")
	}
	clean := filepath.Clean(relPath)
	if invalidBaseRelativePath(clean) {
		return "", stableerr.New("path must not escape .orc")
	}
	path := filepath.Join(orcDir, clean)
	// Resolve referenced workflow/agent paths early so path errors are reported
	// against the config reference before the file read guard runs.
	if err := validateResolvedUnderDir(realOrcDir, path); err != nil {
		return "", err
	}
	return path, nil
}

func validateResolvedUnderDir(realDir, path string) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil {
		return fmt.Errorf("resolve path relative to .orc: %w", err)
	}
	if invalidBaseRelativePath(rel) {
		return stableerr.New("path must not escape .orc")
	}
	return nil
}

func invalidBaseRelativePath(rel string) bool {
	return rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
