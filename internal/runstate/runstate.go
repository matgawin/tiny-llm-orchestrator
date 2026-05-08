// Package runstate adapts persisted run-store status into workflow evaluation state.
package runstate

import (
	"maps"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// WorkflowState returns the workflow evaluation state for a persisted run status,
// including the latest terminal outcome that workflow routing can consume.
func WorkflowState(status runstore.Status) workflow.RunState {
	state := workflow.RunState{
		Status:        status.State,
		ActiveAttempt: status.ActiveAttempt != nil,
	}
	if status.RetryLineage != nil {
		state.Retry = workflow.RetryLineage{
			Step:   status.RetryLineage.StepID,
			Counts: maps.Clone(status.RetryLineage.Counts),
		}
	}
	if step, ok := runstore.ResolvedHumanBlockStep(status); ok {
		state.SelectedStep = step
		return state
	}
	if status.State == workflow.RunStatusRunning && !state.ActiveAttempt && len(status.WorkflowLoop.Entries) > 0 {
		state.SelectedStep = status.WorkflowLoop.Entries[len(status.WorkflowLoop.Entries)-1].State
	}
	if attempt, ok := runstore.LatestConsumableOutcome(status); ok {
		state.ActiveAttempt = false
		state.SelectedStep = attempt.StepID
		state.Outcome = &workflow.Outcome{
			Step:   attempt.StepID,
			Status: attempt.Status,
			Result: attempt.Result,
		}
	}
	return state
}
