package runstart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestStartTaskFilePersistsContextAndSnapshot(t *testing.T) {
	root := writeRunStartProject(t, workflowTaskContext("optional", true))
	taskPath := filepath.Join(root, "task.md")
	writeRunStartFile(t, taskPath, "# Task\n\nDo the work.\n")

	result, err := Start(context.Background(), Options{
		Root:     root,
		Workflow: "implementation",
		TaskFile: taskPath,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if got := string(readRunStartFile(t, filepath.Join(result.Path, "task", "context.md"))); got != "# Task\n\nDo the work.\n" {
		t.Fatalf("task context = %q", got)
	}
	snapshot := readTaskSnapshot(t, result.Path)
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", snapshot.SchemaVersion)
	}
	if snapshot.Source.Type != SourceTaskFile || snapshot.Source.Path != taskPath {
		t.Fatalf("source = %+v, want task file %q", snapshot.Source, taskPath)
	}
	if snapshot.BeadLookup.Attempted {
		t.Fatalf("bead lookup attempted = true, want false")
	}
	assertLoadedRunHasTaskArtifacts(t, root, result.RunID)
}

func TestStartRejectsExplicitBeadFailureWithoutFallbackBeforeRunDirectory(t *testing.T) {
	root := writeRunStartProject(t, workflowTaskContext("optional", true))

	_, err := Start(context.Background(), Options{
		Root:     root,
		Workflow: "implementation",
		BeadID:   "missing-bead",
		Env:      []string{"PATH="},
	})
	if err == nil {
		t.Fatal("Start returned nil error, want bead lookup failure")
	}
	if !strings.Contains(err.Error(), `read bead "missing-bead"`) {
		t.Fatalf("error = %q, want bead lookup context", err)
	}
	runsDir := filepath.Join(root, ".orc", "runs")
	entries, readErr := os.ReadDir(runsDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read runs dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("runs dir entries = %d, want no partial run", len(entries))
	}
}

func TestStartUsesFallbackTaskFileAndRecordsBeadFailure(t *testing.T) {
	root := writeRunStartProject(t, workflowTaskContext("optional", true))
	fallbackPath := filepath.Join(root, "fallback.md")
	writeRunStartFile(t, fallbackPath, "# Fallback\n")
	beadsDir := filepath.Join(root, "..", ".beads")

	result, err := Start(context.Background(), Options{
		Root:             root,
		Workflow:         "implementation",
		BeadID:           "missing-bead",
		FallbackTaskFile: fallbackPath,
		Env:              []string{"PATH=", "BEADS_DIR=" + beadsDir},
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if got := string(readRunStartFile(t, filepath.Join(result.Path, "task", "context.md"))); got != "# Fallback\n" {
		t.Fatalf("fallback context = %q", got)
	}
	snapshot := readTaskSnapshot(t, result.Path)
	if snapshot.Source.Type != SourceFallbackTaskFile || snapshot.Source.Path != fallbackPath {
		t.Fatalf("source = %+v, want fallback task file %q", snapshot.Source, fallbackPath)
	}
	if got := snapshot.Source.Env["BEADS_DIR"]; got != beadsDir {
		t.Fatalf("fallback snapshot BEADS_DIR = %q, want %q", got, beadsDir)
	}
	if !snapshot.BeadLookup.Attempted || snapshot.BeadLookup.OK || snapshot.BeadLookup.BeadID != "missing-bead" {
		t.Fatalf("bead lookup = %+v, want failed missing-bead attempt", snapshot.BeadLookup)
	}
	if snapshot.Fallback != (FallbackInfo{Used: true, SourceType: SourceTaskFile, Path: fallbackPath}) {
		t.Fatalf("fallback = %+v, want used task file %q", snapshot.Fallback, fallbackPath)
	}
}

func TestCleanupStartedRunRemovesRunDirectory(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "partial-run")
	if err := os.Mkdir(runPath, 0o750); err != nil {
		t.Fatalf("create partial run dir: %v", err)
	}

	cause := errors.New("artifact write failed")
	err := cleanupStartedRun(runPath, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("cleanup error = %v, want original cause", err)
	}
	if _, statErr := os.Stat(runPath); !os.IsNotExist(statErr) {
		t.Fatalf("partial run stat err = %v, want removed", statErr)
	}
}

func TestCleanupStartedRunReportsCleanupFailure(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	writeRunStartFile(t, parentFile, "content\n")
	runPath := filepath.Join(parentFile, "partial-run")

	err := cleanupStartedRun(runPath, errors.New("artifact write failed"))
	if err == nil {
		t.Fatal("cleanupStartedRun returned nil error, want cleanup failure")
	}
	for _, want := range []string{"artifact write failed", "cleanup run directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("cleanup error = %q, want %q", err, want)
		}
	}
}

func TestStartEnforcesWorkflowTaskContextPolicy(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		opts     Options
		want     string
	}{
		{
			name:     "beads disabled",
			workflow: workflowTaskContext("disabled", true),
			opts:     Options{Workflow: "implementation", BeadID: "main-1"},
			want:     "disables bead task context",
		},
		{
			name:     "beads required",
			workflow: workflowTaskContext("required", true),
			opts:     Options{Workflow: "implementation", TaskText: "local task"},
			want:     "requires bead task context",
		},
		{
			name:     "markdown disabled",
			workflow: workflowTaskContext("optional", false),
			opts:     Options{Workflow: "implementation", TaskText: "local task"},
			want:     "disables Markdown task context",
		},
		{
			name:     "fallback disabled",
			workflow: workflowTaskContext("optional", false),
			opts:     Options{Workflow: "implementation", BeadID: "main-1", FallbackTaskFile: "task.md"},
			want:     "disables Markdown fallback task context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeRunStartProject(t, tt.workflow)
			tt.opts.Root = root
			_, err := Start(context.Background(), tt.opts)
			if err == nil {
				t.Fatal("Start returned nil error, want policy failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestStartRequiredBeadsAllowsFallbackWhenMarkdownFallbackEnabled(t *testing.T) {
	root := writeRunStartProject(t, workflowTaskContext("required", true))
	fallbackPath := filepath.Join(root, "fallback.md")
	writeRunStartFile(t, fallbackPath, "# Required Fallback\n")

	result, err := Start(context.Background(), Options{
		Root:             root,
		Workflow:         "implementation",
		BeadID:           "missing-bead",
		FallbackTaskFile: fallbackPath,
		Env:              []string{"PATH="},
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	snapshot := readTaskSnapshot(t, result.Path)
	if snapshot.Source.Type != SourceFallbackTaskFile {
		t.Fatalf("source type = %q, want %q", snapshot.Source.Type, SourceFallbackTaskFile)
	}
	if !snapshot.Fallback.Used {
		t.Fatalf("fallback = %+v, want used fallback", snapshot.Fallback)
	}
}

func TestStartReadableBeadRecordsBEADSDir(t *testing.T) {
	root := writeRunStartProject(t, workflowTaskContext("optional", true))
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	beadID := "main-readable"
	path := fakeBDPath(t, beadID, beadsDir)
	// exec.LookPath uses the parent process PATH to locate the fake bd binary;
	// Options.Env below is still the environment passed to the child process.
	t.Setenv("PATH", path)

	result, err := Start(context.Background(), Options{
		Root:     root,
		Workflow: "implementation",
		BeadID:   beadID,
		Env:      []string{"PATH=" + path, "BEADS_DIR=" + beadsDir},
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	contextContent := string(readRunStartFile(t, filepath.Join(result.Path, "task", "context.md")))
	if !strings.Contains(contextContent, beadID) || !strings.Contains(contextContent, "Readable bead") {
		t.Fatalf("task context = %q, want bead JSON content", contextContent)
	}
	snapshot := readTaskSnapshot(t, result.Path)
	if snapshot.Source.Type != SourceBead || snapshot.Source.BeadID != beadID {
		t.Fatalf("source = %+v, want readable bead %q", snapshot.Source, beadID)
	}
	if got := snapshot.Source.Env["BEADS_DIR"]; got != beadsDir {
		t.Fatalf("snapshot BEADS_DIR = %q, want %q", got, beadsDir)
	}
	if !snapshot.BeadLookup.Attempted || !snapshot.BeadLookup.OK {
		t.Fatalf("bead lookup = %+v, want successful attempt", snapshot.BeadLookup)
	}
}

func readTaskSnapshot(t *testing.T, runPath string) Snapshot {
	t.Helper()
	content := readRunStartFile(t, filepath.Join(runPath, "task", "snapshot.json"))
	var snapshot Snapshot
	if err := json.Unmarshal(content, &snapshot); err != nil {
		t.Fatalf("unmarshal task snapshot: %v\n%s", err, string(content))
	}
	return snapshot
}

func assertLoadedRunHasTaskArtifacts(t *testing.T, root, runID string) {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	run, err := store.Load(runID)
	if err != nil {
		t.Fatalf("load run %s: %v", runID, err)
	}
	if run.Status.State != "running" {
		t.Fatalf("loaded state = %q, want running", run.Status.State)
	}
	if run.Status.LastSequence != 3 {
		t.Fatalf("last sequence = %d, want 3", run.Status.LastSequence)
	}
	if got := len(run.Status.Artifacts); got != 2 {
		t.Fatalf("artifact refs = %d, want 2", got)
	}
	want := map[runstore.ArtifactKind]string{
		runstore.KindTaskContext:  "task/context.md",
		runstore.KindTaskSnapshot: "task/snapshot.json",
	}
	for _, ref := range run.Status.Artifacts {
		if wantPath, ok := want[ref.Kind]; !ok || ref.Path != wantPath {
			t.Fatalf("unexpected artifact ref = %+v, want task context and snapshot refs", ref)
		}
		delete(want, ref.Kind)
	}
	if len(want) != 0 {
		t.Fatalf("missing artifact refs: %+v", want)
	}
	if got := len(run.Events); got != 3 {
		t.Fatalf("event count = %d, want run.created plus two artifact writes", got)
	}
}

func writeRunStartProject(t *testing.T, workflow string) string {
	t.Helper()
	root := t.TempDir()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	writeRunStartFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n")
	writeRunStartFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), workflow)
	writeRunStartFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Test planner.\n---\n\nPlan.\n")
	return root
}

func workflowTaskContext(beads string, markdownFallback bool) string {
	return fmt.Sprintf(`name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: %s
  markdown_fallback: %t
defaults:
  timeout: 30m
  report_exit_grace: 30s
  retries: {}
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`, beads, markdownFallback)
}

func fakeBDPath(t *testing.T, beadID, beadsDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
if [ "${BEADS_DIR:-}" != %q ]; then
  printf 'unexpected BEADS_DIR: %%s\n' "${BEADS_DIR:-}" >&2
  exit 3
fi
if [ "$1" = "show" ] && [ "$2" = %q ] && [ "$3" = "--json" ]; then
  printf ' [{"id":%q,"title":"Readable bead","description":"Task body"}]\n'
  exit 0
fi
printf 'unexpected bd args: %%s %%s %%s\n' "$1" "$2" "$3" >&2
exit 2
`, beadsDir, beadID, beadID)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return dir
}

func readRunStartFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func writeRunStartFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
