// Package loopcap evaluates configured workflow loop caps against persisted
// workflow state-entry counters.
package loopcap

import (
	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// DecisionKind identifies whether a prospective workflow state entry hits a cap.
type DecisionKind string

const (
	DecisionNone DecisionKind = ""
	DecisionSoft DecisionKind = "soft"
	DecisionHard DecisionKind = "hard"
)

// Decision describes the cap effect for a prospective worker-selecting state.
type Decision struct {
	Kind             DecisionKind
	Workflow         string
	State            string
	CurrentCount     int
	ProspectiveCount int
	Soft             int
	Hard             int
	PreviousState    string
	TriggerStatus    string
	TriggerResult    string
}

// Evaluate returns the loop-cap decision for a routing decision. Caps only
// apply when routing enters a worker-selecting state with a new state-entry
// count; retries, terminals, handoffs, active waits, and disabled caps bypass
// enforcement.
func Evaluate(workflowName string, caps config.EffectiveLoopCaps, status runstore.Status, routing workflow.Decision, latest runstore.Attempt, hasLatest bool) Decision {
	if !caps.Enabled || routing.Kind != workflow.DecisionSelectStep {
		return Decision{}
	}

	current := status.WorkflowLoop.Counts[routing.Step]
	prospective := current + 1

	decision := Decision{
		Workflow:         workflowName,
		State:            routing.Step,
		CurrentCount:     current,
		ProspectiveCount: prospective,
		Soft:             caps.Soft,
		Hard:             caps.Hard,
	}
	if hasLatest {
		decision.PreviousState = latest.StepID
		decision.TriggerStatus = latest.Status
		decision.TriggerResult = latest.Result
	}

	if prospective >= caps.Hard+1 {
		decision.Kind = DecisionHard
		return decision
	}

	switch prospective {
	case caps.Soft + 1:
		decision.Kind = DecisionSoft
	default:
		return Decision{}
	}

	return decision
}

// SoftCap converts a soft-cap decision to its durable run-store representation.
func (d Decision) SoftCap() runstore.WorkflowLoopSoftCap {
	return runstore.WorkflowLoopSoftCap{
		Workflow:      d.Workflow,
		State:         d.State,
		Count:         d.ProspectiveCount,
		Soft:          d.Soft,
		Hard:          d.Hard,
		PreviousState: d.PreviousState,
		TriggerStatus: d.TriggerStatus,
		TriggerResult: d.TriggerResult,
	}
}

// HardCap converts a hard-cap decision to its durable run-store representation.
func (d Decision) HardCap() runstore.WorkflowLoopHardCap {
	return runstore.WorkflowLoopHardCap{
		Workflow:         d.Workflow,
		BlockedState:     d.State,
		CurrentCount:     d.CurrentCount,
		ProspectiveCount: d.ProspectiveCount,
		Soft:             d.Soft,
		Hard:             d.Hard,
		PreviousState:    d.PreviousState,
		TriggerStatus:    d.TriggerStatus,
		TriggerResult:    d.TriggerResult,
		Reason:           runstore.WorkflowLoopHardCapReason,
	}
}
