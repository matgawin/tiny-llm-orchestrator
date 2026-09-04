//nolint:goconst // Test strings are clearer in place.
package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestRepeatedReviewFindingPreviewAdvanceAndContinuation(t *testing.T) {
	run := startCLIImplementationReportRun(t)
	launchCLIWorkerReport(t, run.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, run.runID, ready("Code is ready."))
	launchCLIWorkerReport(t, run.runID, passed("Checks passed."))
	launchCLIWorkerReport(t, run.runID, changesRequested("Fix the review finding."))
	launchCLIWorkerReport(t, run.runID, ready("No files needed a change."))
	launchCLIWorkerReport(t, run.runID, passed("Checks still pass."))
	launchCLIWorkerReport(t, run.runID, changesRequested("The same finding remains."))

	beforePreview := loadCLIRun(t, run.root, run.runID)
	preview := executeCLICommand(t, []string{commandRun, "next", run.runID})
	assertCLIOutputContainsAll(t, preview, []string{"repeated_review_finding", "review-change", "code"})

	afterPreview := loadCLIRun(t, run.root, run.runID)
	if afterPreview.Status.LastSequence != beforePreview.Status.LastSequence || afterPreview.Status.ReviewFindingBlock != nil {
		t.Fatalf("preview mutated status: before=%+v after=%+v", beforePreview.Status, afterPreview.Status)
	}

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandRun, "advance", run.runID}, &stdout, &stderr)
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("advance error = %v, exit code = %d, want repeated-finding exit code 2", err, ExitCode(err))
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{"stop reason: repeated_review_finding", "exit code: 2"})

	blocked := loadCLIRun(t, run.root, run.runID)
	if blocked.Status.ReviewFindingBlock == nil || blocked.Status.ReviewFindingBlock.FirstReportAttemptID == "" || blocked.Status.ReviewFindingBlock.RepeatedReportAttemptID == "" {
		t.Fatalf("review finding block = %+v, want both report attempts", blocked.Status.ReviewFindingBlock)
	}

	counts := blocked.Status.WorkflowLoop.Counts

	output := executeCLICommand(t, []string{commandRun, "continue", run.runID, "--allow-review-finding"})
	assertCLIOutputContainsAll(t, output, []string{"continued run " + run.runID, "allowed one routing decision into code"})
	launchCLIWorkerReport(t, run.runID, ready("Human allowed one more correction."))

	continued := loadCLIRun(t, run.root, run.runID)
	if continued.Status.PendingReviewFindingOverride != nil || continued.Status.ReviewFindingBlock != nil {
		t.Fatalf("continued finding state = block %+v override %+v, want consumed", continued.Status.ReviewFindingBlock, continued.Status.PendingReviewFindingOverride)
	}

	if len(continued.Status.Attempts) != len(blocked.Status.Attempts)+1 {
		t.Fatalf("attempt count = %d, want one correction after %d", len(continued.Status.Attempts), len(blocked.Status.Attempts))
	}

	if continued.Status.Attempts[len(continued.Status.Attempts)-1].StepID != "code" {
		t.Fatalf("last step = %q, want code", continued.Status.Attempts[len(continued.Status.Attempts)-1].StepID)
	}

	if counts["code"] == 0 || continued.Status.WorkflowLoop.Counts["code"] != counts["code"]+1 {
		t.Fatalf("code loop counts before=%d after=%d, want one increment", counts["code"], continued.Status.WorkflowLoop.Counts["code"])
	}

	if _, _, err := openCLIStore(t, run.root).AllowRepeatedReviewFinding(run.runID, time.Time{}); err == nil || !strings.Contains(err.Error(), "no active repeated review finding block") {
		t.Fatalf("second continuation error = %v, want one-use rejection", err)
	}

	// LoadContext replays the recorded stop, override, and consumption events.
	replayed := loadCLIRun(t, run.root, run.runID)
	if replayed.Status.PendingReviewFindingOverride != nil || replayed.Status.WorkflowLoop.Counts["code"] != continued.Status.WorkflowLoop.Counts["code"] {
		t.Fatalf("replayed status = %+v, want consumed override and preserved counts", replayed.Status)
	}

	if got := repeatedFindingOccurrences(replayed.Status.Attempts, "review-change"); got != 2 {
		t.Fatalf("finding occurrences = %d, want 2 after continuation", got)
	}

	events := string(readCLIFile(t, filepath.Join(replayed.Path, "events.jsonl")))
	assertCLIOutputContainsAll(t, events, []string{"workflow.repeated_review_finding", "workflow.repeated_review_finding_override", "allow_review_finding"})
}

func TestRepeatedReviewFindingStopWinsOverLoopHardCap(t *testing.T) {
	root := withTempCwd(t)
	writeCLIImplementationProject(t, root)
	workflowPath := root + "/.orc/workflows/implementation.yaml"
	workflowConfig := string(readCLIFile(t, workflowPath))
	workflowConfig = strings.Replace(workflowConfig, "  retries:\n", "  loop_caps:\n    enabled: true\n    soft: 10\n    hard: 20\n  retries:\n", 1)
	workflowConfig = strings.Replace(workflowConfig, "  code:\n    agent: coder\n", "  code:\n    agent: coder\n    loop: {key: coding, soft: 1, hard: 2}\n", 1)
	writeCLIFile(t, workflowPath, workflowConfig)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	t.Setenv("PATH", shim.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")

	launchCLIWorkerReport(t, result.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, result.runID, ready("Initial code is ready."))
	launchCLIWorkerReport(t, result.runID, passed("Checks passed."))
	launchCLIWorkerReport(t, result.runID, changesRequested("Fix the review finding."))
	launchCLIWorkerReport(t, result.runID, ready("Correction is ready."))
	launchCLIWorkerReport(t, result.runID, passed("Checks passed again."))
	launchCLIWorkerReport(t, result.runID, changesRequested("The same finding remains."))

	preview := executeCLICommand(t, []string{commandRun, "next", result.runID})
	if !strings.Contains(preview, "repeated_review_finding") || strings.Contains(preview, "workflow_loop_hard_cap") {
		t.Fatalf("preview = %q, want repeated-finding stop to win over loop hard cap", preview)
	}
}

func repeatedFindingOccurrences(attempts []runstore.Attempt, findingID string) int {
	count := 0

	for _, attempt := range attempts {
		if attempt.Report == nil {
			continue
		}

		for _, finding := range attempt.Report.Findings {
			if finding.FindingID == findingID {
				count++
			}
		}
	}

	return count
}

func TestExecuteRunContinueAllowsWorkflowLoopHardCapOnce(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	blockCLIWorkflowLoopHardCap(t, root, result.runID, "plan", 1, 2)

	output := executeCLICommand(t, []string{commandRun, "continue", result.runID, "--allow-loop-cap"})
	assertCLIOutputContainsAll(t, output, []string{
		"continued run " + result.runID,
		"allowed one entry into plan at count 2",
	})

	loaded := loadCLIRun(t, root, result.runID)
	if loaded.Status.State != stateRunning {
		t.Fatalf("state = %q, want running", loaded.Status.State)
	}

	override := loaded.Status.WorkflowLoop.PendingHardCapOverride
	if override == nil || override.TargetState != "plan" || override.CountBeforeOverride != 1 || override.CountAfterOverride != 2 || override.HumanAction != "allow_loop_cap" {
		t.Fatalf("pending override = %+v, want plan count 2 allow_loop_cap", override)
	}
}

func TestExecuteRunContinueFailsWithoutActiveWorkflowLoopHardCap(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	before := loadCLIRun(t, root, result.runID)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, "continue", result.runID, "--allow-loop-cap"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want no-active-block failure")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "no active workflow loop hard-cap block") {
		t.Fatalf("stderr = %q, want no-active-block message", got)
	}

	after := loadCLIRun(t, root, result.runID)
	if after.Status.LastSequence != before.Status.LastSequence || after.Status.State != before.Status.State || after.Status.WorkflowLoop.PendingHardCapOverride != nil {
		t.Fatalf("after status = %+v, want no mutation from %+v", after.Status, before.Status)
	}
}

func TestExecuteRunContinueReviewFindingFailureWritesStderr(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandRun, "continue", result.runID, "--allow-review-finding"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want no-active-block failure")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "no active repeated review finding block") {
		t.Fatalf("stderr = %q, want no-active-block message", got)
	}
}

func TestExecuteRunContinueResolveBlockRetriesBlockedStep(t *testing.T) {
	run := startCLIImplementationReportRun(t)
	launchCLIWorkerReport(t, run.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, run.runID, ready("Code is ready for tests."))
	launchCLIWorkerReport(t, run.runID, blocked("Tests require network approval."))
	terminalizeCLIWorkflow(t, run.root, run.runID, "blocked_for_human", 3, "Tests require network approval.")

	output := executeCLICommand(t, []string{commandRun, "continue", run.runID, "--resolve-block", "--reason= fixed network config "})
	assertCLIOutputContainsAll(t, output, []string{
		"continued run " + run.runID,
		"after human-resolved block",
		"retrying step test",
	})

	loaded := loadCLIRun(t, run.root, run.runID)
	if loaded.Status.State != stateRunning {
		t.Fatalf("state = %q, want running", loaded.Status.State)
	}

	if loaded.Status.Continued == nil || loaded.Status.Continued.Reason != "fixed network config" || loaded.Status.Continued.ResolvedStepID != "test" {
		t.Fatalf("continued = %+v, want trimmed reason and test step", loaded.Status.Continued)
	}

	launchCLIWorkerReport(t, run.runID, passed("Tests passed after human fix."))

	afterRetry := loadCLIRun(t, run.root, run.runID)
	if got := len(afterRetry.Status.Attempts); got != 4 {
		t.Fatalf("attempt history len = %d, want retry attempt appended", got)
	}

	if got := afterRetry.Status.Attempts[3].StepID; got != "test" {
		t.Fatalf("retry step = %q, want test", got)
	}

	if afterRetry.Status.Continued != nil {
		t.Fatalf("continued marker = %+v, want cleared after retry launch", afterRetry.Status.Continued)
	}
}

func TestExecuteRunContinueResolveBlockFlagValidation(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "mutually exclusive modes",
			args: []string{commandRun, "continue", result.runID, "--allow-loop-cap", "--resolve-block", "--reason", "fixed"},
			want: []string{"mutually exclusive"},
		},
		{
			name: "missing reason",
			args: []string{commandRun, "continue", result.runID, "--resolve-block"},
			want: []string{"--reason is required"},
		},
		{
			name: "whitespace reason",
			args: []string{commandRun, "continue", result.runID, "--resolve-block", "--reason", " \t "},
			want: []string{"non-empty after trimming"},
		},
		{
			name: "repeated reason",
			args: []string{commandRun, "continue", result.runID, "--resolve-block", "--reason", "one", "--reason=two"},
			want: []string{"repeated --reason"},
		},
		{
			name: "reason without resolve block",
			args: []string{commandRun, "continue", result.runID, "--reason", "fixed"},
			want: []string{"--reason is only valid with --resolve-block"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := loadCLIRun(t, root, result.runID)

			var stdout, stderr bytes.Buffer
			if err := Execute(tc.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want flag validation failure")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			assertCLIOutputContainsAll(t, stderr.String(), tc.want)

			after := loadCLIRun(t, root, result.runID)
			if after.Status.LastSequence != before.Status.LastSequence || after.Status.State != before.Status.State {
				t.Fatalf("after status = %+v, want no mutation from %+v", after.Status, before.Status)
			}
		})
	}
}

func TestExecuteRunContinueHelpDocumentsContinuationModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, "continue", helpFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"orc run continue <run-id> --allow-loop-cap",
		"orc run continue <run-id> --resolve-block --reason <text>",
		"--resolve-block",
		"--reason <text>",
	})
}
