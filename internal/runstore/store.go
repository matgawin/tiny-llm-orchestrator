package runstore

import (
	"fmt"
	"path/filepath"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// Open returns a run store rooted at projectRoot.
func Open(projectRoot string) (*Store, error) {
	if projectRoot == "" {
		return nil, stableerr.New("project root is required")
	}

	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	return &Store{
		orcDir:  filepath.Join(root, orcDirName),
		runsDir: filepath.Join(root, orcDirName, runsDirName),
	}, nil
}
