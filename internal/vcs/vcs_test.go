package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	wantJJRootCommand   = "jj root"
	wantJJStatusCommand = "jj status"
)

func TestInspectPreRunPrefersJJOverGit(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
case "$1" in
  root) printf '%s\n' "$PWD";;
  status) printf 'The working copy has no changes.\nWorking copy  (@) : abc\nParent commit (@-): def\n';;
  *) exit 2;;
esac
`,
		"git": `#!/bin/sh
printf 'git should not be used\n' >&2
exit 9
`,
	})
	t.Setenv("PATH", path)

	snapshot, err := InspectPreRun(context.Background(), Options{Root: root, Env: []string{"PATH=" + path}})
	if err != nil {
		t.Fatalf("InspectPreRun returned error: %v", err)
	}

	if snapshot.Kind != KindJJ {
		t.Fatalf("kind = %q, want %q", snapshot.Kind, KindJJ)
	}

	if snapshot.Dirty {
		t.Fatalf("dirty = true, want false")
	}

	if !strings.Contains(snapshot.Summary, "The working copy has no changes.") {
		t.Fatalf("summary = %q, want clean jj status output", snapshot.Summary)
	}

	if got := snapshot.Commands[len(snapshot.Commands)-1]; strings.Join(got, " ") != wantJJStatusCommand {
		t.Fatalf("last command = %v, want jj status", got)
	}
}

func TestInspectPreRunPropagatesCanceledProbe(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
sleep 5
`,
	})
	t.Setenv("PATH", path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := InspectPreRun(ctx, Options{Root: root, Env: []string{"PATH=" + path}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InspectPreRun error = %v, want context.Canceled", err)
	}
}

func TestInspectPreRunFallsBackToGit(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
printf 'Error: No jj repo found\n' >&2
exit 1
`,
		"git": `#!/bin/sh
case "$*" in
  "rev-parse --show-toplevel") printf '%s\n' "$PWD";;
  "status --porcelain=v1 -z --untracked-files=all") printf ' M internal/runstart/runstart.go\0?? docs/features/run-start.md\0R  new.md\0old.md\0?? docs/space path.md\0';;
  *) exit 2;;
esac
`,
	})
	t.Setenv("PATH", path)

	snapshot, err := InspectPreRun(context.Background(), Options{Root: root, Env: []string{"PATH=" + path}})
	if err != nil {
		t.Fatalf("InspectPreRun returned error: %v", err)
	}

	if snapshot.Kind != KindGit || !snapshot.Dirty {
		t.Fatalf("snapshot kind/dirty = %s/%t, want git/true", snapshot.Kind, snapshot.Dirty)
	}

	want := []string{"docs/features/run-start.md", "docs/space path.md", "internal/runstart/runstart.go", "new.md", "old.md"}
	assertStrings(t, snapshot.ChangedPaths, want)

	if snapshot.Summary != "Git working copy has 5 changed paths." {
		t.Fatalf("summary = %q, want git changed-path count", snapshot.Summary)
	}
}

func TestInspectPreRunRejectsBrokenProbe(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
printf 'permission denied reading repo metadata\n' >&2
exit 1
`,
	})
	t.Setenv("PATH", path)

	_, err := InspectPreRun(context.Background(), Options{Root: root, Env: []string{"PATH=" + path}})
	if err == nil {
		t.Fatal("InspectPreRun returned nil error, want broken probe failure")
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("InspectPreRun error = %v, want probe failure details", err)
	}
}

func TestInspectPreRunRejectsBrokenGitProbeAfterJJUnavailable(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
printf 'No jj repo found\n' >&2
exit 1
`,
		"git": `#!/bin/sh
printf 'permission denied reading git metadata\n' >&2
exit 1
`,
	})
	t.Setenv("PATH", path)

	_, err := InspectPreRun(context.Background(), Options{Root: root, Env: []string{"PATH=" + path}})
	if err == nil {
		t.Fatal("InspectPreRun returned nil error, want broken git probe failure")
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("InspectPreRun error = %v, want git probe failure details", err)
	}
}

func TestInspectPreRunNoVCS(t *testing.T) {
	root := t.TempDir()
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
printf 'No jj repo found\n' >&2
exit 1
`,
		"git": `#!/bin/sh
printf 'fatal: not a git repository\n' >&2
exit 1
`,
	})
	t.Setenv("PATH", path)

	snapshot, err := InspectPreRun(context.Background(), Options{Root: root, Env: []string{"PATH=" + path}})
	if err != nil {
		t.Fatalf("InspectPreRun returned error: %v", err)
	}

	if snapshot.Kind != KindNone || snapshot.Dirty {
		t.Fatalf("snapshot kind/dirty = %s/%t, want none/false", snapshot.Kind, snapshot.Dirty)
	}

	if snapshot.Summary != "No supported VCS detected." {
		t.Fatalf("summary = %q, want no-VCS summary", snapshot.Summary)
	}
}

func TestRecordPostRunWritesSnapshotArtifact(t *testing.T) {
	root := t.TempDir()

	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	run, err := store.Create(runstore.CreateRunRequest{RunID: "post-run", Workflow: "implementation"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
case "$1" in
  root) printf '%s\n' "$PWD";;
  status) printf 'Working copy changes:\nM internal/vcs/vcs.go\n';;
  *) exit 2;;
esac
`,
	})
	t.Setenv("PATH", path)

	ref, snapshot, err := RecordPostRun(context.Background(), store, run.ID, Options{
		Root: root,
		Env:  []string{"PATH=" + path},
		Time: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordPostRun returned error: %v", err)
	}

	if ref.Kind != runstore.KindSnapshot || !strings.Contains(ref.Path, "vcs-post-run.json") {
		t.Fatalf("artifact ref = %+v, want post-run snapshot", ref)
	}

	if snapshot.Phase != PhasePostRun {
		t.Fatalf("phase = %q, want %q", snapshot.Phase, PhasePostRun)
	}

	assertJJDirtySnapshot(t, snapshot)

	content, err := os.ReadFile(filepath.Join(run.Path, filepath.FromSlash(ref.Path)))
	if err != nil {
		t.Fatalf("read snapshot artifact: %v", err)
	}

	var persisted Snapshot
	if err := json.Unmarshal(content, &persisted); err != nil {
		t.Fatalf("unmarshal persisted snapshot: %v", err)
	}

	assertJJDirtySnapshot(t, persisted)
}

func assertJJDirtySnapshot(t *testing.T, snapshot Snapshot) {
	t.Helper()

	if snapshot.Kind != KindJJ || !snapshot.Dirty {
		t.Fatalf("snapshot kind/dirty = %s/%t, want jj/true", snapshot.Kind, snapshot.Dirty)
	}

	if !strings.Contains(snapshot.Summary, "Working copy changes:") {
		t.Fatalf("summary = %q, want jj status output", snapshot.Summary)
	}

	if len(snapshot.Commands) != 2 ||
		strings.Join(snapshot.Commands[0], " ") != wantJJRootCommand ||
		strings.Join(snapshot.Commands[1], " ") != wantJJStatusCommand {
		t.Fatalf("commands = %+v, want jj root and jj status", snapshot.Commands)
	}

	assertStrings(t, snapshot.ChangedPaths, []string{"internal/vcs/vcs.go"})
}

func TestParseJJChangedPathsDoesNotTrimPathCharacters(t *testing.T) {
	got := parseJJChangedPaths("Working copy changes:\nM README.md\nA Makefile\n")
	assertStrings(t, got, []string{"Makefile", "README.md"})
}

func fakeVCSPath(t *testing.T, scripts map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	return dir
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("strings = %+v, want %+v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("strings = %+v, want %+v", got, want)
		}
	}
}
