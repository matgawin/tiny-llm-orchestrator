package runcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

func TestLoadUsesPinnedSnapshotAfterLiveWorkflowMutation(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{MarkdownFallback: true})
	store := openContextStore(t, root)
	run := createContextRun(t, store, "snapshot-context-run")
	writeContextConfigSnapshot(t, root, store, run.ID)
	writeContextFile(t, filepath.Join(root, ".orc", "workflows", "implementation.yaml"), `name: implementation
start: review
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  runtime: codex
  retries: {}
steps:
  review:
    agent: reviewer
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`)

	loaded, err := Load(root, run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.ConfigSnapshotVersion != 1 || loaded.ConfigSnapshotVersionDir != "000001" {
		t.Fatalf("snapshot version = %d/%q, want 1/000001", loaded.ConfigSnapshotVersion, loaded.ConfigSnapshotVersionDir)
	}

	if loaded.Workflow.Start != "plan" {
		t.Fatalf("workflow start = %q, want pinned snapshot start %q", loaded.Workflow.Start, "plan")
	}

	if _, ok := loaded.Workflow.Steps["review"]; ok {
		t.Fatalf("loaded live-mutated review step, want pinned snapshot workflow")
	}
}

func TestLoadMissingSnapshotCurrentFailsWithRunAndPath(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{MarkdownFallback: true})
	store := openContextStore(t, root)
	run := createContextRun(t, store, "missing-current-run")

	_, err := Load(root, run.ID)
	if err == nil {
		t.Fatal("Load returned nil error, want missing snapshot error")
	}

	wantPath := filepath.ToSlash(filepath.Join(root, ".orc", "runs", run.ID, "config", "current.json"))
	for _, want := range []string{`run "missing-current-run" config snapshot`, wantPath, "no such file or directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
}

func openContextStore(t *testing.T, root string) *runstore.Store {
	t.Helper()

	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	return store
}

func createContextRun(t *testing.T, store *runstore.Store, runID string) *runstore.Run {
	t.Helper()

	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    runID,
		Workflow: "implementation",
		Time:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	return run
}

func writeContextConfigSnapshot(t *testing.T, root string, store *runstore.Store, runID string) {
	t.Helper()

	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}

	snapshot, err := configsnapshot.BuildInitial(project, "implementation", time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}

	if err := store.WriteInitialConfigSnapshot(runID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
}

func writeContextFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
