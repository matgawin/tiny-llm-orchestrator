// Package attemptdeadline computes worker attempt deadline guidance.
package attemptdeadline

import (
	"fmt"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
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
	reportNowDivisor   = 15
	wrapUpDivisor      = 6
	narrowDivisor      = 3
)

type ActionGroup string

const (
	ActionGroupPlanning       ActionGroup = "planning"
	ActionGroupImplementation ActionGroup = "implementation"
	ActionGroupReview         ActionGroup = "review"
	ActionGroupVerification   ActionGroup = "verification"
	ActionGroupGeneric        ActionGroup = "generic"
)

type Guidance struct {
	RunID        string
	StepID       string
	AgentID      string
	AttemptID    string
	StartedAt    time.Time
	Deadline     time.Time
	CalculatedAt time.Time
	Elapsed      time.Duration
	Remaining    time.Duration
	Phase        string
	Action       string
	TimeoutRaw   string
}

func FromAttempt(attempt runstore.Attempt, now time.Time, group ActionGroup) (Guidance, error) {
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
	phase := Phase(remaining, timeout)

	return Guidance{
		RunID:        attempt.RunID,
		StepID:       attempt.StepID,
		AgentID:      attempt.AgentID,
		AttemptID:    attempt.AttemptID,
		StartedAt:    startedAt,
		Deadline:     deadline,
		CalculatedAt: now.UTC(),
		Elapsed:      elapsed,
		Remaining:    remaining,
		Phase:        phase,
		Action:       Action(phase, group),
		TimeoutRaw:   attempt.Timeout,
	}, nil
}

func Env(attempt runstore.Attempt) map[string]string {
	guidance, err := FromAttempt(attempt, attempt.StartedAt, ActionGroupGeneric)
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

func Phase(remaining, timeout time.Duration) string {
	reportNowThreshold := min(reportNowThreshold, timeout/reportNowDivisor)
	wrapUpThreshold := min(wrapUpThreshold, timeout/wrapUpDivisor)
	narrowThreshold := min(narrowThreshold, timeout/narrowDivisor)

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

func GroupForRole(role string) ActionGroup {
	switch role {
	case "planner":
		return ActionGroupPlanning
	case "coder", "mechanical-coder":
		return ActionGroupImplementation
	case "reviewer", "mechanical-reviewer", "readability-reviewer", "redundancy-reviewer", "docs-reviewer":
		return ActionGroupReview
	case "tester", "test-designer", "bug-reproducer":
		return ActionGroupVerification
	default:
		return ActionGroupGeneric
	}
}

func GroupForAttempt(run *runstore.Run, attempt runstore.Attempt) (ActionGroup, error) {
	version := attempt.ConfigSnapshotVersion
	if version == 0 {
		version = 1
	}

	loaded, err := configsnapshot.LoadVersion(run, version)
	if err != nil {
		return "", fmt.Errorf("load config snapshot version %d: %w", version, err)
	}

	workflowConfig, ok := loaded.Project.Workflows[run.Status.Workflow]
	if !ok {
		return "", fmt.Errorf("workflow %q is not present in config snapshot version %d", run.Status.Workflow, version)
	}

	step, ok := workflowConfig.Steps[attempt.StepID]
	if !ok {
		return "", fmt.Errorf("step %q is not present in config snapshot version %d", attempt.StepID, version)
	}

	if step.EffectiveAgentID() != attempt.AgentID {
		return "", fmt.Errorf("step %q uses agent %q, not attempt agent %q", attempt.StepID, step.EffectiveAgentID(), attempt.AgentID)
	}

	if step.EffectiveKind() != config.StepKindAgent {
		return ActionGroupGeneric, nil
	}

	agent, ok := loaded.Project.Agents[attempt.AgentID]
	if !ok {
		return "", fmt.Errorf("agent %q is not present in config snapshot version %d", attempt.AgentID, version)
	}

	return GroupForRole(agent.Role), nil
}

func Action(phase string, group ActionGroup) string {
	if phase == PhaseReportNow {
		return "submit orc report now or report blocked/blocked now if blocked"
	}

	actions := map[ActionGroup]map[string]string{
		ActionGroupPlanning:       {PhaseNormal: "produce the smallest complete scope envelope", PhaseNarrow: "stop discovery and finalize the scope envelope", PhaseWrapUp: "stop adding plan detail and prepare the report"},
		ActionGroupImplementation: {PhaseNormal: "continue the scoped implementation", PhaseNarrow: "finish the current change and add no new behavior", PhaseWrapUp: "stop editing, run at most one cheap check, and prepare the report"},
		ActionGroupReview:         {PhaseNormal: "review only the assigned review category", PhaseNarrow: "stop broad searches and verify current findings", PhaseWrapUp: "stop adding findings and prepare the report"},
		ActionGroupVerification:   {PhaseNormal: "run the scoped verification", PhaseNarrow: "stop adding test surfaces and finish the current check", PhaseWrapUp: "run no new checks and prepare the report"},
		ActionGroupGeneric:        {PhaseNormal: "continue scoped work", PhaseNarrow: "stop expanding scope", PhaseWrapUp: "stop new work and prepare the report"},
	}
	if action := actions[group][phase]; action != "" {
		return action
	}

	return "inspect attempt deadline state"
}
