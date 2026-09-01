// Package attemptdeadline computes worker attempt deadline guidance.
package attemptdeadline

import (
	"fmt"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	PhaseNormal    = "NORMAL"
	PhaseNarrow    = "NARROW"
	PhaseWrapUp    = "WRAP_UP"
	PhaseReportNow = "REPORT_NOW"

	reportNowThreshold = 2 * time.Minute
	wrapUpThreshold    = 5 * time.Minute
	narrowThreshold    = 10 * time.Minute
)

type Guidance struct {
	RunID      string
	StepID     string
	AgentID    string
	AttemptID  string
	StartedAt  time.Time
	Deadline   time.Time
	Elapsed    time.Duration
	Remaining  time.Duration
	Phase      string
	Action     string
	TimeoutRaw string
}

func FromAttempt(attempt runstore.Attempt, now time.Time) (Guidance, error) {
	timeout, err := time.ParseDuration(attempt.Timeout)
	if err != nil {
		return Guidance{}, fmt.Errorf("parse attempt timeout: %w", err)
	}

	if timeout <= 0 {
		return Guidance{}, fmt.Errorf("attempt timeout must be > 0")
	}

	startedAt := attempt.StartedAt.UTC()
	deadline := startedAt.Add(timeout)
	elapsed := now.UTC().Sub(startedAt)
	remaining := deadline.Sub(now.UTC())
	phase := Phase(remaining)

	return Guidance{
		RunID:      attempt.RunID,
		StepID:     attempt.StepID,
		AgentID:    attempt.AgentID,
		AttemptID:  attempt.AttemptID,
		StartedAt:  startedAt,
		Deadline:   deadline,
		Elapsed:    elapsed,
		Remaining:  remaining,
		Phase:      phase,
		Action:     Action(phase),
		TimeoutRaw: attempt.Timeout,
	}, nil
}

func Env(attempt runstore.Attempt) map[string]string {
	guidance, err := FromAttempt(attempt, attempt.StartedAt)
	if err != nil {
		return nil
	}

	return map[string]string{
		"ORC_ATTEMPT_STARTED_AT": FormatTime(guidance.StartedAt),
		"ORC_ATTEMPT_DEADLINE":   FormatTime(guidance.Deadline),
		"ORC_ATTEMPT_TIMEOUT":    guidance.TimeoutRaw,
	}
}

func FormatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func Phase(remaining time.Duration) string {
	switch {
	case remaining <= reportNowThreshold:
		return PhaseReportNow
	case remaining <= wrapUpThreshold:
		return PhaseWrapUp
	case remaining <= narrowThreshold:
		return PhaseNarrow
	default:
		return PhaseNormal
	}
}

func Action(phase string) string {
	switch phase {
	case PhaseNormal:
		return "continue scoped work"
	case PhaseNarrow:
		return "stop expanding scope"
	case PhaseWrapUp:
		return "stop implementing new behavior and run at most one cheap check"
	case PhaseReportNow:
		return "submit orc report now or report blocked/blocked now if blocked"
	default:
		return "inspect attempt deadline state"
	}
}
