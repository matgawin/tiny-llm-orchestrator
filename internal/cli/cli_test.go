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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

const reportIgnoredEvent = "report.ignored"

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
	cmd := exec.Command("sh", "-c", "echo $$ > "+shellQuoteCLI(childPIDPath)+"; trap \"\" TERM; sleep 30")
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

func (s cliCodexShim) setDefaultEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", s.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "record-prompt")
	t.Setenv("ORC_CLI_CODEX_ARGS", s.argsPath)
	t.Setenv("ORC_CLI_CODEX_STDIN", s.stdinPath)
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

func TestExecuteHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"Usage:", "Available Commands:", "init", "run", "worker", "version"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	oldVersion := version
	version = defaultVersion
	t.Cleanup(func() {
		version = oldVersion
	})

	if err := Execute([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got, want := stdout.String(), "orc dev\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"nope"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, `unknown command "nope"`) {
		t.Fatalf("stderr = %q, want unknown command message", got)
	}
}

func TestExecuteRunStartInlineTaskCreatesRun(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	if got := string(readCLIFile(t, cliTaskArtifactPath(root, result.runID, "context.md"))); got != "# Task\n" {
		t.Fatalf("task context = %q, want inline task with newline", got)
	}
}

func TestExecuteRunInspectCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(runID string) []string
		want string
	}{
		{
			name: "status",
			args: func(runID string) []string { return []string{"run", "status", runID} },
			want: "state: running",
		},
		{
			name: "next",
			args: func(runID string) []string { return []string{"run", "next", runID} },
			want: "decision: select_step",
		},
		{
			name: "summary-context",
			args: func(runID string) []string { return []string{"run", "summary-context", runID} },
			want: "# Summary Context",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

			output := executeCLICommand(t, tc.args(result.runID))
			if !strings.Contains(output, tc.want) {
				t.Fatalf("%s output missing %q:\n%s", tc.name, tc.want, output)
			}
		})
	}
}

func TestExecuteRunInspectUnknownRunFailsClearly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "status", args: []string{"run", "status", "missing-run"}, want: `orc run status: run "missing-run" not found`},
		{name: "next", args: []string{"run", "next", "missing-run"}, want: `orc run next: run "missing-run" not found`},
		{name: "summary-context", args: []string{"run", "summary-context", "missing-run"}, want: `orc run summary-context: run "missing-run" not found`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)

			var stdout, stderr bytes.Buffer
			if err := Execute(tc.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want missing run failure")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecuteRunAddFollowupAppendsFollowup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	output := executeCLICommand(t, []string{
		"run", "add-followup", result.runID,
		"--title", "Create release note",
		"--details", "Mention the follow-up recorder.",
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
	beadsDir := filepath.Join(root, "..", ".beads")
	beadID := "main-readable"
	path := fakeCLIBDPath(t, beadID, beadsDir, true)
	t.Setenv("PATH", path)
	t.Setenv("BEADS_DIR", beadsDir)
	result := executeCLIRunStart(t, root, []string{"--bead", beadID}, nil)

	t.Setenv("PATH", fakeCLIBDPath(t, beadID, beadsDir, false))
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

func TestExecuteReportFlagsPersistsCurrentAttemptReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "detail.md")
	writeCLIFile(t, reportPath, "## Detail\n")

	output := executeCLICommand(t, []string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "attempt-001",
		"--status", "done",
		"--result", "ready",
		"--summary", "Plan is ready.",
		"--changed-path", "README.md",
		"--command", "go test ./internal/cli",
		"--test", "go test ./internal/cli",
		"--risk", "none",
		"--follow-up", "Document report summaries",
		"--report-file", reportPath,
	})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})
	store := openCLIStore(t, root)
	loaded, err := store.Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateReported || attempt.Report == nil {
		t.Fatalf("attempt = %+v, want reported attempt with report", attempt)
	}
	if attempt.Report.ChangedPaths[0] != "README.md" || attempt.Report.Commands[0] != "go test ./internal/cli" {
		t.Fatalf("report = %+v, want preserved optional fields", attempt.Report)
	}
	if attempt.Report.Tests[0] != "go test ./internal/cli" || attempt.Report.Risks[0] != "none" || attempt.Report.Followups[0].Title != "Document report summaries" {
		t.Fatalf("report = %+v, want preserved tests, risks, and followups", attempt.Report)
	}
	if attempt.ReportRef == nil || attempt.ReportRef.Kind != runstore.KindReport {
		t.Fatalf("report ref = %+v, want report artifact ref", attempt.ReportRef)
	}
	if attempt.Report.ReportRef == nil || *attempt.Report.ReportRef != *attempt.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", attempt.Report.ReportRef, attempt.ReportRef)
	}
	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, filepath.FromSlash(attempt.ReportRef.Path)))); got != "## Detail\n" {
		t.Fatalf("report detail = %q, want copied markdown", got)
	}
	followups := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, followups, []string{
		"## Document report summaries",
		"Source: report",
		"Step: plan",
		"Agent: planner",
		"Attempt: attempt-001",
	})
}

func TestExecuteReportBadReportFileTerminalizesInvalidReport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		makePath  func(t *testing.T, root string) string
		wantError string
	}{
		{
			name: "missing",
			makePath: func(t *testing.T, root string) string {
				t.Helper()
				return filepath.Join(root, "missing.md")
			},
			wantError: "report_file",
		},
		{
			name: "directory",
			makePath: func(t *testing.T, root string) string {
				t.Helper()
				path := filepath.Join(root, "report-dir")
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatalf("Mkdir returned error: %v", err)
				}
				return path
			},
			wantError: "not a regular file",
		},
		{
			name: "unreadable",
			makePath: func(t *testing.T, root string) string {
				t.Helper()
				path := filepath.Join(root, "unreadable.md")
				writeCLIFile(t, path, "## Detail\n")
				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("Chmod returned error: %v", err)
				}
				t.Cleanup(func() {
					_ = os.Chmod(path, 0o600)
				})
				file, err := os.Open(path)
				if err == nil {
					_ = file.Close()
					t.Skip("current user can read mode 000 files")
				}
				return path
			},
			wantError: "report_file",
		},
		{
			name: "symlink",
			makePath: func(t *testing.T, root string) string {
				t.Helper()
				target := filepath.Join(root, "target.md")
				link := filepath.Join(root, "link.md")
				writeCLIFile(t, target, "## Detail\n")
				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("Symlink returned error: %v", err)
				}
				return link
			},
			wantError: "report_file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")
			reportPath := tc.makePath(t, root)

			var stdout, stderr bytes.Buffer
			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "done",
				"--result", "ready",
				"--summary", "Plan is ready.",
				"--report-file", reportPath,
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want report_file error")
			}
			if !strings.Contains(stderr.String(), tc.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantError)
			}
			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport || attempt.Report == nil {
				t.Fatalf("attempt = %+v, want invalid_report with report", attempt)
			}
			if attempt.ReportRef != nil || attempt.Report.ReportRef != nil {
				t.Fatalf("report refs = %+v/%+v, want none for invalid report_file", attempt.ReportRef, attempt.Report.ReportRef)
			}
		})
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
	if loaded.Status.State != "ready_for_human" {
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
	terminalizeCLIWorkflow(t, run.root, run.runID, "ready_for_human", attemptsBeforeApprovalTerminal+1, "Review approved.")
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

func TestExecuteReportRejectsReservedSystemOutcomes(t *testing.T) {
	for _, reserved := range []string{"invalid_report", "missing_report", "timeout", "process_error", "error"} {
		t.Run(reserved, func(t *testing.T) {
			root := withTempCwd(t)
			testutil.WriteProject(t, root, testutil.ProjectOptions{
				Beads:            "optional",
				MarkdownFallback: true,
				FailedResults:    []string{"invalid_report", "missing_report", "timeout", "process_error", "error"},
			})
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer
			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "failed",
				"--result", reserved,
				"--summary", "Trying to claim a system outcome.",
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want reserved outcome rejection")
			}
			if !strings.Contains(stderr.String(), "reserved system outcome failed/"+reserved) {
				t.Fatalf("stderr = %q, want reserved system outcome error", stderr.String())
			}
			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport || attempt.Status != "failed" || attempt.Result != runstore.AttemptResultInvalidReport {
				t.Fatalf("attempt = %+v, want failed/invalid_report", attempt)
			}
		})
	}
}

func TestExecuteReportInvalidCurrentAttemptTerminalizesInvalidReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")

	var stdout, stderr bytes.Buffer
	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "attempt-001",
		"--status", "done",
		"--result", "not-allowed",
		"--summary", "Bad result.",
		"--follow-up", "Should not append",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want invalid report error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "does not allow done/not-allowed") {
		t.Fatalf("stderr = %q, want disallowed result", stderr.String())
	}
	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Status != "failed" || attempt.Result != runstore.AttemptResultInvalidReport {
		t.Fatalf("attempt = %+v, want failed/invalid_report", attempt)
	}
	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestExecuteReportWrongAttemptRecordsIgnoredBeforeConfigLoad(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	var stdout, stderr bytes.Buffer
	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "old-attempt",
		"--status", "done",
		"--result", "ready",
		"--summary", "Stale report.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want wrong attempt error")
	}
	if strings.Contains(stderr.String(), "load project config") {
		t.Fatalf("stderr = %q, want ignored report before config load", stderr.String())
	}
	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}
}

func TestExecuteReportShapeInvalidCurrentAttemptDoesNotLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing-status",
			args: []string{
				"report",
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--result", "ready",
				"--summary", "Missing status.",
			},
			want: "status is required",
		},
		{
			name: "reserved-outcome",
			args: []string{
				"report",
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "failed",
				"--result", "timeout",
				"--summary", "Reserved outcome.",
			},
			want: "reserved system outcome failed/timeout",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")
			writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

			args := append([]string{"report", "--run", result.runID}, tc.args[1:]...)
			var stdout, stderr bytes.Buffer
			err := Execute(args, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want shape validation error")
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			if strings.Contains(stderr.String(), "load project config") {
				t.Fatalf("stderr = %q, want no config load failure", stderr.String())
			}
			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport {
				t.Fatalf("attempt state = %q, want invalid_report", attempt.State)
			}
		})
	}
}

func TestExecuteReportMissingRequiredFieldTerminalizesInvalidReport(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(runID string) []string
		want string
	}{
		{
			name: "missing-status",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--result", "ready",
					"--summary", "Missing status.",
				}
			},
			want: "status is required",
		},
		{
			name: "missing-result",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--status", "done",
					"--summary", "Missing result.",
				}
			},
			want: "result is required",
		},
		{
			name: "blank-summary",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--status", "done",
					"--result", "ready",
					"--summary", " \t",
				}
			},
			want: "summary is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer
			err := Execute(tc.args(result.runID), &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want missing field error")
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport {
				t.Fatalf("attempt state = %q, want invalid_report", attempt.State)
			}
		})
	}
}

func TestExecuteReportJSONTrailingObjectTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, "")
	writeCLIFile(t, jsonPath, currentAttemptJSONReport(runID, "")+"\n"+`{"extra": true}`)

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want trailing JSON schema error")
	}
	if !strings.Contains(stderr.String(), "multiple JSON values are not allowed") {
		t.Fatalf("stderr = %q, want multiple JSON values error", stderr.String())
	}
	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONSchemaInvalidCurrentAttemptDoesNotLoadConfig(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"unexpected": true`)
	writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want schema validation error")
	}
	if !strings.Contains(stderr.String(), `unknown field "unexpected"`) {
		t.Fatalf("stderr = %q, want unknown field", stderr.String())
	}
	if strings.Contains(stderr.String(), "load project config") {
		t.Fatalf("stderr = %q, want no config load failure", stderr.String())
	}
	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONFilePersistsReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	jsonPath := filepath.Join(root, "report.json")
	writeCLIFile(t, jsonPath, fmt.Sprintf(`{
  "run_id": %q,
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "attempt-001",
  "status": "done",
  "result": "ready",
  "summary": "Plan is ready.",
  "changed_paths": ["README.md"],
  "commands": ["go test ./..."],
  "tests": ["task tests"],
  "risks": ["none"],
  "followups": [
    {"title": "Later", "details": "Capture summary context."}
  ]
}`, result.runID))

	output := executeCLICommand(t, []string{"report", "--json-file", jsonPath})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})
	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	report := loaded.Status.Attempts[len(loaded.Status.Attempts)-1].Report
	if report == nil || report.Commands[0] != "go test ./..." {
		t.Fatalf("report = %+v, want JSON command", report)
	}
	if report.ChangedPaths[0] != "README.md" || report.Tests[0] != "task tests" || report.Risks[0] != "none" {
		t.Fatalf("report = %+v, want JSON optional slices", report)
	}
	if report.Followups[0].Title != "Later" || report.Followups[0].Details != "Capture summary context." {
		t.Fatalf("followups = %+v, want JSON followup details", report.Followups)
	}
	followups := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, followups, []string{
		"## Later",
		"Source: report",
		"Step: plan",
		"Capture summary context.",
	})
}

func TestExecuteReportJSONFileCopiesMarkdownDetail(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "detail.md")
	writeCLIFile(t, reportPath, "")
	jsonPath := filepath.Join(root, "report.json")
	writeCLIFile(t, jsonPath, fmt.Sprintf(`{
  "run_id": %q,
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "attempt-001",
  "status": "done",
  "result": "ready",
  "summary": "Plan is ready.",
  "report_file": %q
}`, result.runID, reportPath))

	output := executeCLICommand(t, []string{"report", "--json-file", jsonPath})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})
	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.ReportRef == nil {
		t.Fatal("report ref = nil, want report artifact for empty report_file")
	}
	if attempt.Report == nil || attempt.Report.ReportRef == nil || *attempt.Report.ReportRef != *attempt.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", attempt.Report, attempt.ReportRef)
	}
	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, filepath.FromSlash(attempt.ReportRef.Path)))); got != "" {
		t.Fatalf("report detail = %q, want empty copied file", got)
	}
}

func TestExecuteReportRejectsJSONMixedWithFlags(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, "")

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath, "--summary", "mixed"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want mixed input rejection")
	}
	if !strings.Contains(stderr.String(), "--json-file cannot be combined") {
		t.Fatalf("stderr = %q, want JSON mixed rejection", stderr.String())
	}
	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONUnknownTopLevelFieldTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"surprise": true`)

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want unknown field error")
	}
	if !strings.Contains(stderr.String(), `unknown field "surprise"`) {
		t.Fatalf("stderr = %q, want unknown field", stderr.String())
	}
	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONReportRefTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"report_ref": {
    "kind": "report",
    "path": "reports/000001-plan.md",
    "event_sequence": 1
  }`)

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want report_ref schema error")
	}
	if !strings.Contains(stderr.String(), `unknown field "report_ref"`) {
		t.Fatalf("stderr = %q, want report_ref unknown field", stderr.String())
	}
	attempt := assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Report == nil {
		t.Fatalf("attempt = %+v, want invalid_report with preserved identity", attempt)
	}
	if attempt.Report.ReportRef != nil {
		t.Fatalf("report_ref = %+v, want caller-supplied ref cleared", attempt.Report.ReportRef)
	}
}

func TestExecuteReportJSONUnknownNestedFieldTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"followups": [
    {"title": "Later", "unexpected": true}
  ]`)

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want nested unknown field error")
	}
	if !strings.Contains(stderr.String(), `unknown field "unexpected"`) {
		t.Fatalf("stderr = %q, want nested unknown field", stderr.String())
	}
	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportWrongAttemptRecordsIgnoredEvent(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "ignored-detail.md")
	writeCLIFile(t, reportPath, "## Ignored\n")

	var stdout, stderr bytes.Buffer
	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "old-attempt",
		"--status", "done",
		"--result", "ready",
		"--summary", "Stale report.",
		"--report-file", reportPath,
		"--follow-up", "Should not append",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want wrong attempt error")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != "attempt-001" {
		t.Fatalf("active attempt = %+v, want unchanged attempt-001", loaded.Status.ActiveAttempt)
	}
	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}
	for _, artifact := range loaded.Status.Artifacts {
		if artifact.Kind == runstore.KindReport {
			t.Fatalf("artifact = %+v, want no report artifact for ignored report", artifact)
		}
	}
	if active := loaded.Status.ActiveAttempt; active == nil || active.ReportRef != nil || active.Report != nil {
		t.Fatalf("active attempt = %+v, want unchanged active attempt without report refs", active)
	}
	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestExecuteReportWrongStepAgentAndStartingAttemptRecordIgnoredEvent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		step       string
		agent      string
		start      func(t *testing.T, root, runID, attemptID string)
		wantActive string
	}{
		{name: "wrong-step", step: "future", agent: "planner", start: startCLIActiveAttempt, wantActive: runstore.AttemptStateActive},
		{name: "wrong-agent", step: "plan", agent: "other", start: startCLIActiveAttempt, wantActive: runstore.AttemptStateActive},
		{name: "starting", step: "plan", agent: "planner", start: startCLIStartingAttempt, wantActive: runstore.AttemptStateStarting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			tc.start(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer
			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", tc.step,
				"--agent", tc.agent,
				"--attempt", "attempt-001",
				"--status", "done",
				"--result", "ready",
				"--summary", "Ignored.",
				"--follow-up", "Should not append",
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want ignored report error")
			}
			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.State != tc.wantActive {
				t.Fatalf("active attempt = %+v, want unchanged %s", loaded.Status.ActiveAttempt, tc.wantActive)
			}
			if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
				t.Fatalf("last event type = %q, want report.ignored", got)
			}
			if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
				t.Fatalf("followups.md = %q, want unchanged empty file", got)
			}
		})
	}
}

func TestExecuteWorkerLaunchNextUsesDefaultCodexCommand(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	shim.setDefaultEnv(t)

	output := executeCLICommand(t, []string{"worker", "launch-next", result.runID})
	if !strings.Contains(output, "result: failed/missing_report") {
		t.Fatalf("output missing successful shim missing_report result:\n%s\nlogs:\n%s", output, readCLILaunchLogs(t, root, result.runID))
	}
	assertCLIOutputContainsAll(t, output, []string{"launched attempt"})
	if got := string(readCLIFile(t, shim.argsPath)); got != "--ask-for-approval\nnever\nexec\n--skip-git-repo-check\n-\n" {
		t.Fatalf("codex args = %q, want default launch command args", got)
	}
	assertCLIOutputContainsAll(t, string(readCLIFile(t, shim.stdinPath)), []string{
		"# Tiny Orc Worker Prompt\n",
		"- run_id: `" + result.runID + "`\n",
		"- step_id: `plan`\n",
		"- agent_id: `planner`\n",
	})
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
	cmd := exec.Command(os.Args[0], "worker", "launch-next", result.runID)
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

func TestExecuteRunStartStdinTaskCreatesRun(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	result := executeCLIRunStart(t, root, []string{"--task-stdin"}, strings.NewReader("# Stdin Task\n"))
	if got := string(readCLIFile(t, cliTaskArtifactPath(root, result.runID, "context.md"))); got != "# Stdin Task\n" {
		t.Fatalf("task context = %q, want stdin task", got)
	}
	if result.snapshot.Source.Type != "stdin_task" {
		t.Fatalf("snapshot source type = %q, want stdin_task", result.snapshot.Source.Type)
	}
}

func TestExecuteRunStartTaskFileCreatesRun(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	taskPath := filepath.Join(root, "task.md")
	writeCLIFile(t, taskPath, "# File Task\n")

	result := executeCLIRunStart(t, root, []string{"--task-file", taskPath}, nil)
	if result.snapshot.Source.Type != "task_file" || result.snapshot.Source.Path != taskPath {
		t.Fatalf("snapshot source = %+v, want task file %q", result.snapshot.Source, taskPath)
	}
}

func TestExecuteRunStartHelpShowsFallbackOnlyUnderBead(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"run", "start", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "orc run start --workflow <name> (--bead <id> [--fallback-task-file <path>] | --task-file <path> | --task <markdown> | --task-stdin)"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("help output missing grouped fallback usage %q:\n%s", want, stdout.String())
	}
}

func TestExecuteRunStartBeadFallbackCreatesRun(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	fallbackPath := filepath.Join(root, "fallback.md")
	writeCLIFile(t, fallbackPath, "# Fallback Task\n")

	t.Setenv("PATH", "")
	result := executeCLIRunStart(t, root, []string{"--bead", "missing-bead", "--fallback-task-file", fallbackPath}, nil)
	if result.snapshot.Source.Type != "fallback_task_file" || result.snapshot.Source.Path != fallbackPath {
		t.Fatalf("snapshot source = %+v, want fallback file %q", result.snapshot.Source, fallbackPath)
	}
	if !result.snapshot.BeadLookup.Attempted || result.snapshot.BeadLookup.OK || result.snapshot.BeadLookup.BeadID != "missing-bead" {
		t.Fatalf("bead lookup = %+v, want failed missing-bead lookup", result.snapshot.BeadLookup)
	}
	if !result.snapshot.Fallback.Used {
		t.Fatalf("fallback = %+v, want used fallback", result.snapshot.Fallback)
	}
}

func TestExecuteRunStartRejectsMissingTaskSource(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "start", "--workflow", "implementation"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want missing source failure")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "requires --bead, --task-file, --task, or --task-stdin") {
		t.Fatalf("stderr = %q, want noninteractive source failure", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".orc", "runs")); !os.IsNotExist(err) {
		t.Fatalf(".orc/runs stat err = %v, want no run directory", err)
	}
}

func TestExecuteRunStartRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"run", "start", "--bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	output := stderr.String()
	for _, want := range []string{`unknown flag "--bogus"`, "Usage:", "run start"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteInitDryRunUsesCurrentDirectory(t *testing.T) {
	withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "orc init dry-run:") {
		t.Fatalf("stdout = %q, want init dry-run routing", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitRejectsDryRunWithYes(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--dry-run", "--yes"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want invalid flags")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "orc init:") {
		t.Fatalf("stderr = %q, want init error context", got)
	}
}

func TestExecuteInitHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"orc init scaffolds", "Usage:", "--dry-run", "--yes"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	output := stderr.String()
	for _, want := range []string{`unknown flag "--bogus"`, "Usage:", "orc init"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteWithInputInitForwardsInteractiveInput(t *testing.T) {
	root := withTempCwd(t)

	if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".orc", "config.yaml"), []byte("custom: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	stdin := confirmThreeInitPromptsThroughCLI()
	if err := ExecuteWithInput([]string{"init"}, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("ExecuteWithInput returned error: %v\nstderr: %s", err, stderr.String())
	}

	if got := stdout.String(); !strings.Contains(got, "Overwrite .orc/config.yaml?") {
		t.Fatalf("stdout = %q, want forwarded interactive response", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func withTempCwd(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir temp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	return root
}

func confirmThreeInitPromptsThroughCLI() *strings.Reader {
	return strings.NewReader("yes\nyes\nyes\n")
}

func writeCLIProject(t *testing.T, root, beads string, markdownFallback bool) {
	t.Helper()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		Beads:            beads,
		MarkdownFallback: markdownFallback,
	})
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
	writeCLIFile(t, filepath.Join(orcDir, "config.yaml"), `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
  coder: agents/coder.md
  tester: agents/tester.md
  reviewer: agents/reviewer.md
`)
	writeCLIFile(t, filepath.Join(orcDir, "agents", "planner.md"), cliAgentDescriptor("planner", "Creates implementation plans."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "coder.md"), cliAgentDescriptor("coder", "Implements code changes."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "tester.md"), cliAgentDescriptor("tester", "Runs verification."))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), cliAgentDescriptor("reviewer", "Reviews completed work."))
	writeCLIFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  retries:
    failed/missing_report: 1
    failed/timeout: 0
    failed/invalid_report: 0
    failed/process_error: 1
    failed/error: 0
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/ready: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human
  code:
    agent: coder
    allowed_results:
      done: [ready]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/ready: test
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human
  test:
    agent: tester
    allowed_results:
      done: [passed, failed]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/passed: review
      done/failed: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human
  review:
    agent: reviewer
    allowed_results:
      done: [approved, changes_requested]
      blocked: [blocked]
      failed: [error, timeout, missing_report, invalid_report, process_error]
    on:
      done/approved: ready_for_human
      done/changes_requested: code
      blocked/blocked: blocked_for_human
      failed/error: blocked_for_human
      failed/timeout: blocked_for_human
      failed/missing_report: blocked_for_human
      failed/invalid_report: blocked_for_human
      failed/process_error: blocked_for_human
`)
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
printf 'bd must not be called during follow-up recording\n' >&2
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

type cliProcessResult struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func startCLIProcess(t *testing.T, root string, env []string, args ...string) *cliProcessResult {
	t.Helper()
	result := &cliProcessResult{cmd: exec.Command(os.Args[0], args...)}
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
	return r.cmd.Wait()
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
