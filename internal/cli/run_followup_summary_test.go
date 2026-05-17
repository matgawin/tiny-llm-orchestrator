package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestExecuteRunAddFollowupAppendsFollowup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	output := executeCLICommand(t, []string{
		"run", "add-followup", result.runID,
		"--title=Create release note",
		"--details=Mention the follow-up recorder.",
	})
	assertCLIOutputContainsAll(t, output, []string{"recorded follow-up for run " + result.runID})
	content := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, content, []string{
		"## Create release note",
		"Source: orchestrator",
		"Recorded-At:",
		"Mention the follow-up recorder.",
	})

	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Events[len(loaded.Events)-1].Type; got != "artifact.written" {
		t.Fatalf("last event type = %q, want artifact.written", got)
	}
}

func TestExecuteRunAddFollowupRequiresTitle(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"run", "add-followup", result.runID, "--details", "No title."}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want missing title failure")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "--title is required") {
		t.Fatalf("stderr = %q, want missing title", got)
	}
}

func TestExecuteRunAddFollowupOnBeadBackedRunDoesNotCallBD(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := startCLIBeadBackedRunThenBlockBD(t, root)

	output := executeCLICommand(t, []string{
		"run", "add-followup", result.runID,
		"--title", "Create bead manually",
		"--details", "Human should decide whether to create a bead.",
	})
	assertCLIOutputContainsAll(t, output, []string{"recorded follow-up for run " + result.runID})
	content := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, content, []string{
		"## Create bead manually",
		"Source: orchestrator",
		"Human should decide whether to create a bead.",
	})
}

func TestRunRecordSummaryOnBeadBackedRunDoesNotCallBD(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	result := startCLIBeadBackedRunThenBlockBD(t, root)
	if _, _, err := openCLIStore(t, root).UpdateStatus(result.runID, runstore.StatusUpdate{State: cliStateReadyForHuman}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	summaryPath := filepath.Join(root, "final-summary.md")
	writeCLIFile(t, summaryPath, "# Final Summary\n\nSuggested bead note for the human.\n")

	output := executeCLICommand(t, []string{"run", "record-summary", result.runID, "--file=" + summaryPath})
	assertCLIOutputContainsAll(t, output, []string{"recorded final summary for run " + result.runID, "summaries/"})

	_, content := latestCLIArtifactContent(t, root, result.runID, runstore.KindSummary)
	if !strings.Contains(content, "Suggested bead note for the human.") {
		t.Fatalf("summary content = %q, want bead note suggestion preserved", content)
	}
}

func TestRunRecordSummaryPersistsReadySummaryAndLeavesRunTerminal(t *testing.T) {
	run := startCLIImplementationReportRun(t)
	launchCLIWorkerReport(t, run.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, run.runID, ready("Code is ready for tests."))
	launchCLIWorkerReport(t, run.runID, passed("Tests passed."))
	attemptsBeforeApprovalTerminal := len(loadCLIRun(t, run.root, run.runID).Status.Attempts)
	launchCLIWorkerReport(t, run.runID, approved("Review approved."))
	terminalizeCLIWorkflow(t, run.root, run.runID, cliStateReadyForHuman, attemptsBeforeApprovalTerminal+1, "Review approved.")

	summaryPath := filepath.Join(run.root, "final-summary.md")
	writeCLIFile(t, summaryPath, "# Final Summary\n\nChanges, tests, risks, follow-ups, VCS summary, and review checklist.\n")
	recordOutput := executeCLICommand(t, []string{"run", "record-summary", run.runID, "--file", summaryPath})
	assertCLIOutputContainsAll(t, recordOutput, []string{"recorded final summary", "summaries/"})

	summaryRef, summaryContent := latestCLIArtifactContent(t, run.root, run.runID, runstore.KindSummary)
	if !strings.Contains(summaryContent, "review checklist") {
		t.Fatalf("summary content = %q, want copied final handoff content", summaryContent)
	}

	statusOutput := executeCLICommand(t, []string{"run", "status", run.runID})
	assertCLIOutputContainsAll(t, statusOutput, []string{"state: ready_for_human", "summary: " + summaryRef.Path})

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"worker", "launch-next", run.runID}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want terminal no-launch error after summary")
	}

	afterLaunch := loadCLIRun(t, run.root, run.runID)
	if got := len(afterLaunch.Status.Attempts); got != attemptsBeforeApprovalTerminal+1 {
		t.Fatalf("attempt history len = %d, want no worker launch after summary", got)
	}
}

func TestRunRecordSummaryRejectsNotReadyRuns(t *testing.T) {
	for _, state := range []string{"running", "blocked_for_human", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)

			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			if state != "running" {
				if _, _, err := openCLIStore(t, root).UpdateStatus(result.runID, runstore.StatusUpdate{State: state}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			}

			summaryPath := filepath.Join(root, "final-summary.md")
			writeCLIFile(t, summaryPath, "# Final Summary\n")

			var stdout, stderr bytes.Buffer

			err := Execute([]string{"run", "record-summary", result.runID, "--file", summaryPath}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want rejection")
			}

			assertCLIOutputContainsAll(t, stderr.String(), []string{`want "ready_for_human"`, "use summary-context"})

			loaded := loadCLIRun(t, root, result.runID)
			for _, ref := range loaded.Status.Artifacts {
				if ref.Kind == runstore.KindSummary {
					t.Fatalf("summary ref = %+v, want none after rejection", ref)
				}
			}
		})
	}
}
