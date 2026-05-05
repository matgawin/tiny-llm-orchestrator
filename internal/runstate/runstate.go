// Package runstate adapts persisted run-store status into workflow evaluation state.
package runstate

import (
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// WorkflowState returns the workflow evaluation state for a persisted run status,
// including the latest valid reported outcome when present.
func WorkflowState(status runstore.Status) workflow.RunState {
	state := workflow.RunState{
		Status:        status.State,
		ActiveAttempt: status.ActiveAttempt != nil,
	}
	if attempt, ok := runstore.LatestReportedOutcome(status); ok {
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
