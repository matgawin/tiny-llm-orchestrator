package runinspect

import (
	"bytes"
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestStatusShowsNewRunSelectedStartStep(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte("# Task\n"),
		Time:    fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	var stdout bytes.Buffer
	if err := Status(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	output := stdout.String()
	assertContainsAll(t, "status", output, []string{
		"run: inspect-run\n",
		"state: running\n",
		"workflow: implementation\n",
		"selected_step: plan\n",
		"active_attempt: none\n",
		"recent_reports:\n  none\n",
		"artifacts:\n  - task_context: task/context.md\n",
	})
}

func TestNextShowsSelectedStepWithoutLaunching(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	before := snapshotRunDir(t, root, runID)

	var stdout bytes.Buffer
	if err := Next(context.Background(), Options{Root: root, RunID: runID, Stdout: &stdout}); err != nil {
		t.Fatalf("Next returned error: %v", err)
	}

	output := stdout.String()
	assertContainsAll(t, "next", output, []string{
		"decision: select_step\n",
		"selected_step: plan\n",
		"agent: planner\n",
		"launch: not launched\n",
	})
	after := snapshotRunDir(t, root, runID)
	if !maps.Equal(before, after) {
		t.Fatalf("Next mutated run directory:\nbefore: %+v\nafter: %+v", before, after)
	}
}

func TestStatusAndNextShowActiveAttempt(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       "attempt-active",
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"selected_step: none\n",
		"active_attempt: attempt-active\n",
	})
	assertContainsAll(t, "next", next, []string{
		"decision: wait_active_attempt\n",
		"active_attempt: attempt-active\n",
		"selected_step: plan\n",
		"agent: planner\n",
		"launch: already active\n",
	})
}

func TestNextRefusesPendingLauncherOutcome(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusRunning, nil)
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	recordMissingReportAttempt(t, store, runID, "attempt-missing-report")

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"selected_step: none\n",
		"active_attempt: none\n",
		"terminal_reason: pending_worker_outcome\n",
	})
	assertContainsAll(t, "next", next, []string{
		"decision: terminal\n",
		"terminal_reason: pending_worker_outcome\n",
		"launch: no worker should launch\n",
	})
}

func recordMissingReportAttempt(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	recordLaunchedAttempt(t, store, runID, attemptID)
	if _, _, err := store.FinishAttempt(runID, runstore.FinishAttemptRequest{
		AttemptID: attemptID,
		State:     runstore.AttemptStateMissingReport,
		Status:    "failed",
		Result:    runstore.AttemptResultMissingReport,
		ExitState: "exited",
		Time:      fixedTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
}

func recordLaunchedAttempt(t *testing.T, store *runstore.Store, runID, attemptID string) {
	t.Helper()
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan-" + attemptID,
		Content: []byte("prompt\n"),
		Time:    fixedTime().Add(1200 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedTime().Add(1300 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan-" + attemptID,
		Content: []byte("log\n"),
		Time:    fixedTime().Add(1400 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedTime().Add(1500 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedTime().Add(1600 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
}

func TestReadyForHumanStatusAndNextShowReviewPaths(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusReadyForHuman, nil)

	status, next := inspectStatusAndNext(t, root, runID)
	paths := terminalPaths(root, runID)
	assertTerminalOutputs(t, status, next, terminalOutputWants{
		status: []string{
			"selected_step: none\n",
			"terminal_reason: ready_for_human\n",
			"review_state: ready_for_human; no more workers should launch\n",
			"summary_context: " + paths.run + "\n",
			"final_summaries: " + paths.summaries + "\n",
		},
		next: []string{
			"decision: terminal\n",
			"terminal_reason: ready_for_human\n",
			"launch: no worker should launch\n",
			"review_state: ready_for_human; no more workers should launch\n",
			"final_summaries: " + paths.summaries + "\n",
		},
	})
}

func TestBlockedForHumanStatusAndNextShowUnavailableReportDetails(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusBlockedForHuman, nil)

	status, next := inspectStatusAndNext(t, root, runID)
	paths := terminalPaths(root, runID)
	assertTerminalOutputs(t, status, next, terminalOutputWants{
		status: []string{
			"selected_step: none\n",
			"terminal_reason: blocked_for_human\n",
			"human_attention: blocked_for_human; report details not available\n",
			"summary_context: " + paths.run + "\n",
			"final_summaries: " + paths.summaries + "\n",
		},
		next: []string{
			"decision: terminal\n",
			"terminal_reason: blocked_for_human\n",
			"launch: no worker should launch\n",
			"human_attention: blocked_for_human; report details not available\n",
			"final_summaries: " + paths.summaries + "\n",
		},
	})
}

type terminalOutputWants struct {
	status []string
	next   []string
}

type terminalPathWants struct {
	run       string
	summaries string
}

func terminalPaths(root, runID string) terminalPathWants {
	runPath := filepath.ToSlash(filepath.Join(root, ".orc", "runs", runID))
	return terminalPathWants{
		run:       runPath,
		summaries: filepath.ToSlash(filepath.Join(runPath, "summaries")),
	}
}

func assertTerminalOutputs(t *testing.T, status, next string, wants terminalOutputWants) {
	t.Helper()
	assertContainsAll(t, "status", status, wants.status)
	assertContainsAll(t, "next", next, wants.next)
}

func TestBlockedStatusAndNextShowReportArtifact(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)
	runID := createRun(t, root, workflow.RunStatusBlockedForHuman, &runstore.Artifact{
		Kind:    runstore.KindReport,
		Name:    "plan",
		Content: []byte("blocked report\n"),
		Time:    fixedTime().Add(time.Minute),
	})

	status, next := inspectStatusAndNext(t, root, runID)
	assertContainsAll(t, "status", status, []string{
		"terminal_reason: blocked_for_human\n",
		"recent_reports:\n  - reports/000002-plan.md\n",
		"human_attention: blocked_for_human; see recent_reports\n",
	})
	assertContainsAll(t, "next", next, []string{
		"decision: terminal\n",
		"terminal_reason: blocked_for_human\n",
		"recent_reports:\n  - reports/000002-plan.md\n",
		"human_attention: blocked_for_human; see recent_reports\n",
	})
}

func TestInspectUnknownRunFailsClearly(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root)

	for _, tc := range []struct {
		name    string
		inspect func(context.Context, Options) error
	}{
		{name: "status", inspect: Status},
		{name: "next", inspect: Next},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.inspect(context.Background(), Options{Root: root, RunID: "missing", Stdout: &bytes.Buffer{}})
			if err == nil || !strings.Contains(err.Error(), `run "missing" not found`) {
				t.Fatalf("%s error = %v, want not found", tc.name, err)
			}
		})
	}
}

func inspectStatusAndNext(t *testing.T, root, runID string) (string, string) {
	t.Helper()
	var statusOut bytes.Buffer
	if err := Status(context.Background(), Options{Root: root, RunID: runID, Stdout: &statusOut}); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	var nextOut bytes.Buffer
	if err := Next(context.Background(), Options{Root: root, RunID: runID, Stdout: &nextOut}); err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	return statusOut.String(), nextOut.String()
}

func assertContainsAll(t *testing.T, label, output string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("%s output missing %q:\n%s", label, want, output)
		}
	}
}

func createRun(t *testing.T, root, state string, artifact *runstore.Artifact) string {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "inspect-run",
		Workflow: "implementation",
		Time:     fixedTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if artifact != nil {
		if _, err := store.WriteArtifact(run.ID, *artifact); err != nil {
			t.Fatalf("WriteArtifact returned error: %v", err)
		}
	}
	if state != workflow.RunStatusRunning {
		at := fixedTime().Add(time.Minute)
		if artifact != nil {
			at = fixedTime().Add(2 * time.Minute)
		}
		if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: state, Time: at}); err != nil {
			t.Fatalf("UpdateStatus returned error: %v", err)
		}
	}
	return run.ID
}

func writeProject(t *testing.T, root string) {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		BlockedResults:   []string{"blocked"},
	})
}

func fixedTime() time.Time {
	return time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
}

func TestReportPathsFiltersReports(t *testing.T) {
	refs := []runstore.ArtifactRef{
		{Kind: runstore.KindTaskContext, Path: "task/context.md"},
		{Kind: runstore.KindReport, Path: "reports/000002-plan.md"},
	}
	paths := reportPaths(refs)
	if want := []string{"reports/000002-plan.md"}; !slices.Equal(paths, want) {
		t.Fatalf("report paths = %v, want %v", paths, want)
	}
}

func snapshotRunDir(t *testing.T, root, runID string) map[string]string {
	t.Helper()
	runDir := filepath.Join(root, ".orc", "runs", runID)
	snapshot := map[string]string{}
	if err := filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(rel)] = string(content)
		return nil
	}); err != nil {
		t.Fatalf("snapshot run dir: %v", err)
	}
	return snapshot
}
