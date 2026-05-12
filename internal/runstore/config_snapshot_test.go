package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitialConfigSnapshotPersistsVersionedFilesAndCurrentPointer(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, err := store.Create(CreateRunRequest{RunID: "config-snapshot-run", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	err = store.WriteInitialConfigSnapshot(run.ID, ConfigSnapshot{
		Version:  1,
		Resolved: []byte("{\"schema_version\":1,\"project\":{}}\n"),
		Manifest: []byte("{\"schema_version\":1,\"version\":1,\"version_dir\":\"000001\"}\n"),
	})
	if err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}

	assertFile(t, filepath.Join(run.Path, "config", "000001", "resolved.json"))
	assertFile(t, filepath.Join(run.Path, "config", "000001", "manifest.json"))
	currentContent := readFile(t, filepath.Join(run.Path, "config", "current.json"))
	var current configCurrent
	if err := json.Unmarshal(currentContent, &current); err != nil {
		t.Fatalf("unmarshal current.json: %v\n%s", err, string(currentContent))
	}
	if current.SchemaVersion != 1 || current.Version != 1 || current.VersionDir != "000001" {
		t.Fatalf("current = %+v, want version 1 000001", current)
	}
	info, err := os.Lstat(filepath.Join(run.Path, "config", "current.json"))
	if err != nil {
		t.Fatalf("lstat current.json: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("current.json mode = %s, want regular non-symlink file", info.Mode())
	}
}

func TestWriteInitialConfigSnapshotRejectsSymlinkedConfigDir(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	run, err := store.Create(CreateRunRequest{RunID: "config-symlink-run", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	outside := filepath.Join(root, "outside-config")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatalf("mkdir outside config: %v", err)
	}
	symlinkPath(t, filepath.Join(run.Path, "config"), outside)

	err = store.WriteInitialConfigSnapshot(run.ID, ConfigSnapshot{
		Version:  1,
		Resolved: []byte("{\"schema_version\":1}\n"),
		Manifest: []byte("{\"schema_version\":1}\n"),
	})
	requireErrorContains(t, err, "symlink")
	if _, statErr := os.Lstat(filepath.Join(outside, "current.json")); !os.IsNotExist(statErr) {
		t.Fatalf("outside current stat err = %v, want no write through symlink", statErr)
	}
}

func TestWriteInitialConfigSnapshotRejectsExistingCurrent(t *testing.T) {
	store := openStore(t, t.TempDir())
	run, err := store.Create(CreateRunRequest{RunID: "config-existing-current-run", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.Path, "config", "current.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write current: %v", err)
	}

	err = store.WriteInitialConfigSnapshot(run.ID, ConfigSnapshot{
		Version:  1,
		Resolved: []byte("{\"schema_version\":1}\n"),
		Manifest: []byte("{\"schema_version\":1}\n"),
	})
	requireErrorContains(t, err, "current.json already exists")
}
