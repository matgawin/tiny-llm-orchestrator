package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRunStartInlineTaskCreatesRun(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	if got := string(readCLIFile(t, cliTaskArtifactPath(root, result.runID, "context.md"))); got != "# Task\n" {
		t.Fatalf("task context = %q, want inline task with newline", got)
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

	result := executeCLIRunStart(t, root, []string{"--task-file=" + taskPath}, nil)
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

func TestExecuteRunStartRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"run", "start", "--bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	output := stderr.String()
	for _, want := range []string{`unknown flag: --bogus`, "Usage:", "run start"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
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
