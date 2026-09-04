//nolint:goconst // Test strings are clearer in place.
package loopcap

import (
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestEvaluateIgnoresDisabledRetryAndTerminalDecisions(t *testing.T) {
	status := runstore.Status{
		Workflow: "implementation",
		WorkflowLoop: runstore.WorkflowLoop{
			Counts: map[string]int{"code": 4},
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
			decision: workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "code"},
		},
		{
			name:     "retry step",
			caps:     caps,
			decision: workflow.Decision{Kind: workflow.DecisionRetryStep, Step: "code"},
		},
		{
			name:     "terminal handoff",
			caps:     caps,
			decision: workflow.Decision{Kind: workflow.DecisionTerminal, RunStatus: workflow.RunStatusBlockedForHuman},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(config.Workflow{Name: "implementation", LoopCaps: tt.caps, Steps: map[string]config.Step{"code": {}}}, status, tt.decision, runstore.Attempt{}, false); got.Kind != DecisionNone {
				t.Fatalf("Evaluate kind = %q, want none", got.Kind)
			}
		})
	}
}

func TestEvaluateSoftAndHardThresholds(t *testing.T) {
	caps := config.EffectiveLoopCaps{Enabled: true, Soft: 2, Hard: 4}
	latest := runstore.Attempt{StepID: "test", Status: "done", Result: "passed"}

	wf := config.Workflow{Name: "implementation", LoopCaps: caps, Steps: map[string]config.Step{"code": {}}}

	soft := Evaluate(wf, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"code": 2}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "code"}, latest, true)
	if soft.Kind != DecisionSoft || soft.ProspectiveCount != 3 || soft.PreviousState != "test" || soft.TriggerStatus != "done" || soft.TriggerResult != "passed" {
		t.Fatalf("soft decision = %+v, want threshold decision with trigger", soft)
	}

	hard := Evaluate(wf, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"code": 4}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "code"}, latest, true)
	if hard.Kind != DecisionHard || hard.CurrentCount != 4 || hard.ProspectiveCount != 5 {
		t.Fatalf("hard decision = %+v, want hard threshold before count increment", hard)
	}

	hardAgain := Evaluate(wf, runstore.Status{
		WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"code": 5}},
	}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "code"}, latest, true)
	if hardAgain.Kind != DecisionHard || hardAgain.CurrentCount != 5 || hardAgain.ProspectiveCount != 6 {
		t.Fatalf("hardAgain decision = %+v, want repeated hard threshold after override", hardAgain)
	}
}

func TestEvaluateUsesSharedStepCounter(t *testing.T) {
	wf := config.Workflow{Name: "implementation", LoopCaps: config.EffectiveLoopCaps{Enabled: true, Soft: 9, Hard: 10}, Steps: map[string]config.Step{
		"code_fixer": {Loop: &config.StepLoopConfig{Key: "coding", Soft: 2, Hard: 3}},
	}}

	got := Evaluate(wf, runstore.Status{WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"coding": 2}}}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "code_fixer"}, runstore.Attempt{}, false)
	if got.Kind != DecisionSoft || got.CounterKey != "coding" || got.State != "code_fixer" || got.ProspectiveCount != 3 {
		t.Fatalf("Evaluate = %+v, want keyed soft decision", got)
	}
}

func TestEvaluateUsesSharedStepHardCap(t *testing.T) {
	wf := config.Workflow{Name: "implementation", LoopCaps: config.EffectiveLoopCaps{Enabled: true, Soft: 9, Hard: 10}, Steps: map[string]config.Step{
		"code":  {Loop: &config.StepLoopConfig{Key: "coding", Soft: 2, Hard: 3}},
		"fixer": {Loop: &config.StepLoopConfig{Key: "coding", Soft: 2, Hard: 3}},
	}}

	got := Evaluate(wf, runstore.Status{WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"coding": 3}}}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "fixer"}, runstore.Attempt{}, false)
	if got.Kind != DecisionHard || got.CounterKey != "coding" || got.State != "fixer" || got.ProspectiveCount != 4 {
		t.Fatalf("Evaluate = %+v, want keyed hard decision", got)
	}
}

func TestEvaluateIgnoresDisabledSharedStepCap(t *testing.T) {
	wf := config.Workflow{Name: "implementation", LoopCaps: config.EffectiveLoopCaps{Enabled: false}, Steps: map[string]config.Step{
		"fixer": {Loop: &config.StepLoopConfig{Key: "coding", Soft: 2, Hard: 3}},
	}}

	got := Evaluate(wf, runstore.Status{WorkflowLoop: runstore.WorkflowLoop{Counts: map[string]int{"coding": 3}}}, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: "fixer"}, runstore.Attempt{}, false)
	if got.Kind != DecisionNone {
		t.Fatalf("Evaluate = %+v, want no decision", got)
	}
}
