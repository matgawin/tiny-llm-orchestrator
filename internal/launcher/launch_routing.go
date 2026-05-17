package launcher

import (
	"maps"

	"tiny-llm-orchestrator/orc/internal/loopcap"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

type startRouting struct {
	consumeAttemptID string
	retryLineage     *runstore.RetryLineage
	supersedeReason  string
}

func startRoutingForDecision(decision workflow.Decision, attempt runstore.Attempt, ok bool) startRouting {
	if !ok {
		return startRouting{}
	}

	routing := startRouting{consumeAttemptID: attempt.AttemptID}
	if decision.Kind != workflow.DecisionRetryStep {
		return routing
	}

	routing.retryLineage = &runstore.RetryLineage{
		StepID: decision.Retry.Step,
		Counts: maps.Clone(decision.Retry.Counts),
	}
	routing.supersedeReason = attempt.Status + "/" + attempt.Result

	return routing
}

func workflowStateEntryForDecision(decision workflow.Decision, attempt runstore.Attempt, ok bool) runstore.WorkflowStateEntryRequest {
	if decision.Kind != workflow.DecisionSelectStep || !ok {
		return runstore.WorkflowStateEntryRequest{}
	}

	return runstore.WorkflowStateEntryRequest{
		State:         decision.Step,
		PreviousState: attempt.StepID,
		TriggerStatus: attempt.Status,
		TriggerResult: attempt.Result,
	}
}

func workflowEntryOutcome(status runstore.Status, latestOutcome runstore.Attempt, hasOutcome bool) (runstore.Attempt, bool) {
	if hasOutcome {
		return latestOutcome, true
	}

	return runstore.ResolvedHumanBlockOutcome(status)
}

func workflowLoopHardCapOverrideMatches(override *runstore.WorkflowLoopHardCapOverride, decision loopcap.Decision) bool {
	if override == nil {
		return false
	}

	return override.Workflow == decision.Workflow &&
		override.TargetState == decision.State &&
		override.CountBeforeOverride == decision.CurrentCount &&
		override.CountAfterOverride == decision.ProspectiveCount &&
		override.Soft == decision.Soft &&
		override.Hard == decision.Hard &&
		override.Reason == runstore.WorkflowLoopHardCapReason
}
