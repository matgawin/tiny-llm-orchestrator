package attemptdeadline

import (
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

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
			if got := Phase(tt.remaining); got != tt.want {
				t.Fatalf("Phase(%s) = %s, want %s", tt.remaining, got, tt.want)
			}
		})
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
		Timeout:   "30m0s",
	}, now)
	if err != nil {
		t.Fatalf("FromAttempt returned error: %v", err)
	}

	if got.Deadline != startedAt.Add(30*time.Minute) {
		t.Fatalf("deadline = %s, want started_at + timeout", got.Deadline)
	}

	if got.Elapsed != 21*time.Minute || got.Remaining != 9*time.Minute {
		t.Fatalf("elapsed/remaining = %s/%s, want 21m0s/9m0s", got.Elapsed, got.Remaining)
	}

	if got.Phase != PhaseNarrow || got.Action != Action(PhaseNarrow) {
		t.Fatalf("phase/action = %s/%s, want NARROW action", got.Phase, got.Action)
	}
}
