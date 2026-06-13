package loopcap

import (
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestEvaluateIgnoresDisabledRetryAndTerminalDecisions(t *testing.T) {
	status := runstore.Status{
		Workflow: loopcapWorkflowImplementation,
		WorkflowLoop: runstore.WorkflowLoop{
			Counts: map[string]int{loopcapStepCode: 4},
		},
	}

	caps := config.EffectiveLoopCaps{Enabled: true, Soft: 2, Hard: 4}
	for _, tt := range []struct {
		name     string
		caps     config.EffectiveLoopCaps
		decision workflow.Decision
	}{
		{
			name:     "disabled caps",
			caps:     config.EffectiveLoopCaps{Enabled: false, Soft: 2, Hard: 4},
			decision: workflow.Decision{Kind: workflow.DecisionSelectStep, Step: loopcapStepCode},
		},
		{
			name:     "retry step",
			caps:     caps,
			decision: workflow.Decision{Kind: workflow.DecisionRetryStep, Step: loopcapStepCode},
		},
		{
			name:     "terminal handoff",
			caps:     caps,
			decision: workflow.Decision{Kind: workflow.DecisionTerminal, RunStatus: workflow.RunStatusBlockedForHuman},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(loopcapWorkflowImplementation, tt.caps, status, tt.decision, runstore.Attempt{}, false); got.Kind != DecisionNone {
				t.Fatalf("Evaluate kind = %q, want none", got.Kind)
			}
		})
	}
}

func TestEvaluateSoftAndHardThresholds(t *testing.T) {
	caps := config.EffectiveLoopCaps{Enabled: true, Soft: 2, Hard: 4}
	latest := runstore.Attempt{StepID: loopcapStepTest, Status: loopcapStatusDone, Result: loopcapResultPassed}

	soft := Evaluate(loopcapWorkflowImplementation, caps, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{loopcapStepCode: 2}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: loopcapStepCode}, latest, true)
	if soft.Kind != DecisionSoft || soft.ProspectiveCount != 3 || soft.PreviousState != loopcapStepTest || soft.TriggerStatus != loopcapStatusDone || soft.TriggerResult != loopcapResultPassed {
		t.Fatalf("soft decision = %+v, want threshold decision with trigger", soft)
	}

	hard := Evaluate(loopcapWorkflowImplementation, caps, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{loopcapStepCode: 4}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: loopcapStepCode}, latest, true)
	if hard.Kind != DecisionHard || hard.CurrentCount != 4 || hard.ProspectiveCount != 5 {
		t.Fatalf("hard decision = %+v, want hard threshold before count increment", hard)
	}

	hardAgain := Evaluate(loopcapWorkflowImplementation, caps, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{loopcapStepCode: 5}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: loopcapStepCode}, latest, true)
	if hardAgain.Kind != DecisionHard || hardAgain.CurrentCount != 5 || hardAgain.ProspectiveCount != 6 {
		t.Fatalf("hardAgain decision = %+v, want repeated hard threshold after override", hardAgain)
	}
}
