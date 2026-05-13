package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestExecuteRunContinueAllowsWorkflowLoopHardCapOnce(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	blockCLIWorkflowLoopHardCap(t, root, result.runID, "plan", 1, 2)

	output := executeCLICommand(t, []string{"run", "continue", result.runID, "--allow-loop-cap"})
	assertCLIOutputContainsAll(t, output, []string{
		"continued run " + result.runID,
		"allowed one entry into plan at count 2",
	})
	loaded := loadCLIRun(t, root, result.runID)
	if loaded.Status.State != cliStateRunning {
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
	if err := Execute([]string{"run", "continue", result.runID, "--allow-loop-cap"}, &stdout, &stderr); err == nil {
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

func TestExecuteRunContinueResolveBlockRetriesBlockedStep(t *testing.T) {
	run := startCLIImplementationReportRun(t)
	launchCLIWorkerReport(t, run.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, run.runID, ready("Code is ready for tests."))
	launchCLIWorkerReport(t, run.runID, blocked("Tests require network approval."))
	terminalizeCLIWorkflow(t, run.root, run.runID, "blocked_for_human", 3, "Tests require network approval.")

	output := executeCLICommand(t, []string{"run", "continue", run.runID, "--resolve-block", "--reason= fixed network config "})
	assertCLIOutputContainsAll(t, output, []string{
		"continued run " + run.runID,
		"after human-resolved block",
		"retrying step test",
	})
	loaded := loadCLIRun(t, run.root, run.runID)
	if loaded.Status.State != cliStateRunning {
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
			args: []string{"run", "continue", result.runID, "--allow-loop-cap", "--resolve-block", "--reason", "fixed"},
			want: []string{"mutually exclusive"},
		},
		{
			name: "missing reason",
			args: []string{"run", "continue", result.runID, "--resolve-block"},
			want: []string{"--reason is required"},
		},
		{
			name: "whitespace reason",
			args: []string{"run", "continue", result.runID, "--resolve-block", "--reason", " \t "},
			want: []string{"non-empty after trimming"},
		},
		{
			name: "repeated reason",
			args: []string{"run", "continue", result.runID, "--resolve-block", "--reason", "one", "--reason=two"},
			want: []string{"repeated --reason"},
		},
		{
			name: "reason without resolve block",
			args: []string{"run", "continue", result.runID, "--reason", "fixed"},
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
	if err := Execute([]string{"run", "continue", "--help"}, &stdout, &stderr); err != nil {
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
