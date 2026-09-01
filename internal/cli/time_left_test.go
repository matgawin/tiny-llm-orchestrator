package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	timeLeftTestAgentCoder = "coder"
	timeLeftTestAttemptID  = "attempt-001"
	timeLeftTestFlag       = "--attempt"
	timeLeftTestJSONFlag   = "--json"
	timeLeftTestRunFlag    = "--run"
	timeLeftTestStepCode   = "code"
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
		"elapsed:",
		"remaining:",
		"timeout: 30m0s",
		"phase: REPORT_NOW",
		"action: submit orc report now or report blocked/blocked now if blocked",
	})
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
		t.Fatalf("identity = %+v, want run/code/coder/attempt-001", payload)
	}

	if payload.StartedAt != "2026-05-04T12:00:00Z" || payload.Deadline != "2026-05-04T12:30:00Z" || payload.Timeout != "30m0s" {
		t.Fatalf("timing = %+v, want persisted started_at + timeout", payload)
	}

	if payload.Phase != "REPORT_NOW" || !strings.Contains(payload.Action, "orc report") {
		t.Fatalf("phase/action = %s/%s, want REPORT_NOW report action", payload.Phase, payload.Action)
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

	if got := err.Error(); !strings.Contains(got, `ORC_STEP_ID "review" does not match attempt step "code"`) {
		t.Fatalf("error = %q, want step mismatch", got)
	}
}
