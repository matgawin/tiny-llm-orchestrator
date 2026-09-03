package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/attemptdeadline"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	timeLeftTestAgentCoder = "planner"
	timeLeftTestAttemptID  = "attempt-001"
	timeLeftTestFlag       = "--attempt"
	timeLeftTestJSONFlag   = "--json"
	timeLeftTestHookFlag   = "--codex-hook"
	timeLeftTestMissing    = "missing"
	timeLeftTestRunFlag    = "--run"
	timeLeftTestStepCode   = "plan"
	timeLeftTestTask       = "# Task"
	timeLeftTestTaskFlag   = "--task"
)

func TestExecuteTimeLeftUsesWorkerEnvironment(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
	startCLIActiveAttemptForStep(t, root, result.runID, timeLeftTestAttemptID, timeLeftTestStepCode, timeLeftTestAgentCoder)

	t.Setenv("ORC_RUN_ID", result.runID)
	t.Setenv("ORC_STEP_ID", timeLeftTestStepCode)
	t.Setenv("ORC_ATTEMPT_ID", timeLeftTestAttemptID)

	output := executeCLICommand(t, []string{commandTimeLeft})
	assertCLIOutputContainsAll(t, output, []string{
		"run: " + result.runID,
		"step: " + timeLeftTestStepCode,
		"agent: " + timeLeftTestAgentCoder,
		"attempt: " + timeLeftTestAttemptID,
		"started_at: 2026-05-04T12:00:00Z",
		"deadline: 2026-05-04T12:30:00Z",
		"calculated_at:",
		"elapsed:",
		"remaining:",
		"timeout: 30m0s",
		"phase: REPORT_NOW",
		"action: submit orc report now or report blocked/blocked now if blocked",
	})
}

func TestExecuteTimeLeftRejectsJSONWithCodexHookBeforeLookup(t *testing.T) {
	t.Setenv("ORC_RUN_ID", "")
	t.Setenv("ORC_ATTEMPT_ID", "")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandTimeLeft, timeLeftTestJSONFlag, timeLeftTestHookFlag}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want flag conflict")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if !strings.Contains(err.Error(), "json") || !strings.Contains(err.Error(), "codex-hook") {
		t.Fatalf("error = %q, want both conflicting flags", err)
	}
}

func TestExecuteTimeLeftCodexHookPhases(t *testing.T) {
	tests := []struct {
		phase     string
		remaining time.Duration
	}{
		{phase: attemptdeadline.PhaseNormal, remaining: 20 * time.Minute},
		{phase: attemptdeadline.PhaseNarrow, remaining: 7 * time.Minute},
		{phase: attemptdeadline.PhaseWrapUp, remaining: 3 * time.Minute},
		{phase: attemptdeadline.PhaseReportNow, remaining: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			descriptorPath := filepath.Join(root, ".orc", "agents", "planner.md")
			descriptor := string(readCLIFile(t, descriptorPath))
			writeCLIFile(t, descriptorPath, strings.Replace(descriptor, "role: planner", "role: coder", 1))
			result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
			attemptID := "attempt-hook-" + strings.ToLower(tt.phase)
			startCLIActiveAttemptWithRemaining(t, root, result.runID, attemptID, tt.remaining)

			output := executeCLICommand(t, []string{commandTimeLeft, timeLeftTestRunFlag, result.runID, timeLeftTestFlag, attemptID, timeLeftTestHookFlag})
			if tt.phase == attemptdeadline.PhaseNormal {
				if output != "" {
					t.Fatalf("output = %q, want empty", output)
				}

				return
			}

			var payload timeLeftCodexHook
			if err := json.Unmarshal([]byte(output), &payload); err != nil {
				t.Fatalf("unmarshal Codex hook JSON: %v\n%s", err, output)
			}

			wantAction := attemptdeadline.Action(tt.phase, attemptdeadline.ActionGroupImplementation)

			wantContext := "Orc deadline phase: " + tt.phase + ". Time remaining: "
			if payload.HookSpecificOutput.HookEventName != "PostToolUse" || !strings.HasPrefix(payload.HookSpecificOutput.AdditionalContext, wantContext) || !strings.HasSuffix(payload.HookSpecificOutput.AdditionalContext, ". "+wantAction+".") {
				t.Fatalf("payload = %+v, want complete %s hook response with implementation action", payload, tt.phase)
			}

			if !strings.HasSuffix(output, "}\n") {
				t.Fatalf("output = %q, want one trailing newline", output)
			}
		})
	}
}

func TestExecuteTimeLeftCodexHookLookupErrorWritesNoOutput(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandTimeLeft, "--root", root, timeLeftTestRunFlag, timeLeftTestMissing, timeLeftTestFlag, timeLeftTestMissing, timeLeftTestHookFlag}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want lookup error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestWriteTimeLeftCodexHook(t *testing.T) {
	guidance := attemptdeadline.Guidance{
		Phase:     attemptdeadline.PhaseWrapUp,
		Remaining: 2*time.Minute + 3*time.Second,
		Action:    `finish "quoted" work`,
	}

	var output bytes.Buffer
	if err := writeTimeLeftCodexHook(&output, guidance); err != nil {
		t.Fatalf("writeTimeLeftCodexHook returned error: %v", err)
	}

	want := "{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"additionalContext\":\"Orc deadline phase: WRAP_UP. Time remaining: 2m3s. finish \\\"quoted\\\" work.\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWriteTimeLeftCodexHookRejectsInvalidGuidanceBeforeOutput(t *testing.T) {
	tests := []attemptdeadline.Guidance{
		{Phase: "UNKNOWN", Action: "act"},
		{Phase: attemptdeadline.PhaseNormal},
	}
	for _, guidance := range tests {
		var output bytes.Buffer
		if err := writeTimeLeftCodexHook(&output, guidance); err == nil {
			t.Fatalf("guidance = %+v returned nil error", guidance)
		}

		if output.Len() != 0 {
			t.Fatalf("guidance = %+v wrote %q, want empty", guidance, output.String())
		}
	}
}

func TestWriteTimeLeftCodexHookReturnsWriterError(t *testing.T) {
	reader, writer := io.Pipe()
	if err := reader.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	err := writeTimeLeftCodexHook(writer, attemptdeadline.Guidance{Phase: attemptdeadline.PhaseNarrow, Remaining: time.Minute, Action: "act"})
	if err == nil || !errors.Is(err, io.ErrClosedPipe) || !strings.Contains(err.Error(), "write time-left Codex hook output") {
		t.Fatalf("error = %v, want output writer error", err)
	}
}

func TestExecuteTimeLeftExplicitLookupJSON(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
	startCLIActiveAttemptForStep(t, root, result.runID, timeLeftTestAttemptID, timeLeftTestStepCode, timeLeftTestAgentCoder)

	output := executeCLICommand(t, []string{
		commandTimeLeft,
		timeLeftTestRunFlag,
		result.runID,
		timeLeftTestFlag,
		timeLeftTestAttemptID,
		timeLeftTestJSONFlag,
	})

	var payload timeLeftJSON
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal time-left JSON: %v\n%s", err, output)
	}

	if payload.RunID != result.runID || payload.StepID != timeLeftTestStepCode || payload.AgentID != timeLeftTestAgentCoder || payload.AttemptID != timeLeftTestAttemptID {
		t.Fatalf("identity = %+v, want run/plan/planner/attempt-001", payload)
	}

	if payload.StartedAt != "2026-05-04T12:00:00Z" || payload.Deadline != "2026-05-04T12:30:00Z" || payload.Timeout != "30m0s" {
		t.Fatalf("timing = %+v, want persisted started_at + timeout", payload)
	}

	if _, err := time.Parse(time.RFC3339Nano, payload.CalculatedAt); err != nil {
		t.Fatalf("calculated_at = %q, want RFC3339Nano timestamp: %v", payload.CalculatedAt, err)
	}

	if payload.Phase != "REPORT_NOW" || !strings.Contains(payload.Action, "orc report") {
		t.Fatalf("phase/action = %s/%s, want REPORT_NOW report action", payload.Phase, payload.Action)
	}
}

func TestExecuteTimeLeftRootPrecedenceUsesFlagBeforeEnv(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
	startCLIActiveAttemptForStep(t, root, result.runID, timeLeftTestAttemptID, timeLeftTestStepCode, timeLeftTestAgentCoder)

	wrongRoot := t.TempDir()
	t.Setenv("ORC_PROJECT_ROOT", wrongRoot)

	output := executeCLICommand(t, []string{
		commandTimeLeft,
		"--root",
		root,
		timeLeftTestRunFlag,
		result.runID,
		timeLeftTestFlag,
		timeLeftTestAttemptID,
	})

	if !strings.Contains(output, "run: "+result.runID) {
		t.Fatalf("output = %q, want run loaded from --root", output)
	}
}

func TestExecuteTimeLeftUsesProjectRootEnvFromDifferentCwd(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
	startCLIActiveAttemptForStep(t, root, result.runID, timeLeftTestAttemptID, timeLeftTestStepCode, timeLeftTestAgentCoder)

	otherDir := filepath.Join(root, "subdir")
	if err := os.Mkdir(otherDir, 0o750); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir subdir: %v", err)
	}

	t.Setenv("ORC_PROJECT_ROOT", root)
	t.Setenv("ORC_RUN_ID", result.runID)
	t.Setenv("ORC_STEP_ID", "")
	t.Setenv("ORC_ATTEMPT_ID", timeLeftTestAttemptID)

	output := executeCLICommand(t, []string{commandTimeLeft})
	if !strings.Contains(output, "run: "+result.runID) {
		t.Fatalf("output = %q, want run loaded from ORC_PROJECT_ROOT", output)
	}
}

func TestExecuteTimeLeftJSONPhaseActions(t *testing.T) {
	tests := []struct {
		name      string
		attemptID string
		remaining time.Duration
		wantPhase string
	}{
		{name: "normal", attemptID: "attempt-normal", remaining: 11 * time.Minute, wantPhase: attemptdeadline.PhaseNormal},
		{name: "narrow", attemptID: "attempt-narrow", remaining: 7 * time.Minute, wantPhase: attemptdeadline.PhaseNarrow},
		{name: "wrap up", attemptID: "attempt-wrap-up", remaining: 3 * time.Minute, wantPhase: attemptdeadline.PhaseWrapUp},
		{name: "report now", attemptID: "attempt-report-now", remaining: time.Minute, wantPhase: attemptdeadline.PhaseReportNow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
			startCLIActiveAttemptWithRemaining(t, root, result.runID, tt.attemptID, tt.remaining)

			output := executeCLICommand(t, []string{
				commandTimeLeft,
				timeLeftTestRunFlag,
				result.runID,
				timeLeftTestFlag,
				tt.attemptID,
				timeLeftTestJSONFlag,
			})

			var payload timeLeftJSON
			if err := json.Unmarshal([]byte(output), &payload); err != nil {
				t.Fatalf("unmarshal time-left JSON: %v\n%s", err, output)
			}

			if payload.Phase != tt.wantPhase || payload.Action != attemptdeadline.Action(tt.wantPhase, attemptdeadline.ActionGroupPlanning) {
				t.Fatalf("phase/action = %s/%s, want %s/%s", payload.Phase, payload.Action, tt.wantPhase, attemptdeadline.Action(tt.wantPhase, attemptdeadline.ActionGroupPlanning))
			}

			if tt.wantPhase == attemptdeadline.PhaseNormal {
				human := executeCLICommand(t, []string{commandTimeLeft, timeLeftTestRunFlag, result.runID, timeLeftTestFlag, tt.attemptID})
				assertCLIOutputContainsAll(t, human, []string{
					"calculated_at: ",
					"phase: NORMAL",
					"action: produce the smallest complete scope envelope",
				})
			}
		})
	}
}

func TestExecuteTimeLeftRoleActionsMatchHumanAndJSONOutput(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{role: "coder", want: "continue the scoped implementation"},
		{role: "reviewer", want: "review only the assigned review category"},
		{role: "tester", want: "run the scoped verification"},
		{role: "custom-role", want: "continue scoped work"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			descriptorPath := filepath.Join(root, ".orc", "agents", "planner.md")
			descriptor := string(readCLIFile(t, descriptorPath))
			writeCLIFile(t, descriptorPath, strings.Replace(descriptor, "role: planner", "role: "+tt.role, 1))
			result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
			attemptID := "attempt-" + tt.role
			startCLIActiveAttemptWithRemaining(t, root, result.runID, attemptID, 20*time.Minute)

			human := executeCLICommand(t, []string{commandTimeLeft, timeLeftTestRunFlag, result.runID, timeLeftTestFlag, attemptID})
			assertCLIOutputContainsAll(t, human, []string{"phase: NORMAL", "action: " + tt.want})

			output := executeCLICommand(t, []string{commandTimeLeft, timeLeftTestRunFlag, result.runID, timeLeftTestFlag, attemptID, timeLeftTestJSONFlag})

			var payload timeLeftJSON
			if err := json.Unmarshal([]byte(output), &payload); err != nil {
				t.Fatalf("unmarshal time-left JSON: %v\n%s", err, output)
			}

			if payload.Phase != attemptdeadline.PhaseNormal || payload.Action != tt.want {
				t.Fatalf("phase/action = %s/%s, want NORMAL/%s", payload.Phase, payload.Action, tt.want)
			}
		})
	}
}

func TestExecuteTimeLeftRequiresWorkerEnvOrExplicitIDs(t *testing.T) {
	t.Setenv("ORC_RUN_ID", "")
	t.Setenv("ORC_ATTEMPT_ID", "")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandTimeLeft}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want missing identity error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "ORC_RUN_ID and ORC_ATTEMPT_ID") || !strings.Contains(got, "--run and --attempt") {
		t.Fatalf("stderr = %q, want actionable identity guidance", got)
	}

	if got := err.Error(); !strings.Contains(got, "ORC_RUN_ID and ORC_ATTEMPT_ID") || !strings.Contains(got, "--run and --attempt") {
		t.Fatalf("error = %q, want actionable identity guidance", got)
	}
}

func TestExecuteTimeLeftValidatesEnvStepID(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{timeLeftTestTaskFlag, timeLeftTestTask}, nil)
	startCLIActiveAttemptForStep(t, root, result.runID, timeLeftTestAttemptID, timeLeftTestStepCode, timeLeftTestAgentCoder)

	t.Setenv("ORC_RUN_ID", result.runID)
	t.Setenv("ORC_STEP_ID", "review")
	t.Setenv("ORC_ATTEMPT_ID", timeLeftTestAttemptID)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{commandTimeLeft}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want step mismatch")
	}

	if got := err.Error(); !strings.Contains(got, `ORC_STEP_ID "review" does not match attempt step "plan"`) {
		t.Fatalf("error = %q, want step mismatch", got)
	}
}

func startCLIActiveAttemptWithRemaining(t *testing.T, root, runID, attemptID string, remaining time.Duration) {
	t.Helper()

	timeout := 30 * time.Minute
	startedAt := time.Now().UTC().Add(remaining - timeout)

	store := openCLIStore(t, root)
	if _, _, err := store.StartAttemptContext(context.Background(), runID, runstore.StartAttemptRequest{
		StepID:          timeLeftTestStepCode,
		AgentID:         timeLeftTestAgentCoder,
		AttemptID:       attemptID,
		Timeout:         timeout,
		ReportExitGrace: 30 * time.Second,
		Time:            startedAt,
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
}
