package attemptdeadline

import (
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

const testAttemptTimeout = "30m0s"

func TestPhaseThresholds(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		want      string
	}{
		{name: "normal", remaining: 10*time.Minute + time.Nanosecond, want: PhaseNormal},
		{name: "narrow", remaining: 10 * time.Minute, want: PhaseNarrow},
		{name: "wrap up", remaining: 5 * time.Minute, want: PhaseWrapUp},
		{name: "report now", remaining: 2 * time.Minute, want: PhaseReportNow},
		{name: "expired", remaining: -time.Second, want: PhaseReportNow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Phase(tt.remaining, 30*time.Minute); got != tt.want {
				t.Fatalf("Phase(%s) = %s, want %s", tt.remaining, got, tt.want)
			}
		})
	}
}

func TestPhaseUsesProportionalCappedThresholds(t *testing.T) {
	tests := []struct {
		name, want         string
		timeout, remaining time.Duration
	}{
		{name: "short before narrow", timeout: 9 * time.Minute, remaining: 3*time.Minute + time.Nanosecond, want: PhaseNormal},
		{name: "short narrow inclusive", timeout: 9 * time.Minute, remaining: 3 * time.Minute, want: PhaseNarrow},
		{name: "short wrap inclusive", timeout: 9 * time.Minute, remaining: 90 * time.Second, want: PhaseWrapUp},
		{name: "short report inclusive", timeout: 9 * time.Minute, remaining: 36 * time.Second, want: PhaseReportNow},
		{name: "long capped narrow", timeout: 90 * time.Minute, remaining: 10 * time.Minute, want: PhaseNarrow},
		{name: "long capped wrap up", timeout: 90 * time.Minute, remaining: 5 * time.Minute, want: PhaseWrapUp},
		{name: "long capped report now", timeout: 90 * time.Minute, remaining: 2 * time.Minute, want: PhaseReportNow},
		{name: "very small thresholds are zero", timeout: time.Nanosecond, remaining: time.Nanosecond, want: PhaseNormal},
		{name: "expired", timeout: time.Nanosecond, remaining: -time.Nanosecond, want: PhaseReportNow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Phase(tt.remaining, tt.timeout); got != tt.want {
				t.Fatalf("Phase(%s, %s) = %s, want %s", tt.remaining, tt.timeout, got, tt.want)
			}
		})
	}
}

func TestActionsByGroup(t *testing.T) {
	tests := []struct {
		group                  ActionGroup
		normal, narrow, wrapUp string
	}{
		{ActionGroupPlanning, "produce the smallest complete scope envelope", "stop discovery and finalize the scope envelope", "stop adding plan detail and prepare the report"},
		{ActionGroupImplementation, "continue the scoped implementation", "finish the current change and add no new behavior", "stop editing, run at most one cheap check, and prepare the report"},
		{ActionGroupReview, "review only the assigned review category", "stop broad searches and verify current findings", "stop adding findings and prepare the report"},
		{ActionGroupVerification, "run the scoped verification", "stop adding test surfaces and finish the current check", "run no new checks and prepare the report"},
		{ActionGroupGeneric, "continue scoped work", "stop expanding scope", "stop new work and prepare the report"},
	}
	for _, tt := range tests {
		for phase, want := range map[string]string{PhaseNormal: tt.normal, PhaseNarrow: tt.narrow, PhaseWrapUp: tt.wrapUp, PhaseReportNow: "submit orc report now or report blocked/blocked now if blocked"} {
			if got := Action(phase, tt.group); got != want {
				t.Errorf("Action(%s, %s) = %q, want %q", phase, tt.group, got, want)
			}
		}
	}
}

func TestGroupForEveryScaffoldRole(t *testing.T) {
	tests := map[ActionGroup][]string{
		ActionGroupPlanning:       {"planner"},
		ActionGroupImplementation: {"coder", "mechanical-coder"},
		ActionGroupReview:         {"reviewer", "mechanical-reviewer", "readability-reviewer", "redundancy-reviewer", "docs-reviewer"},
		ActionGroupVerification:   {"tester", "test-designer", "bug-reproducer"},
		ActionGroupGeneric:        {"custom-role"},
	}
	for want, roles := range tests {
		for _, role := range roles {
			if got := GroupForRole(role); got != want {
				t.Errorf("GroupForRole(%q) = %q, want %q", role, got, want)
			}
		}
	}
}

func TestFromAttemptComputesDeadlineGuidance(t *testing.T) {
	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 123, time.UTC)
	now := startedAt.Add(21 * time.Minute)

	got, err := FromAttempt(runstore.Attempt{
		RunID:     "run-001",
		StepID:    "code",
		AgentID:   "coder",
		AttemptID: "attempt-001",
		StartedAt: startedAt,
		Timeout:   testAttemptTimeout,
	}, now, ActionGroupImplementation)
	if err != nil {
		t.Fatalf("FromAttempt returned error: %v", err)
	}

	if got.Deadline != startedAt.Add(30*time.Minute) {
		t.Fatalf("deadline = %s, want started_at + timeout", got.Deadline)
	}

	if got.Elapsed != 21*time.Minute || got.Remaining != 9*time.Minute {
		t.Fatalf("elapsed/remaining = %s/%s, want 21m0s/9m0s", got.Elapsed, got.Remaining)
	}

	if got.Phase != PhaseNarrow || got.Action != Action(PhaseNarrow, ActionGroupImplementation) {
		t.Fatalf("phase/action = %s/%s, want NARROW action", got.Phase, got.Action)
	}
}

func TestEnvFormatsDeadlineValues(t *testing.T) {
	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 123, time.UTC)

	got := Env(runstore.Attempt{
		StartedAt: startedAt,
		Timeout:   testAttemptTimeout,
	})

	want := map[string]string{
		"ORC_ATTEMPT_STARTED_AT": "2026-09-01T12:00:00.000000123Z",
		"ORC_ATTEMPT_DEADLINE":   "2026-09-01T12:30:00.000000123Z",
		"ORC_ATTEMPT_TIMEOUT":    testAttemptTimeout,
	}

	for key, value := range want {
		if got[key] != value {
			t.Fatalf("Env()[%s] = %q, want %q", key, got[key], value)
		}
	}
}
