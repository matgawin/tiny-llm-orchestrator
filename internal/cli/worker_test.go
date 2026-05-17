package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	defaultCodexNormalArgsWithReasoning  = "--ask-for-approval\nnever\nexec\n--skip-git-repo-check\n-\n--config\nmodel_reasoning_effort=\"medium\"\n"
	defaultCodexSandboxArgsWithReasoning = "--dangerously-bypass-approvals-and-sandbox\nexec\n--skip-git-repo-check\n-\n--config\nmodel_reasoning_effort=\"medium\"\n"
)

func TestExecuteWorkerHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"worker", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{"orc worker launches", "launch-next"})
}

func TestExecuteWorkerLaunchNextHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"worker", "launch-next", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{"Usage:", "launch-next <run-id>"})
}

func TestExecuteWorkerUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"worker", "unknown"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown subcommand")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, `unknown command "unknown" for "orc worker"`) {
		t.Fatalf("stderr = %q, want Cobra unknown command diagnostic", got)
	}
}

func TestExecuteWorkerLaunchNextRequiresRunID(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"worker", "launch-next"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want missing run id")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "orc worker launch-next: requires <run-id>") {
		t.Fatalf("stderr = %q, want missing run id", got)
	}
}

func TestExecuteWorkerLaunchNextRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"worker", "launch-next", "run-1", "extra"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want extra arg rejection")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "orc worker launch-next: accepts exactly one <run-id>") {
		t.Fatalf("stderr = %q, want extra arg diagnostic", got)
	}
}

func TestWorkerLaunchNextRoutesValidReportWithoutPendingOutcome(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	executeCLICommand(t, []string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "attempt-001",
		"--status", "done",
		"--result", "ready",
		"--summary", "Plan is ready.",
	})

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"worker", "launch-next", result.runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want terminal no-launch error")
	}

	stderrText := stderr.String()
	if strings.Contains(stderrText, "pending worker outcome") {
		t.Fatalf("stderr = %q, want routed terminal decision not legacy pending outcome", stderrText)
	}

	if !strings.Contains(stderrText, "transitioned to ready_for_human") {
		t.Fatalf("stderr = %q, want terminal transition", stderrText)
	}

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.State != cliStateReadyForHuman {
		t.Fatalf("run state = %q, want ready_for_human", loaded.Status.State)
	}
}

func TestWorkerLaunchNextAcceptsWorkerReportBeforeExit(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	t.Setenv("PATH", shim.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"result: done/ready"})

	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateReported || attempt.Report == nil {
		t.Fatalf("attempt = %+v, want worker reported attempt", attempt)
	}

	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want cleared by report", loaded.Status.ActiveAttempt)
	}
}

func TestWorkerLaunchNextDisplaysLiveProgressFromAgent(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	t.Setenv("PATH", shim.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")
	t.Setenv("ORC_CLI_CODEX_PROGRESS", "analyzing code paths")

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	loaded := loadCLIRun(t, root, result.runID)
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]

	progressLine := fmt.Sprintf("[%s %s] analyzing code paths", attempt.StepID, attempt.AttemptID)
	if !strings.Contains(output, progressLine) {
		t.Fatalf("output = %q, want progress line %q", output, progressLine)
	}

	if progressIndex, resultIndex := strings.Index(output, progressLine), strings.Index(output, "launched attempt "+attempt.AttemptID); progressIndex < 0 || resultIndex < 0 || progressIndex > resultIndex {
		t.Fatalf("output = %q, want progress before final launch result", output)
	}
}

func TestWorkerLaunchNextRoutesImplementationWorkflowReportsEndToEnd(t *testing.T) {
	run := startCLIImplementationReportRun(t)

	launchCLIWorkerReport(t, run.runID, ready("Plan is ready."))
	launchCLIWorkerReport(t, run.runID, ready("Code is ready for tests."))
	launchCLIWorkerReport(t, run.runID, failed("Tests failed: go test ./..."))

	codeAfterTestPrompt := filepath.Join(run.root, "code-after-test.md")
	launchCLIWorkerReport(t, run.runID, withPromptPath(ready("Code fixed the test failure."), codeAfterTestPrompt))
	assertCLIOutputContainsAll(t, string(readCLIFile(t, codeAfterTestPrompt)), []string{
		"## Prior Report Context",
		"Tests failed: go test ./...",
		"- step_id: `code`",
		"- agent_id: `coder`",
	})

	launchCLIWorkerReport(t, run.runID, passed("Tests passed."))
	launchCLIWorkerReport(t, run.runID, changesRequested("Review found missing docs."))

	codeAfterReviewPrompt := filepath.Join(run.root, "code-after-review.md")
	launchCLIWorkerReport(t, run.runID, withPromptPath(ready("Code addressed review findings."), codeAfterReviewPrompt))
	assertCLIOutputContainsAll(t, string(readCLIFile(t, codeAfterReviewPrompt)), []string{
		"Review found missing docs.",
		"- step_id: `code`",
		"- agent_id: `coder`",
	})

	launchCLIWorkerReport(t, run.runID, passed("Tests passed after review fixes."))
	attemptsBeforeApprovalTerminal := len(loadCLIRun(t, run.root, run.runID).Status.Attempts)
	launchCLIWorkerReport(t, run.runID, approved("Review approved."))
	terminalizeCLIWorkflow(t, run.root, run.runID, cliStateReadyForHuman, attemptsBeforeApprovalTerminal+1, "Review approved.")
}

func TestWorkerLaunchNextRoutesImplementationBlockedReportsToHuman(t *testing.T) {
	tests := []struct {
		name         string
		prepare      []workerReport
		blockSummary string
	}{
		{
			name: "tester blocked",
			prepare: []workerReport{
				ready("Plan is ready."),
				ready("Code is ready for tests."),
			},
			blockSummary: "Tests require network approval.",
		},
		{
			name: "reviewer blocked",
			prepare: []workerReport{
				ready("Plan is ready."),
				ready("Code is ready for tests."),
				passed("Tests passed."),
			},
			blockSummary: "Review requires human product decision.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := startCLIImplementationReportRun(t)

			for _, report := range tt.prepare {
				launchCLIWorkerReport(t, run.runID, report)
			}

			attemptsBeforeBlockedTerminal := len(loadCLIRun(t, run.root, run.runID).Status.Attempts)
			launchCLIWorkerReport(t, run.runID, blocked(tt.blockSummary))
			terminalizeCLIWorkflow(t, run.root, run.runID, "blocked_for_human", attemptsBeforeBlockedTerminal+1, tt.blockSummary)
		})
	}
}

func TestExecuteWorkerLaunchNextUsesDefaultCodexCommand(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	if !strings.Contains(output, "result: failed/missing_report") {
		t.Fatalf("output missing successful shim missing_report result:\n%s\nlogs:\n%s", output, readCLILaunchLogs(t, root, result.runID))
	}

	assertCLIOutputContainsAll(t, output, []string{"launched attempt"})
	assertCLICodexArgs(t, shim, defaultCodexNormalArgsWithReasoning)
	assertCLIOutputContainsAll(t, string(readCLIFile(t, shim.stdinPath)), []string{
		"# Tiny Orc Worker Prompt\n",
		"- run_id: `" + result.runID + "`\n",
		"- step_id: `plan`\n",
		"- agent_id: `planner`\n",
	})
}

func TestExecuteWorkerLaunchNextUsesNormalDefaultWithSandboxConfigOutsideSandbox(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"launched attempt", "result: failed/missing_report"})
	assertCLICodexArgs(t, shim, defaultCodexNormalArgsWithReasoning)
}

func TestExecuteWorkerLaunchNextUsesSandboxCodexCommandInsideVerifiedSandbox(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"launched attempt", "result: failed/missing_report"})
	assertCLICodexArgs(t, shim, defaultCodexSandboxArgsWithReasoning)
}

func TestExecuteWorkerLaunchNextUsesNormalDefaultWhenSandboxMarkerDisabled(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)
	t.Setenv("ORC_SANDBOX", "0")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"launched attempt", "result: failed/missing_report"})
	assertCLICodexArgs(t, shim, defaultCodexNormalArgsWithReasoning)
}

func TestExecuteWorkerLaunchNextUsesNormalDefaultWhenSandboxRootInvalid(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", filepath.Join(root, "missing-sandbox-root"))

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"launched attempt", "result: failed/missing_report"})
	assertCLICodexArgs(t, shim, defaultCodexNormalArgsWithReasoning)
}

func TestExecuteWorkerLaunchNextRefusesWhenSandboxGuardMissingMarker(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  require_for_workers: true
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	t.Setenv("ORC_SANDBOX", "")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"worker", "launch-next", result.runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want sandbox guard refusal")
	}

	assertCLIOutputContainsAll(t, stderr.String(), []string{
		"sandbox.require_for_workers is enabled",
		"start the orchestrator with `orc sandbox run`",
		"missing ORC_SANDBOX=1",
	})

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want none after guard refusal", loaded.Status.ActiveAttempt)
	}
}

func TestExecuteWorkerLaunchNextRefusesWhenSandboxGuardRootMismatches(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  require_for_workers: true
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", t.TempDir())

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"worker", "launch-next", result.runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want sandbox root mismatch refusal")
	}

	assertCLIOutputContainsAll(t, stderr.String(), []string{
		"sandbox.require_for_workers is enabled",
		"ORC_SANDBOX_ROOT",
		"does not match current repo root",
		"start the orchestrator with `orc sandbox run`",
	})

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want none after guard refusal", loaded.Status.ActiveAttempt)
	}
}

func TestExecuteWorkerLaunchNextAllowsMatchingSandboxGuard(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProjectWithSandbox(t, root, `sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  require_for_workers: true
`)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	assertCLIOutputContainsAll(t, output, []string{"launched attempt", "result: failed/missing_report"})
	assertCLICodexArgs(t, shim, defaultCodexSandboxArgsWithReasoning)
}

func TestConcurrentWorkerLaunchNextOnlyStartsOneWorker(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	counterPath := filepath.Join(root, "codex-starts.txt")
	env := shim.processEnv("count-starts", "ORC_CLI_CODEX_COUNTER="+counterPath)

	first := startCLIProcess(t, root, env, "worker", "launch-next", result.runID)
	second := startCLIProcess(t, root, env, "worker", "launch-next", result.runID)
	firstErr := first.wait()

	secondErr := second.wait()
	if firstErr == nil && secondErr == nil {
		t.Fatalf("both launch-next commands succeeded, want one refusal\nfirst:\n%s\nsecond:\n%s", first.output(), second.output())
	}

	starts := strings.Fields(string(readCLIFile(t, counterPath)))
	if got := len(starts); got != 1 {
		t.Fatalf("codex starts = %d (%v), want exactly one\nfirst:\n%s\nsecond:\n%s", got, starts, first.output(), second.output())
	}

	combined := first.output() + "\n" + second.output()
	if !strings.Contains(combined, "result: failed/missing_report") {
		t.Fatalf("combined output missing launched attempt result:\n%s", combined)
	}

	if !strings.Contains(combined, "already has starting attempt") && !strings.Contains(combined, "already has active attempt") {
		t.Fatalf("combined output missing active-attempt refusal:\n%s", combined)
	}
}

func TestWorkerLaunchNextSignalCancelsWorkerProcessGroup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	childPIDPath := filepath.Join(root, "signal-child.pid")
	cmd := exec.CommandContext(context.Background(), os.Args[0], "worker", "launch-next", result.runID)
	cmd.Dir = root
	cmd.Env = shim.processEnv("child-pid", "ORC_CLI_CODEX_CHILD_PID="+childPIDPath)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orc CLI process: %v", err)
	}

	eventuallyCLI(t, time.Second, func() bool {
		_, err := os.Stat(childPIDPath)
		return err == nil
	})

	childPID := readCLIPIDFile(t, childPIDPath)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal orc CLI process: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	waitErr := make(chan error, 1)

	go func() {
		waitErr <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()

		t.Fatal("orc CLI process did not exit after SIGTERM")
	case err := <-waitErr:
		if err == nil {
			t.Fatalf("orc CLI process returned nil error, want nonzero cancellation exit\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	}

	eventuallyCLI(t, time.Second, func() bool {
		return !cliProcessExists(childPID)
	})

	output := stdout.String()
	if !strings.Contains(output, "result: failed/process_error") {
		t.Fatalf("stdout missing cancellation result:\n%s\nstderr:\n%s", output, stderr.String())
	}

	if !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("stderr missing context canceled:\n%s", stderr.String())
	}
}
