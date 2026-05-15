package runstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

type stagedArtifact struct {
	commit   func() error
	rollback func() error
	cleanup  func()
}

func stageArtifact(path string, artifact Artifact) (stagedArtifact, error) {
	if err := validateArtifactFile(path); err != nil {
		return stagedArtifact{}, err
	}
	existing, readErr := os.ReadFile(path) // #nosec G304 -- path is scoped to the run directory.
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return stagedArtifact{}, readErr
	}
	if existed && artifact.Kind != KindFollowup {
		return stagedArtifact{}, stableerr.Errorf("artifact %s already exists", filepath.Base(path))
	}
	content := artifact.Content
	if artifact.Kind == KindFollowup {
		content = append(append([]byte(nil), existing...), artifact.Content...)
	}
	tempName, err := writeStagedFile(path, content)
	if err != nil {
		return stagedArtifact{}, err
	}
	cleanup := func() {
		_ = os.Remove(tempName) // #nosec G703 -- tempName was created under the scoped artifact directory.
	}
	commit := func() error {
		if err := os.Rename(tempName, path); err != nil { // #nosec G703 -- both paths are scoped to the run directory.
			return fmt.Errorf("replace %s: %w", path, err)
		}
		return nil
	}
	rollback := func() error {
		if existed {
			return writeAtomic(path, existing)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return stagedArtifact{
		commit:   commit,
		rollback: rollback,
		cleanup:  cleanup,
	}, nil
}

func stageArtifactFromFile(path string, artifact Artifact, sourcePath string) (stagedArtifact, error) {
	if artifact.Kind == KindFollowup {
		return stagedArtifact{}, stableerr.Errorf("artifact kind %q cannot be written from file", artifact.Kind)
	}
	if err := validateArtifactFile(path); err != nil {
		return stagedArtifact{}, err
	}
	if err := validateRegularFile(sourcePath, "artifact source "+filepath.Base(sourcePath)); err != nil {
		return stagedArtifact{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		return stagedArtifact{}, stableerr.Errorf("artifact %s already exists", filepath.Base(path))
	} else if !os.IsNotExist(err) {
		return stagedArtifact{}, err
	}
	tempName, err := writeStagedFileFromFile(path, sourcePath)
	if err != nil {
		return stagedArtifact{}, err
	}
	cleanup := func() {
		_ = os.Remove(tempName) // #nosec G703 -- tempName was created under the scoped artifact directory.
	}
	commit := func() error {
		if err := os.Rename(tempName, path); err != nil { // #nosec G703 -- both paths are scoped to the run directory.
			return fmt.Errorf("replace %s: %w", path, err)
		}
		return nil
	}
	rollback := func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return stagedArtifact{
		commit:   commit,
		rollback: rollback,
		cleanup:  cleanup,
	}, nil
}

func writeStagedFile(path string, content []byte) (string, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp") // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	cleanup := func(closeFile bool) {
		if closeFile {
			_ = temp.Close()
		}
		_ = os.Remove(tempName) // #nosec G703 -- tempName comes from os.CreateTemp in the target directory.
	}
	if _, err := temp.Write(content); err != nil {
		cleanup(true)
		return "", err
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup(true)
		return "", err
	}
	if err := temp.Close(); err != nil {
		cleanup(false)
		return "", err
	}
	return tempName, nil
}

func writeStagedFileFromFile(path, sourcePath string) (string, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	source, err := os.OpenFile(sourcePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- sourcePath is validated as a regular launcher-owned artifact source.
	if err != nil {
		return "", err
	}
	defer func() {
		_ = source.Close()
	}()
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp") // #nosec G304 -- path is scoped to the run directory.
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	cleanup := func(closeFile bool) {
		if closeFile {
			_ = temp.Close()
		}
		_ = os.Remove(tempName) // #nosec G703 -- tempName comes from os.CreateTemp in the target directory.
	}
	if _, err := io.Copy(temp, source); err != nil {
		cleanup(true)
		return "", err
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup(true)
		return "", err
	}
	if err := temp.Close(); err != nil {
		cleanup(false)
		return "", err
	}
	return tempName, nil
}

func writeAtomic(path string, content []byte) error {
	tempName, err := writeStagedFile(path, content)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tempName) // #nosec G703 -- tempName was created under the scoped destination directory.
	}()
	if err := os.Rename(tempName, path); err != nil { // #nosec G703 -- both paths are scoped to the destination directory.
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func ensureArtifactParentDir(runDir, relPath string) error {
	return checkArtifactParentDir(runDir, relPath, true)
}

func validateArtifactParentDir(runDir, relPath string) error {
	return checkArtifactParentDir(runDir, relPath, false)
}

func checkArtifactParentDir(runDir, relPath string, createMissing bool) error {
	if err := validateRelativeArtifactPath(relPath); err != nil {
		return err
	}
	if err := validateDir(runDir); err != nil {
		return err
	}
	current := runDir
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relPath)))
	if parent == "." {
		return nil
	}
	for component := range strings.SplitSeq(parent, "/") {
		current = filepath.Join(current, component)
		displayPath := filepath.ToSlash(componentPath(runDir, current))
		info, err := os.Lstat(current) // #nosec G703 -- current is built from validated relative artifact path components under runDir.
		if errorsIsNotExist(err) {
			if !createMissing {
				return fmt.Errorf("artifact parent %s: %w", displayPath, err)
			}
			if err := os.Mkdir(current, 0o750); err != nil { // #nosec G703 -- current is built from validated relative artifact path components under runDir.
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("artifact parent %s: %w", displayPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return stableerr.Errorf("artifact parent %s is a symlink", displayPath)
		}
		if !info.IsDir() {
			return stableerr.Errorf("artifact parent %s is not a directory", displayPath)
		}
	}
	return nil
}

func ensureRunsDir(orcDir, runsDir string) error {
	if err := ensureDir(orcDir); err != nil {
		return fmt.Errorf("%s: %w", orcDirName, err)
	}
	if err := ensureDir(runsDir); err != nil {
		return fmt.Errorf("%s: %w", filepath.ToSlash(filepath.Join(orcDirName, runsDirName)), err)
	}
	return nil
}

func validateRunsDir(orcDir, runsDir string) error {
	if err := validateDir(orcDir); err != nil {
		return fmt.Errorf("%s: %w", orcDirName, err)
	}
	if err := validateDir(runsDir); err != nil {
		return fmt.Errorf("%s: %w", filepath.ToSlash(filepath.Join(orcDirName, runsDirName)), err)
	}
	return nil
}

func validateRunLayout(runDir string) error {
	for _, dir := range artifactDirs() {
		if err := validateDir(filepath.Join(runDir, dir)); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	if err := validateRegularFile(filepath.Join(runDir, followupsName), followupsName); err != nil {
		return err
	}
	return nil
}

func ensureDir(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- caller validates run-store scoped paths before directory checks.
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o750); err != nil && !os.IsExist(err) { // #nosec G703 -- caller validates run-store scoped paths before directory creation.
			return err
		}
		return validateDir(path)
	}
	if err != nil {
		return err
	}
	return validateDirInfo(path, info)
}

func validateDir(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- caller validates run-store scoped paths before directory checks.
	if err != nil {
		return err
	}
	return validateDirInfo(path, info)
}

func validateDirInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return stableerr.Errorf("%s is a symlink", path)
	}
	if !info.IsDir() {
		return stableerr.Errorf("%s is not a directory", path)
	}
	return nil
}

func validateArtifactFile(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- caller validates run-store scoped paths before file checks.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateFileInfo("artifact "+filepath.Base(path), info)
}

func validateRegularFile(path, name string) error {
	info, err := os.Lstat(path) // #nosec G703 -- caller validates run-store scoped paths before file checks.
	if err != nil {
		return err
	}
	return validateFileInfo(name, info)
}

func validateFileInfo(name string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return stableerr.Errorf("%s is a symlink", name)
	}
	if info.IsDir() {
		return stableerr.Errorf("%s is a directory", name)
	}
	if !info.Mode().IsRegular() {
		return stableerr.Errorf("%s is not a regular file", name)
	}
	return nil
}

func componentPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
