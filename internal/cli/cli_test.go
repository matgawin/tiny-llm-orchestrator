package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/progress"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

const (
	reportIgnoredEvent    = "report.ignored"
	cliStateRunning       = "running"
	cliStateReadyForHuman = "ready_for_human"
)

func TestMain(m *testing.M) {
	if os.Getenv("ORC_CLI_CODEX_SHIM") == "1" && filepath.Base(os.Args[0]) == "codex" {
		cliCodexShimMain()
		return
	}
	if os.Getenv("ORC_CLI_EXECUTE") == "1" {
		if err := Execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func cliCodexShimMain() {
	switch os.Getenv("ORC_CLI_CODEX_MODE") {
	case "child-pid":
		cliCodexShimChildPID(os.Getenv("ORC_CLI_CODEX_CHILD_PID"))
	case "count-starts":
		cliCodexShimCounter(os.Getenv("ORC_CLI_CODEX_COUNTER"))
	case "record-prompt":
		cliCodexShimRecordPrompt(os.Getenv("ORC_CLI_CODEX_ARGS"), os.Getenv("ORC_CLI_CODEX_STDIN"))
	case "worker-report":
		cliCodexShimWorkerReport()
	default:
		os.Exit(2)
	}
}

func cliCodexShimChildPID(childPIDPath string) {
	if childPIDPath == "" {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo $$ > "+shellQuoteCLI(childPIDPath)+"; trap \"\" TERM; sleep 30")
	if err := cmd.Run(); err != nil {
		os.Exit(6)
	}
}

func cliCodexShimCounter(counterPath string) {
	if counterPath == "" {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	file, err := os.OpenFile(counterPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(7)
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		_ = file.Close()
		os.Exit(8)
	}
	if err := file.Close(); err != nil {
		os.Exit(9)
	}
	time.Sleep(200 * time.Millisecond)
}

func cliCodexShimRecordPrompt(argsPath, stdinPath string) {
	if argsPath == "" || stdinPath == "" {
		os.Exit(2)
	}
	if err := os.WriteFile(argsPath, []byte(strings.Join(os.Args[1:], "\n")+"\n"), 0o600); err != nil {
		os.Exit(3)
	}
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(4)
	}
	if err := os.WriteFile(stdinPath, content, 0o600); err != nil {
		os.Exit(5)
	}
	time.Sleep(50 * time.Millisecond)
}

func cliCodexShimWorkerReport() {
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(4)
	}
	if stdinPath := os.Getenv("ORC_CLI_CODEX_STDIN"); stdinPath != "" {
		if err := os.WriteFile(stdinPath, content, 0o600); err != nil {
			os.Exit(5)
		}
	}
	prompt := string(content)
	status := os.Getenv("ORC_CLI_CODEX_REPORT_STATUS")
	if status == "" {
		status = "done"
	}
	result := os.Getenv("ORC_CLI_CODEX_REPORT_RESULT")
	if result == "" {
		result = "ready"
	}
	summary := os.Getenv("ORC_CLI_CODEX_REPORT_SUMMARY")
	if summary == "" {
		summary = "Worker report is ready."
	}
	if message := os.Getenv("ORC_CLI_CODEX_PROGRESS"); message != "" {
		if err := Execute([]string{"progress", message}, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
	}
	args := []string{
		"report",
		"--run", promptValue(prompt, "run_id"),
		"--step", promptValue(prompt, "step_id"),
		"--agent", promptValue(prompt, "agent_id"),
		"--attempt", promptValue(prompt, "attempt_id"),
		"--status", status,
		"--result", result,
		"--summary", summary,
	}
	if err := Execute(args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

func promptValue(prompt, name string) string {
	prefix := "- " + name + ": `"
	for line := range strings.SplitSeq(prompt, "\n") {
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, "`") {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), "`")
		}
	}
	return ""
}

type cliCodexShim struct {
	binDir    string
	argsPath  string
	stdinPath string
}

func installCLICodexShim(t *testing.T, root string) cliCodexShim {
	t.Helper()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o750); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.Symlink(os.Args[0], filepath.Join(binDir, "codex")); err != nil {
		t.Fatalf("symlink codex shim: %v", err)
	}
	return cliCodexShim{
		binDir:    binDir,
		argsPath:  filepath.Join(root, "codex-args.txt"),
		stdinPath: filepath.Join(root, "codex-stdin.md"),
	}
}

func installCLIOrcExecutable(t *testing.T, root string) string {
	t.Helper()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	orcPath := filepath.Join(binDir, "orc")
	if err := os.Symlink(os.Args[0], orcPath); err != nil {
		t.Fatalf("symlink orc executable: %v", err)
	}
	t.Setenv("ORC_CLI_EXECUTE", "1")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return orcPath
}

func (s cliCodexShim) setDefaultEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", s.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "record-prompt")
	t.Setenv("ORC_CLI_CODEX_ARGS", s.argsPath)
	t.Setenv("ORC_CLI_CODEX_STDIN", s.stdinPath)
}

func assertCLICodexArgs(t *testing.T, shim cliCodexShim, want string) {
	t.Helper()
	if got := string(readCLIFile(t, shim.argsPath)); got != want {
		t.Fatalf("codex args = %q, want %q", got, want)
	}
}

func (s cliCodexShim) processEnv(mode string, extra ...string) []string {
	env := append([]string{
		"ORC_CLI_EXECUTE=1",
		"ORC_CLI_CODEX_SHIM=1",
		"ORC_CLI_CODEX_MODE=" + mode,
		"PATH=" + s.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, extra...)
	return append(os.Environ(), env...)
}

func writeCLIProject(t *testing.T, root, beads string, markdownFallback bool) {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		Beads:            beads,
		MarkdownFallback: markdownFallback,
	})
}

func writeCLIProjectWithSandbox(t *testing.T, root, sandboxConfig string) {
	t.Helper()
	writeCLIProject(t, root, "optional", true)
	configPath := filepath.Join(root, ".orc", "config.yaml")
	content := string(readCLIFile(t, configPath))
	writeCLIFile(t, configPath, content+sandboxConfig)
}

func writeCLIImplementationProject(t *testing.T, root string) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeCLIRuntime(t, orcDir)
	writeCLIFile(t, filepath.Join(orcDir, "config.yaml"), `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
  coder: agents/coder.md
  tester: agents/tester.md
  reviewer: agents/reviewer.md
runtimes:
  codex: runtimes/codex.yaml
`)
	writeCLIFile(t, filepath.Join(orcDir, "agents", "planner.md"), cliAgentDescriptor("planner", "Creates implementation plans."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "coder.md"), cliAgentDescriptor("coder", "Implements code changes."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "tester.md"), cliAgentDescriptor("tester", "Runs verification."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), cliAgentDescriptor("reviewer", "Reviews completed work."))
	writeCLIFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), string(readCLITestdata(t, "implementation_workflow.yaml")))
}

func writeCLISkipStepProject(t *testing.T, root string, reviewSkippable bool) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeCLIRuntime(t, orcDir)
	writeCLIFile(t, filepath.Join(orcDir, "config.yaml"), `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  reviewer: agents/reviewer.md
  coder: agents/coder.md
runtimes:
  codex: runtimes/codex.yaml
`)
	writeCLIFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), cliAgentDescriptor("reviewer", "Reviews completed work."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "coder.md"), cliAgentDescriptor("coder", "Implements code changes."))
	reviewSkipFields := ""
	reviewDoneResults := "approved, changes_requested"
	reviewSkipTransition := ""
	if reviewSkippable {
		reviewSkipFields = "    skippable: true\n"
		reviewDoneResults = "approved, changes_requested, skipped"
		reviewSkipTransition = "      done/skipped: code\n"
	}
	writeCLIFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), `name: implementation
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
  retries:
    failed/error: 1
steps:
  review:
    agent: reviewer
`+reviewSkipFields+`    allowed_results:
      done: [`+reviewDoneResults+`]
      failed: [error]
    on:
      done/approved: ready_for_human
      done/changes_requested: code
`+reviewSkipTransition+`      failed/error: blocked_for_human
  code:
    agent: coder
    skippable: true
    allowed_results:
      done: [ready, skipped]
      failed: [error]
    on:
      done/ready: ready_for_human
      done/skipped: ready_for_human
      failed/error: blocked_for_human
`)
}

func writeCLIAdvanceCommandProject(t *testing.T, root, reviewStep, loopCaps string) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeCLIRuntime(t, orcDir)
	writeCLIFile(t, filepath.Join(orcDir, "config.yaml"), `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  reviewer: agents/reviewer.md
runtimes:
  codex: runtimes/codex.yaml
`)
	writeCLIFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), cliAgentDescriptor("reviewer", "Reviews completed work."))
	if strings.TrimSpace(reviewStep) == "" {
		reviewStep = `    kind: command
    command:
      argv: ["sh", "-c", "exit 0"]
`
	}
	var caps strings.Builder
	if strings.TrimSpace(loopCaps) != "" {
		caps.WriteString("  loop_caps:\n")
		for line := range strings.SplitSeq(strings.TrimRight(loopCaps, "\n"), "\n") {
			caps.WriteString("    " + line + "\n")
		}
	}
	writeCLIFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 5s
  report_exit_grace: 30ms
  runtime: codex
  retries: {}
`+caps.String()+`steps:
  plan:
    kind: command
    command:
      argv: ["sh", "-c", "exit 0"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: code
      done/failed: blocked_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
  code:
    kind: command
    command:
      argv: ["sh", "-c", "exit 0"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: test
      done/failed: blocked_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
  test:
    kind: command
    command:
      argv: ["sh", "-c", "exit 0"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: review
      done/failed: code
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
  review:
`+reviewStep+`    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: ready_for_human
      done/failed: code
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
`)
}

func writeCLIAdvanceLoopProject(t *testing.T, root, loopCaps string) {
	t.Helper()
	writeCLIAdvanceCommandProject(t, root, "", loopCaps)
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	content := string(readCLIFile(t, workflowPath))
	content = strings.Replace(content, "      done/passed: code", "      done/passed: plan", 1)
	writeCLIFile(t, workflowPath, content)
}

func cliAgentDescriptor(id, description string) string {
	return fmt.Sprintf(`---
id: %s
role: %s
description: %s
---

%s role instructions.
`, id, id, description, id)
}

func writeCLIRuntime(t *testing.T, orcDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(orcDir, "runtimes"), 0o750); err != nil {
		t.Fatalf("mkdir runtimes: %v", err)
	}
	writeCLIFile(t, filepath.Join(orcDir, "runtimes", "codex.yaml"), testutil.CodexRuntimeYAML())
}

func openCLIStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func loadCLIRun(t *testing.T, root, runID string) *runstore.Run {
	t.Helper()
	loaded, err := openCLIStore(t, root).Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return loaded
}

func blockCLIWorkflowLoopHardCap(t *testing.T, root, runID, state string, current, prospective int) {
	t.Helper()
	store := openCLIStore(t, root)
	if _, _, err := store.BlockWorkflowLoopHardCap(runID, runstore.WorkflowLoopHardCap{
		Workflow:         "implementation",
		BlockedState:     state,
		CurrentCount:     current,
		ProspectiveCount: prospective,
		Soft:             1,
		Hard:             current,
		Reason:           runstore.WorkflowLoopHardCapReason,
	}, time.Date(2026, 5, 2, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("BlockWorkflowLoopHardCap returned error: %v", err)
	}
}

type cliImplementationRun struct {
	root  string
	runID string
}

func startCLIImplementationReportRun(t *testing.T) cliImplementationRun {
	t.Helper()
	root := withTempCwd(t)
	writeCLIImplementationProject(t, root)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	t.Setenv("PATH", shim.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")
	return cliImplementationRun{root: root, runID: result.runID}
}

type workerReport struct {
	status     string
	result     string
	summary    string
	promptPath string
}

func ready(summary string) workerReport {
	return done("ready", summary)
}

func failed(summary string) workerReport {
	return done("failed", summary)
}

func passed(summary string) workerReport {
	return done("passed", summary)
}

func changesRequested(summary string) workerReport {
	return done("changes_requested", summary)
}

func approved(summary string) workerReport {
	return done("approved", summary)
}

func blocked(summary string) workerReport {
	return workerReport{status: "blocked", result: "blocked", summary: summary}
}

func done(result, summary string) workerReport {
	return workerReport{status: "done", result: result, summary: summary}
}

func withPromptPath(report workerReport, path string) workerReport {
	report.promptPath = path
	return report
}

func launchCLIWorkerReport(t *testing.T, runID string, report workerReport) string {
	t.Helper()
	t.Setenv("ORC_CLI_CODEX_REPORT_STATUS", report.status)
	t.Setenv("ORC_CLI_CODEX_REPORT_RESULT", report.result)
	t.Setenv("ORC_CLI_CODEX_REPORT_SUMMARY", report.summary)
	t.Setenv("ORC_CLI_CODEX_STDIN", report.promptPath)
	output := executeCLICommand(t, []string{"worker", "launch-next", runID})
	if !strings.Contains(output, "result: "+report.status+"/"+report.result) {
		t.Fatalf("output = %q, want result %s/%s", output, report.status, report.result)
	}
	return output
}

func terminalizeCLIWorkflow(t *testing.T, root, runID, wantState string, wantAttempts int, wantSummary string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"worker", "launch-next", runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want terminal no-launch error")
	}
	if !strings.Contains(stderr.String(), "transitioned to "+wantState) {
		t.Fatalf("stderr = %q, want terminal transition to %s", stderr.String(), wantState)
	}
	loaded := loadCLIRun(t, root, runID)
	if loaded.Status.State != wantState {
		t.Fatalf("run state = %q, want %s", loaded.Status.State, wantState)
	}
	if got := len(loaded.Status.Attempts); got != wantAttempts {
		t.Fatalf("attempt history len = %d, want no relaunch beyond %d attempts", got, wantAttempts)
	}
	latest := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if latest.Report == nil || latest.Report.Summary != wantSummary {
		t.Fatalf("latest report = %+v, want summary %q visible", latest.Report, wantSummary)
	}
}

func writeCurrentAttemptJSONReport(t *testing.T, extraFields string) (string, string, string) {
	t.Helper()
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	jsonPath := filepath.Join(root, "report.json")
	writeCLIFile(t, jsonPath, currentAttemptJSONReport(result.runID, extraFields))
	return root, result.runID, jsonPath
}

func currentAttemptJSONReport(runID, extraFields string) string {
	extra := ""
	if strings.TrimSpace(extraFields) != "" {
		extra = ",\n  " + extraFields
	}
	return fmt.Sprintf(`{
  "run_id": %q,
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "attempt-001",
  "status": "done",
  "result": "ready",
  "summary": "Plan is ready."%s
}`, runID, extra)
}

func assertCLILatestAttemptState(t *testing.T, root, runID, state string) runstore.Attempt {
	t.Helper()
	loaded, loadErr := openCLIStore(t, root).Load(runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != state {
		t.Fatalf("attempt state = %q, want %s", attempt.State, state)
	}
	return attempt
}

func startCLIActiveAttempt(t *testing.T, root, runID, attemptID string) {
	t.Helper()
	startCLIActiveAttemptForStep(t, root, runID, attemptID, "plan", "planner")
}

func startCLIActiveAttemptForStep(t *testing.T, root, runID, attemptID, stepID, agentID string) {
	t.Helper()
	store := openCLIStore(t, root)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          stepID,
		AgentID:         agentID,
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan-" + attemptID,
		Content: []byte("prompt\n"),
		Time:    time.Date(2026, 5, 4, 12, 0, 1, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      time.Date(2026, 5, 4, 12, 0, 2, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan-" + attemptID,
		Content: []byte("log\n"),
		Time:    time.Date(2026, 5, 4, 12, 0, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      time.Date(2026, 5, 4, 12, 0, 4, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             time.Date(2026, 5, 4, 12, 0, 5, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
}

func recordCLIReportedAttempt(t *testing.T, root, runID, attemptID, stepID, agentID, status, result string) {
	t.Helper()
	startCLIActiveAttemptForStep(t, root, runID, attemptID, stepID, agentID)
	if _, _, err := openCLIStore(t, root).RecordAttemptReport(runID, runstore.RecordReportRequest{
		Report: runstore.Report{
			RunID:     runID,
			StepID:    stepID,
			AgentID:   agentID,
			AttemptID: attemptID,
			Status:    status,
			Result:    result,
			Summary:   "reported " + status + "/" + result,
		},
		State: runstore.AttemptStateReported,
		Time:  time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}
}

func startCLIStartingAttempt(t *testing.T, root, runID, attemptID string) {
	t.Helper()
	store := openCLIStore(t, root)
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          "plan",
		AgentID:         "planner",
		AttemptID:       attemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
}

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fakeCLIBDPath(t *testing.T, beadID, beadsDir string, allowShow bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bd")
	var script string
	if allowShow {
		script = fmt.Sprintf(`#!/bin/sh
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
	} else {
		script = `#!/bin/sh
printf 'bd must not be called after run start\n' >&2
exit 9
`
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return dir
}

type cliTaskSnapshot struct {
	Source struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
	BeadLookup struct {
		Attempted bool   `json:"attempted"`
		OK        bool   `json:"ok"`
		BeadID    string `json:"bead_id"`
	} `json:"bead_lookup"`
	Fallback struct {
		Used bool `json:"used"`
	} `json:"fallback"`
}

type cliRunStartResult struct {
	runID    string
	snapshot cliTaskSnapshot
}

func executeCLIRunStart(t *testing.T, root string, sourceArgs []string, stdin *strings.Reader) cliRunStartResult {
	t.Helper()
	args := append([]string{"run", "start", "--workflow", "implementation"}, sourceArgs...)
	var stdout, stderr bytes.Buffer
	var err error
	if stdin == nil {
		err = Execute(args, &stdout, &stderr)
	} else {
		err = ExecuteWithInput(args, stdin, &stdout, &stderr)
	}
	if err != nil {
		t.Fatalf("run start returned error: %v\nstderr: %s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	runID := cliStartedRunID(t, stdout.String())
	return cliRunStartResult{
		runID:    runID,
		snapshot: readCLISnapshot(t, root, runID),
	}
}

func startCLIBeadBackedRunThenBlockBD(t *testing.T, root string) cliRunStartResult {
	t.Helper()
	beadsDir := filepath.Join(root, "..", ".beads")
	beadID := "main-readable"
	t.Setenv("PATH", fakeCLIBDPath(t, beadID, beadsDir, true))
	t.Setenv("BEADS_DIR", beadsDir)
	result := executeCLIRunStart(t, root, []string{"--bead", beadID}, nil)
	t.Setenv("PATH", fakeCLIBDPath(t, beadID, beadsDir, false))
	return result
}

func executeCLICommand(t *testing.T, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := Execute(args, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	return stdout.String()
}

func newCLIProgressListener(t *testing.T) *progress.Listener {
	t.Helper()
	l, err := progress.NewListener()
	if err != nil {
		t.Fatalf("NewListener returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	if err := l.Register(progress.Registration{
		RunID:     "run-001",
		StepID:    "code",
		AttemptID: "attempt-001",
		Token:     "token-001",
	}); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	return l
}

func setCLIProgressEnv(t *testing.T, socketPath, token string) {
	t.Helper()
	t.Setenv("ORC_PROGRESS_SOCKET", socketPath)
	t.Setenv("ORC_RUN_ID", "run-001")
	t.Setenv("ORC_STEP_ID", "code")
	t.Setenv("ORC_ATTEMPT_ID", "attempt-001")
	t.Setenv("ORC_PROGRESS_TOKEN", token)
}

func receiveCLIProgress(t *testing.T, l *progress.Listener) progress.AcceptedMessage {
	t.Helper()
	select {
	case msg := <-l.Accepted():
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for accepted progress message")
		return progress.AcceptedMessage{}
	}
}

func assertNoCLIProgress(t *testing.T, l *progress.Listener) {
	t.Helper()
	select {
	case msg := <-l.Accepted():
		t.Fatalf("unexpected accepted progress message: %+v", msg)
	case <-time.After(25 * time.Millisecond):
	}
}

type cliProcessResult struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func startCLIProcess(t *testing.T, root string, env []string, args ...string) *cliProcessResult {
	t.Helper()
	result := &cliProcessResult{cmd: exec.CommandContext(context.Background(), os.Args[0], args...)}
	result.cmd.Dir = root
	result.cmd.Env = env
	result.cmd.Stdout = &result.stdout
	result.cmd.Stderr = &result.stderr
	if err := result.cmd.Start(); err != nil {
		t.Fatalf("start CLI process %v: %v", args, err)
	}
	return result
}

func (r *cliProcessResult) wait() error {
	if err := r.cmd.Wait(); err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	return nil
}

func (r *cliProcessResult) output() string {
	return "stdout:\n" + r.stdout.String() + "\nstderr:\n" + r.stderr.String()
}

func cliStartedRunID(t *testing.T, output string) string {
	t.Helper()
	if !strings.HasPrefix(output, "started run ") {
		t.Fatalf("stdout = %q, want started run", output)
	}
	runID := strings.TrimSpace(strings.TrimPrefix(output, "started run "))
	if runID == "" {
		t.Fatalf("stdout = %q, want non-empty run id", output)
	}
	return runID
}

func cliTaskArtifactPath(root, runID, name string) string {
	return filepath.Join(root, ".orc", "runs", runID, "task", name)
}

func readCLISnapshot(t *testing.T, root, runID string) cliTaskSnapshot {
	t.Helper()
	var snapshot cliTaskSnapshot
	if err := json.Unmarshal(readCLIFile(t, cliTaskArtifactPath(root, runID, "snapshot.json")), &snapshot); err != nil {
		t.Fatalf("unmarshal task snapshot: %v", err)
	}
	return snapshot
}

func readCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func readCLITestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve CLI testdata path")
	}
	return readCLIFile(t, filepath.Join(filepath.Dir(file), "testdata", name))
}

func latestCLIArtifactRef(t *testing.T, run *runstore.Run, kind runstore.ArtifactKind) runstore.ArtifactRef {
	t.Helper()
	var ref runstore.ArtifactRef
	for _, candidate := range run.Status.Artifacts {
		if candidate.Kind != kind {
			continue
		}
		if ref.Path == "" || candidate.EventSequence > ref.EventSequence {
			ref = candidate
		}
	}
	if ref.Path == "" {
		t.Fatalf("run %s has no %s artifact", run.ID, kind)
	}
	return ref
}

func latestCLIArtifactContent(t *testing.T, root, runID string, kind runstore.ArtifactKind) (runstore.ArtifactRef, string) {
	t.Helper()
	store := openCLIStore(t, root)
	run, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	ref := latestCLIArtifactRef(t, run, kind)
	content, err := store.ReadArtifact(runID, ref)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	return ref, string(content)
}

func readCLILaunchLogs(t *testing.T, root, runID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".orc", "runs", runID, "logs", "*.log"))
	if err != nil {
		t.Fatalf("glob launch logs: %v", err)
	}
	var out strings.Builder
	for _, path := range matches {
		out.WriteString(path)
		out.WriteByte('\n')
		out.Write(readCLIFile(t, path))
		out.WriteByte('\n')
	}
	return out.String()
}

func assertCLIOutputContainsAll(t *testing.T, output string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func eventuallyCLI(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if condition() {
		return
	}
	t.Fatalf("condition not met within %s", timeout)
}

func readCLIPIDFile(t *testing.T, path string) int {
	t.Helper()
	content := strings.TrimSpace(string(readCLIFile(t, path)))
	pid, err := strconv.Atoi(content)
	if err != nil {
		t.Fatalf("parse pid %q: %v", content, err)
	}
	return pid
}

func cliProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func shellQuoteCLI(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
