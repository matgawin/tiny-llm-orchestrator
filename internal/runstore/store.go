package runstore

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Open returns a run store rooted at projectRoot.
func Open(projectRoot string) (*Store, error) {
	if projectRoot == "" {
		return nil, errors.New("project root is required")
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
