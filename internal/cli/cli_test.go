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

	"tiny-llm-orchestrator/orc/internal/testutil"
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

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
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
