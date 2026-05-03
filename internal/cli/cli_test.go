package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"Usage:", "Available Commands:", "init", "run", "version"} {
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
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	writeCLIFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n")
	writeCLIFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), cliWorkflowTaskContext(beads, markdownFallback))
	writeCLIFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Test planner.\n---\n\nPlan.\n")
}

func cliWorkflowTaskContext(beads string, markdownFallback bool) string {
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
