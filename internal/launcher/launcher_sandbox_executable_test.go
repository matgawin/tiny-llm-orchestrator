package launcher

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestLaunchNextPreservesExplicitCommandInsideVerifiedSandbox(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	appendLauncherSandboxConfig(t, root, false)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"sh", "-c", "cat >/dev/null; printf explicit-worker-command"},
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateMissingReport || result.Attempt.LogRef == nil {
		t.Fatalf("attempt = %+v, want missing_report with log", result.Attempt)
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	if !strings.Contains(string(logContent), "explicit-worker-command") {
		t.Fatalf("log = %q, want explicit worker command output", string(logContent))
	}
}

func TestLaunchNextCommandStepIgnoresSandboxWorkerDefault(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"sh", "-c", "printf deterministic-command"},
	})
	appendLauncherSandboxConfig(t, root, false)
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if result.Attempt.State != runstore.AttemptStateReported || result.Attempt.Result != resultCommandPassed {
		t.Fatalf("attempt = %+v, want command step done/passed", result.Attempt)
	}
	loaded := loadLauncherRun(t, root, runID)
	assertLogArtifactContains(t, root, loaded, result.Attempt.AttemptID, "stdout", "deterministic-command")
}

func TestLaunchNextRejectsScriptSymlinkEscape(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Kind:       config.StepKindScript,
		ScriptPath: "scripts/escape.sh",
	})
	outsideDir := t.TempDir()
	outsideScript := filepath.Join(outsideDir, "escape.sh")
	writeLauncherExecutable(t, outsideScript, "#!/bin/sh\ntrue\n")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o750); err != nil {
		t.Fatalf("create scripts dir: %v", err)
	}
	if err := os.Symlink(outsideScript, filepath.Join(root, "scripts", "escape.sh")); err != nil {
		t.Fatalf("create escaping script symlink: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err == nil || !strings.Contains(err.Error(), `path "scripts/escape.sh" escapes repository root`) {
		t.Fatalf("LaunchNext error = %v, want script escape error", err)
	}
	if result.Attempt.State != runstore.AttemptStateReported ||
		result.Attempt.Status != reportStatusFailed ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStateStartFailed {
		t.Fatalf("attempt = %+v, want reported failed/process_error start_failed", result.Attempt)
	}
}

func TestLaunchNextResolvesCommandFromWorkerEnvPATHRelativeToProjectRoot(t *testing.T) {
	root, runID := createLauncherRun(t, "200ms")
	binDir := filepath.Join(root, "worker-bin")
	if err := os.Mkdir(binDir, 0o750); err != nil {
		t.Fatalf("mkdir worker bin: %v", err)
	}
	workerPath := filepath.Join(binDir, "env-worker")
	writeLauncherFile(t, workerPath, "#!/bin/sh\ncat >/dev/null\nprintf 'env-path-worker\\n'\n")
	if err := os.Chmod(workerPath, 0o750); err != nil {
		t.Fatalf("chmod worker: %v", err)
	}

	result, err := LaunchNext(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		Command: []string{"env-worker"},
		Env:     append(envWithoutPath(os.Environ()), "PATH=worker-bin"),
		Time:    fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}
	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}
	logContent := readLauncherArtifact(t, root, runID, *result.Attempt.LogRef)
	if !strings.Contains(string(logContent), "env-path-worker") {
		t.Fatalf("log = %q, want worker from Options.Env PATH", string(logContent))
	}
}

func TestResolveWorkerExecutableDoesNotFallbackWhenEnvOmitsPATH(t *testing.T) {
	_, err := resolveWorkerExecutable("sh", []string{"HOME=/tmp"}, t.TempDir())
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("resolveWorkerExecutable error = %v, want exec.ErrNotFound", err)
	}
}

func TestNewWorkerCommandUsesAbsoluteHelperPath(t *testing.T) {
	cmd, releaseExec, err := newWorkerCommand([]string{"sh", "-c", "true"}, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatalf("newWorkerCommand returned error: %v", err)
	}
	defer func() {
		_ = releaseExec(false)
	}()
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("helper path = %q, want absolute path", cmd.Path)
	}
}

func TestExecHelperClosesHandshakeFDBeforeWorkerExec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd inheritance assertion uses linux procfs")
	}
	cmd, releaseExec, err := newWorkerCommand([]string{"sh", "-c", "test ! -e /proc/$$/fd/3"}, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatalf("newWorkerCommand returned error: %v", err)
	}
	defer func() {
		_ = releaseExec(false)
	}()
	cmd.Env = append(filteredExecEnv(os.Environ()), cmd.Env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start returned error: %v", err)
	}
	if releaseErr := releaseExec(true); releaseErr != nil {
		t.Fatalf("releaseExec returned error: %v", releaseErr)
	}
	err = cmd.Wait()
	if err != nil {
		t.Fatalf("worker found inherited fd 3: %v\n%s", err, output.String())
	}
}

func TestAmbientExecHelperEnvDoesNotBypassNormalInvocation(t *testing.T) {
	if os.Getenv("ORC_LAUNCHER_AMBIENT_HELPER_TEST") == "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAmbientExecHelperEnvDoesNotBypassNormalInvocation")
	cmd.Env = append(os.Environ(),
		execHelperEnv+"=ambient-user-value",
		"ORC_LAUNCHER_AMBIENT_HELPER_TEST=1",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test binary with ambient helper env returned error: %v\n%s", err, output)
	}
}
