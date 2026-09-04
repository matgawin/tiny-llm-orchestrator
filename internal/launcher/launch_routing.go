package launcher

import (
	"maps"

	"tiny-llm-orchestrator/orc/internal/config"
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

func workflowStateEntryForDecision(workflowConfig config.Workflow, decision workflow.Decision, attempt runstore.Attempt, ok bool) runstore.WorkflowStateEntryRequest {
	if decision.Kind != workflow.DecisionSelectStep {
		return runstore.WorkflowStateEntryRequest{}
	}

	key, _ := workflowConfig.EffectiveStepLoop(decision.Step)

	entry := runstore.WorkflowStateEntryRequest{
		State:      decision.Step,
		CounterKey: key,
	}
	if ok {
		entry.PreviousState = attempt.StepID
		entry.TriggerStatus = attempt.Status
		entry.TriggerResult = attempt.Result
	}

	return entry
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

	overrideKey := override.CounterKey
	if overrideKey == "" {
		overrideKey = override.TargetState
	}

	decisionKey := decision.CounterKey
	if decisionKey == "" {
		decisionKey = decision.State
	}

	return override.Workflow == decision.Workflow &&
		override.TargetState == decision.State &&
		overrideKey == decisionKey &&
		override.CountBeforeOverride == decision.CurrentCount &&
		override.CountAfterOverride == decision.ProspectiveCount &&
		override.Soft == decision.Soft &&
		override.Hard == decision.Hard &&
		override.Reason == runstore.WorkflowLoopHardCapReason
}
