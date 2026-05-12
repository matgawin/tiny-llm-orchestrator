package runconfigrefresh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestRefreshPublishesNextSnapshotAndEvent(t *testing.T) {
	root, store, runID := writeRefreshRun(t)
	mutateWorkflow(t, root, `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 45m
  report_exit_grace: 10s
  runtime: codex
  retries:
    failed/error: 3
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
      failed: [error]
    on:
      done/ready: review
      failed/error: blocked_for_human
  review:
    agent: planner
    allowed_results:
      done: [approved]
    on:
      done/approved: ready_for_human
`)

	result, err := Refresh(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Env:   []string{"PATH="},
		Time:  fixedRefreshTime(),
	})
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if result.OldVersionDir != "000001" || result.NewVersionDir != "000002" {
		t.Fatalf("versions = %s -> %s, want 000001 -> 000002", result.OldVersionDir, result.NewVersionDir)
	}
	current := readRefreshCurrent(t, root, runID)
	if current.Version != 2 || current.VersionDir != "000002" {
		t.Fatalf("current = %+v, want version 2 000002", current)
	}
	resolved := readRefreshResolved(t, root, runID, "000002")
	workflowConfig := resolved.Project.Workflows["implementation"]
	if workflowConfig.Defaults.Timeout.Duration != 45*time.Minute {
		t.Fatalf("refreshed timeout = %s, want 45m", workflowConfig.Defaults.Timeout.Duration)
	}
	manifestContent := readRefreshFile(t, filepath.Join(root, ".orc", "runs", runID, "config", "000002", "manifest.json"))
	var manifest struct {
		Version       int    `json:"version"`
		VersionDir    string `json:"version_dir"`
		Reason        string `json:"reason"`
		Source        string `json:"source"`
		HashAlgorithm string `json:"hash_algorithm"`
		VCSSnapshot   struct {
			Phase string `json:"phase"`
			Kind  string `json:"kind"`
		} `json:"vcs_snapshot"`
		VCSHash string `json:"vcs_hash"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.Version != 2 || manifest.VersionDir != "000002" || manifest.Reason != "refresh_config" || manifest.Source != "cli" {
		t.Fatalf("manifest = %+v, want refresh version 2 source cli", manifest)
	}
	if manifest.VCSSnapshot.Phase != "config_refresh" || manifest.VCSSnapshot.Kind != "none" || manifest.VCSHash == "" {
		t.Fatalf("manifest vcs = %+v hash %q, want refresh none snapshot with hash", manifest.VCSSnapshot, manifest.VCSHash)
	}
	sum := sha256.Sum256(manifestContent)
	wantManifestHash := hex.EncodeToString(sum[:])
	if result.ManifestHash != wantManifestHash {
		t.Fatalf("manifest hash = %s, want %s", result.ManifestHash, wantManifestHash)
	}
	loaded, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	event := loaded.Events[len(loaded.Events)-1]
	if event.Type != runstore.EventConfigSnapshotRefreshed {
		t.Fatalf("last event type = %q, want %q", event.Type, runstore.EventConfigSnapshotRefreshed)
	}
	var payload struct {
		OldVersion            int    `json:"old_version"`
		OldVersionDir         string `json:"old_version_dir"`
		NewVersion            int    `json:"new_version"`
		NewVersionDir         string `json:"new_version_dir"`
		ManifestHashAlgorithm string `json:"manifest_hash_algorithm"`
		ManifestHash          string `json:"manifest_hash"`
		Source                string `json:"source"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal refresh payload: %v", err)
	}
	if payload.OldVersion != 1 || payload.OldVersionDir != "000001" || payload.NewVersion != 2 || payload.NewVersionDir != "000002" ||
		payload.ManifestHashAlgorithm != "sha256" || payload.ManifestHash != wantManifestHash || payload.Source != "cli" {
		t.Fatalf("payload = %+v, want refresh event details", payload)
	}
}

func TestRefreshRejectsActiveAttempt(t *testing.T) {
	root, store, runID := writeRefreshRun(t)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:                "plan",
		AgentID:               "planner",
		AttemptID:             "attempt-active",
		ConfigSnapshotVersion: 1,
		Timeout:               time.Minute,
		ReportExitGrace:       time.Second,
		Time:                  fixedRefreshTime(),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	_, err := Refresh(context.Background(), Options{Root: root, RunID: runID, Env: []string{"PATH="}})
	if err == nil || !strings.Contains(err.Error(), "active attempt") {
		t.Fatalf("Refresh error = %v, want active attempt rejection", err)
	}
	current := readRefreshCurrent(t, root, runID)
	if current.Version != 1 || current.VersionDir != "000001" {
		t.Fatalf("current = %+v, want unchanged initial snapshot", current)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".orc", "runs", runID, "config", "000002")); !os.IsNotExist(statErr) {
		t.Fatalf("000002 stat err = %v, want no published refresh snapshot", statErr)
	}
}

func TestRefreshRejectsSelectedStepAllowedResultRemoval(t *testing.T) {
	root, _, runID := writeRefreshRun(t)
	mutateWorkflow(t, root, `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  runtime: codex
  retries:
    {}
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`)

	_, err := Refresh(context.Background(), Options{Root: root, RunID: runID, Env: []string{"PATH="}})
	if err == nil || !strings.Contains(err.Error(), `allowed result pair "failed/error" is no longer declared for selected step "plan"`) {
		t.Fatalf("Refresh error = %v, want selected step allowed result rejection", err)
	}
	current := readRefreshCurrent(t, root, runID)
	if current.Version != 1 || current.VersionDir != "000001" {
		t.Fatalf("current = %+v, want unchanged initial snapshot", current)
	}
}

func TestRefreshRejectsIncompatibleWorkflowChange(t *testing.T) {
	root, _, runID := writeRefreshRun(t)
	mutateWorkflow(t, root, `name: implementation
start: code
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  runtime: codex
  retries:
    {}
steps:
  code:
    agent: planner
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`)

	_, err := Refresh(context.Background(), Options{Root: root, RunID: runID, Env: []string{"PATH="}})
	if err == nil || !strings.Contains(err.Error(), `"plan" is not declared`) {
		t.Fatalf("Refresh error = %v, want missing current step rejection", err)
	}
	current := readRefreshCurrent(t, root, runID)
	if current.Version != 1 || current.VersionDir != "000001" {
		t.Fatalf("current = %+v, want unchanged initial snapshot", current)
	}
}

func writeRefreshRun(t *testing.T) (string, *runstore.Store, string) {
	t.Helper()
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		FailedResults:    []string{"error"},
		Retries:          map[string]int{"failed/error": 1},
	})
	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open store returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "refresh-run",
		Workflow:     "implementation",
		InitialState: "plan",
		Time:         fixedRefreshTime().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	snapshot, err := configsnapshot.BuildInitial(project, "implementation", fixedRefreshTime().Add(-time.Hour))
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}
	if err := store.WriteInitialConfigSnapshot(run.ID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
	return root, store, run.ID
}

func mutateWorkflow(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

func readRefreshCurrent(t *testing.T, root, runID string) struct {
	SchemaVersion int    `json:"schema_version"`
	Version       int    `json:"version"`
	VersionDir    string `json:"version_dir"`
} {
	t.Helper()
	var current struct {
		SchemaVersion int    `json:"schema_version"`
		Version       int    `json:"version"`
		VersionDir    string `json:"version_dir"`
	}
	content := readRefreshFile(t, filepath.Join(root, ".orc", "runs", runID, "config", "current.json"))
	if err := json.Unmarshal(content, &current); err != nil {
		t.Fatalf("unmarshal current: %v", err)
	}
	return current
}

func readRefreshResolved(t *testing.T, root, runID, versionDir string) struct {
	SchemaVersion int             `json:"schema_version"`
	Project       *config.Project `json:"project"`
} {
	t.Helper()
	var resolved struct {
		SchemaVersion int             `json:"schema_version"`
		Project       *config.Project `json:"project"`
	}
	content := readRefreshFile(t, filepath.Join(root, ".orc", "runs", runID, "config", versionDir, "resolved.json"))
	if err := json.Unmarshal(content, &resolved); err != nil {
		t.Fatalf("unmarshal resolved: %v", err)
	}
	return resolved
}

func readRefreshFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func fixedRefreshTime() time.Time {
	return time.Date(2026, 5, 12, 22, 45, 0, 0, time.UTC)
}
