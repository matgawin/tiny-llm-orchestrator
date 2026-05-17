package runsummary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestRecordPersistsSummaryForReadyRun(t *testing.T) {
	root, store := newSummaryProject(t)
	run := createReadySummaryRun(t, store, "ready-summary")
	summaryPath := filepath.Join(root, "final-review.md")

	const summaryContent = "# Final Review\n\nSuggested bead note: ready for review.\n"
	writeSummaryFile(t, summaryPath, summaryContent)

	result, err := Record(context.Background(), Options{Root: root, RunID: run.ID, File: summaryPath})
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	if result.SummaryRef.Kind != runstore.KindSummary {
		t.Fatalf("summary kind = %q, want %q", result.SummaryRef.Kind, runstore.KindSummary)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := len(loaded.Status.Artifacts); got != 1 {
		t.Fatalf("artifact refs = %d, want 1", got)
	}

	if loaded.Status.Artifacts[0].Path != result.SummaryRef.Path {
		t.Fatalf("status artifact path = %q, want %q", loaded.Status.Artifacts[0].Path, result.SummaryRef.Path)
	}

	content, err := store.ReadArtifact(run.ID, result.SummaryRef)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}

	if string(content) != summaryContent {
		t.Fatalf("summary content = %q", string(content))
	}
}

func TestRecordDoesNotRequireCurrentWorkflowConfig(t *testing.T) {
	root := t.TempDir()
	store := openSummaryStore(t, root)

	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "retired-workflow-summary",
		Workflow: "retired-workflow",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: workflow.RunStatusReadyForHuman}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	summaryPath := filepath.Join(root, "final-review.md")
	writeSummaryFile(t, summaryPath, "# Final Review\n")

	result, err := Record(context.Background(), Options{Root: root, RunID: run.ID, File: summaryPath})
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	if result.SummaryRef.Kind != runstore.KindSummary {
		t.Fatalf("summary kind = %q, want %q", result.SummaryRef.Kind, runstore.KindSummary)
	}
}

func TestRecordRejectsRunsThatAreNotReadyForHuman(t *testing.T) {
	for _, state := range []string{workflow.RunStatusRunning, workflow.RunStatusBlockedForHuman, workflow.RunStatusCancelled} {
		t.Run(state, func(t *testing.T) {
			root, store := newSummaryProject(t)

			run := createSummaryRun(t, store, "not-ready-"+state)
			if state != workflow.RunStatusRunning {
				if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: state}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			}

			summaryPath := filepath.Join(root, "final-review.md")
			writeSummaryFile(t, summaryPath, "# Final Review\n")

			_, err := Record(context.Background(), Options{Root: root, RunID: run.ID, File: summaryPath})
			if err == nil {
				t.Fatal("Record returned nil error, want rejection")
			}

			if !strings.Contains(err.Error(), `want "ready_for_human"`) || !strings.Contains(err.Error(), "use summary-context") {
				t.Fatalf("error = %q, want clear ready_for_human rejection", err.Error())
			}

			loaded, loadErr := store.Load(run.ID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			if len(loaded.Status.Artifacts) != 0 {
				t.Fatalf("artifacts = %+v, want none after rejection", loaded.Status.Artifacts)
			}
		})
	}
}

func newSummaryProject(t *testing.T) (string, *runstore.Store) {
	t.Helper()
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		Beads:            "optional",
		MarkdownFallback: true,
	})

	return root, openSummaryStore(t, root)
}

func openSummaryStore(t *testing.T, root string) *runstore.Store {
	t.Helper()

	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	return store
}

func createReadySummaryRun(t *testing.T, store *runstore.Store, runID string) *runstore.Run {
	t.Helper()

	run := createSummaryRun(t, store, runID)
	if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: workflow.RunStatusReadyForHuman}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	return run
}

func createSummaryRun(t *testing.T, store *runstore.Store, runID string) *runstore.Run {
	t.Helper()

	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    runID,
		Workflow: "implementation",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	return run
}

func writeSummaryFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write summary file: %v", err)
	}
}
