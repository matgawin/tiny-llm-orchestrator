package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/launcher"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestRunAdvanceCommandWorkflowReachesReadyForHuman(t *testing.T) {
	root := withTempCwd(t)
	writeCLIAdvanceCommandProject(t, root, "", "")
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	output := executeCLICommand(t, []string{"run", "advance", result.runID})
	assertCLIOutputContainsAll(t, output, []string{
		"launched attempts: 4",
		"final status: ready_for_human",
		"final decision: terminal",
		"stop reason: ready_for_human",
		"exit code: 0",
	})
	loaded := loadCLIRun(t, root, result.runID)
	if loaded.Status.State != cliStateReadyForHuman {
		t.Fatalf("run state = %q, want ready_for_human", loaded.Status.State)
	}
	if got := len(loaded.Status.Attempts); got != 4 {
		t.Fatalf("attempt count = %d, want 4", got)
	}
}

func TestRunAdvanceJSONRoutesLiveProgressToStderr(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	shim := installCLICodexShim(t, root)
	t.Setenv("PATH", shim.binDir)
	t.Setenv("ORC_CLI_CODEX_SHIM", "1")
	t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")
	t.Setenv("ORC_CLI_CODEX_PROGRESS", "analyzing code paths")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "advance", result.runID, "--once", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}
	var payload struct {
		RunID            string                    `json:"run_id"`
		LaunchedAttempts []launcher.AdvanceAttempt `json:"launched_attempts"`
		StopReason       string                    `json:"stop_reason"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout = %q, want one final JSON object: %v", stdout.String(), err)
	}
	loaded := loadCLIRun(t, root, result.runID)
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	progressLine := fmt.Sprintf("[%s %s] analyzing code paths", attempt.StepID, attempt.AttemptID)
	if strings.Contains(stdout.String(), "analyzing code paths") {
		t.Fatalf("stdout = %q, want no live progress in JSON output", stdout.String())
	}
	if !strings.Contains(stderr.String(), progressLine) {
		t.Fatalf("stderr = %q, want progress line %q", stderr.String(), progressLine)
	}
	if payload.RunID != result.runID || len(payload.LaunchedAttempts) != 1 || payload.StopReason != "once" {
		t.Fatalf("json payload = %+v, want once result", payload)
	}
}

func TestRunAdvanceCommandStepDisplaysLiveProgressWithoutDedicatedPersistence(t *testing.T) {
	root := withTempCwd(t)
	const message = "deterministic progress update"
	installCLIOrcExecutable(t, root)
	writeCLIAdvanceCommandProject(t, root, "", "")
	configPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	config := string(readCLIFile(t, configPath))
	config = strings.Replace(config, `argv: ["sh", "-c", "exit 0"]`, `argv: ["sh", "-c", "printf %s \"$ORC_PROGRESS_SOCKET\" > progress-socket.txt; orc progress \"$LIVE_MSG\"; exit 0"]
    env:
      LIVE_MSG: "`+message+`"`, 1)
	writeCLIFile(t, configPath, config)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	output := executeCLICommand(t, []string{"run", "advance", result.runID, "--once"})
	loaded := loadCLIRun(t, root, result.runID)
	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	progressLine := fmt.Sprintf("[%s %s] %s", attempt.StepID, attempt.AttemptID, message)
	if !strings.Contains(output, progressLine) {
		t.Fatalf("output = %q, want progress line %q", output, progressLine)
	}
	socketPath := strings.TrimSpace(string(readCLIFile(t, filepath.Join(root, "progress-socket.txt"))))
	if socketPath == "" {
		t.Fatal("progress socket path file is empty")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("progress socket stat error = %v, want listener cleanup to remove socket", err)
	}
	statusContent := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "status.json")))
	eventsContent := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "events.jsonl")))
	var summaryStdout, summaryStderr bytes.Buffer
	if err := Execute([]string{"run", "summary-context", result.runID}, &summaryStdout, &summaryStderr); err != nil {
		t.Fatalf("summary-context returned error: %v\nstderr: %s", err, summaryStderr.String())
	}
	for name, content := range map[string]string{
		"status.json":     statusContent,
		"events.jsonl":    eventsContent,
		"summary-context": summaryStdout.String(),
	} {
		if strings.Contains(content, message) {
			t.Fatalf("%s contains live progress message %q", name, message)
		}
	}
}

func TestRunAdvanceContinuesAfterReviewChangesRequestedRoute(t *testing.T) {
	root := withTempCwd(t)
	reviewScript := filepath.Join(root, "review-once.sh")
	writeCLIFile(t, reviewScript, "#!/bin/sh\nif [ ! -f review-count ]; then touch review-count; exit 1; fi\nexit 0\n")
	if err := os.Chmod(reviewScript, 0o755); err != nil {
		t.Fatalf("chmod review script: %v", err)
	}
	writeCLIAdvanceCommandProject(t, root, `    kind: script
    script:
      path: review-once.sh
`, "")
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	output := executeCLICommand(t, []string{"run", "advance", result.runID, "--max-steps", "8"})
	assertCLIOutputContainsAll(t, output, []string{
		"launched attempts: 7",
		"final status: ready_for_human",
		"stop reason: ready_for_human",
	})
	loaded := loadCLIRun(t, root, result.runID)
	if got := len(loaded.Status.Attempts); got != 7 {
		t.Fatalf("attempt count = %d, want review changes loop to produce 7 attempts", got)
	}
}

func TestRunAdvanceStopsOnWorkerBlockedAndFailed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		result     string
		wantReason string
		wantCode   int
	}{
		{name: "blocked", status: "blocked", result: "blocked", wantReason: "worker_blocked", wantCode: 2},
		{name: "failed", status: "failed", result: "error", wantReason: "worker_failed", wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIImplementationProject(t, root)
			run := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			shim := installCLICodexShim(t, root)
			t.Setenv("PATH", shim.binDir)
			t.Setenv("ORC_CLI_CODEX_SHIM", "1")
			t.Setenv("ORC_CLI_CODEX_MODE", "worker-report")
			t.Setenv("ORC_CLI_CODEX_REPORT_STATUS", tc.status)
			t.Setenv("ORC_CLI_CODEX_REPORT_RESULT", tc.result)

			var stdout, stderr bytes.Buffer
			err := Execute([]string{"run", "advance", run.runID}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want stop error")
			}
			if got := ExitCode(err); got != tc.wantCode {
				t.Fatalf("ExitCode = %d, want %d", got, tc.wantCode)
			}
			assertCLIOutputContainsAll(t, stdout.String(), []string{"stop reason: " + tc.wantReason, fmt.Sprintf("exit code: %d", tc.wantCode)})
		})
	}
}

func TestRunAdvanceStopsOnLoopCapsAndMaxSteps(t *testing.T) {
	for _, tc := range []struct {
		name       string
		caps       string
		maxSteps   string
		wantReason string
		wantCode   int
	}{
		{name: "soft cap", caps: "enabled: true\nsoft: 1\nhard: 4\n", maxSteps: "5", wantReason: "loop_soft_cap", wantCode: 2},
		{name: "hard cap", caps: "enabled: true\nsoft: 1\nhard: 2\n", maxSteps: "5", wantReason: "loop_hard_cap", wantCode: 2},
		{name: "max steps", caps: "enabled: false\nsoft: 1\nhard: 2\n", maxSteps: "1", wantReason: "max_steps_reached", wantCode: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIAdvanceLoopProject(t, root, tc.caps)
			run := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			if tc.wantReason == "loop_hard_cap" {
				executeCLICommand(t, []string{"worker", "launch-next", run.runID})
				executeCLICommand(t, []string{"worker", "launch-next", run.runID})
				executeCLICommand(t, []string{"worker", "launch-next", run.runID})
			}

			var stdout, stderr bytes.Buffer
			err := Execute([]string{"run", "advance", run.runID, "--max-steps", tc.maxSteps}, &stdout, &stderr)
			if tc.wantCode == 0 && err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if tc.wantCode != 0 {
				if err == nil {
					t.Fatal("Execute returned nil error, want attention stop")
				}
				if got := ExitCode(err); got != tc.wantCode {
					t.Fatalf("ExitCode = %d, want %d", got, tc.wantCode)
				}
			}
			assertCLIOutputContainsAll(t, stdout.String(), []string{"stop reason: " + tc.wantReason, fmt.Sprintf("exit code: %d", tc.wantCode)})
		})
	}
}

func TestRunAdvanceOnceJSONActiveAttemptAndInvalidMaxSteps(t *testing.T) {
	root := withTempCwd(t)
	writeCLIAdvanceCommandProject(t, root, "", "")
	run := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "advance", run.runID, "--once", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}
	var payload struct {
		RunID            string                    `json:"run_id"`
		LaunchedAttempts []launcher.AdvanceAttempt `json:"launched_attempts"`
		FinalStatus      string                    `json:"final_status"`
		FinalDecision    string                    `json:"final_decision"`
		StopReason       string                    `json:"stop_reason"`
		ExitCode         int                       `json:"exit_code"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json output = %q, decode error: %v", stdout.String(), err)
	}
	if payload.RunID != run.runID || len(payload.LaunchedAttempts) != 1 || payload.StopReason != "once" || payload.ExitCode != 0 || payload.FinalDecision != "select_step" {
		t.Fatalf("json payload = %+v, want once result after one launch", payload)
	}

	activeRoot := withTempCwd(t)
	writeCLIProject(t, activeRoot, "optional", true)
	activeRun := executeCLIRunStart(t, activeRoot, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, activeRoot, activeRun.runID, "attempt-001")
	stdout.Reset()
	stderr.Reset()
	err := Execute([]string{"run", "advance", activeRun.runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want active attempt refusal")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode = %d, want 1", got)
	}
	assertCLIOutputContainsAll(t, stdout.String(), []string{"stop reason: active_attempt_exists", "exit code: 1"})

	stdout.Reset()
	stderr.Reset()
	err = Execute([]string{"run", "advance", activeRun.runID, "--max-steps", "0"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want invalid max steps")
	}
	assertCLIOutputContainsAll(t, stderr.String(), []string{"--max-steps must be a positive integer", "run advance"})
}

func TestRunAdvanceStopsOnInvalidRunState(t *testing.T) {
	root := withTempCwd(t)
	writeCLIAdvanceCommandProject(t, root, "", "")
	run := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	if _, _, err := openCLIStore(t, root).UpdateStatus(run.runID, runstore.StatusUpdate{State: "unknown_run_state"}); err != nil {
		t.Fatalf("UpdateStatus returned error: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := Execute([]string{"run", "advance", run.runID}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want invalid state error")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode = %d, want 1", got)
	}
	assertCLIOutputContainsAll(t, stdout.String(), []string{"stop reason: error", "exit code: 1"})
	assertCLIOutputContainsAll(t, stderr.String(), []string{`unsupported run status "unknown_run_state"`})
}
