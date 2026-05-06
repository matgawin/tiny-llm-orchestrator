package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
)

const stepPlan = "plan"

const (
	pairFailedError         = "failed/error"
	pairFailedMissingReport = "failed/missing_report"
	pairFailedProcessError  = "failed/process_error"
)

func TestEvaluateImplementationWorkflowSelectsNextStep(t *testing.T) {
	tests := []struct {
		name     string
		outcome  Outcome
		wantStep string
	}{
		{
			name:     "plan ready",
			outcome:  Outcome{Step: stepPlan, Status: ReportStatusDone, Result: "ready"},
			wantStep: "code",
		},
		{
			name:     "code ready",
			outcome:  Outcome{Step: "code", Status: ReportStatusDone, Result: "ready"},
			wantStep: "test",
		},
		{
			name:     "test failed",
			outcome:  Outcome{Step: "test", Status: ReportStatusDone, Result: "failed"},
			wantStep: "code",
		},
		{
			name:     "test passed",
			outcome:  Outcome{Step: "test", Status: ReportStatusDone, Result: "passed"},
			wantStep: "review",
		},
		{
			name:     "review changes requested",
			outcome:  Outcome{Step: "review", Status: ReportStatusDone, Result: "changes_requested"},
			wantStep: "code",
		},
		{
			name:     "review approved",
			outcome:  Outcome{Step: "review", Status: ReportStatusDone, Result: "approved"},
			wantStep: "redundancy-review",
		},
		{
			name:     "redundancy review changes requested",
			outcome:  Outcome{Step: "redundancy-review", Status: ReportStatusDone, Result: "changes_requested"},
			wantStep: "code_fixer",
		},
		{
			name:     "code fixer ready",
			outcome:  Outcome{Step: "code_fixer", Status: ReportStatusDone, Result: "ready"},
			wantStep: "test-redundancy",
		},
		{
			name:     "redundancy test failed",
			outcome:  Outcome{Step: "test-redundancy", Status: ReportStatusDone, Result: "failed"},
			wantStep: "code_fixer",
		},
		{
			name:     "redundancy test passed",
			outcome:  Outcome{Step: "test-redundancy", Status: ReportStatusDone, Result: "passed"},
			wantStep: "redundancy-review",
		},
		{
			name:     "redundancy review approved",
			outcome:  Outcome{Step: "redundancy-review", Status: ReportStatusDone, Result: "approved"},
			wantStep: "readability-review",
		},
		{
			name:     "readability review changes requested",
			outcome:  Outcome{Step: "readability-review", Status: ReportStatusDone, Result: "changes_requested"},
			wantStep: "code_cleaner",
		},
		{
			name:     "code cleaner ready",
			outcome:  Outcome{Step: "code_cleaner", Status: ReportStatusDone, Result: "ready"},
			wantStep: "test-readability",
		},
		{
			name:     "readability test failed",
			outcome:  Outcome{Step: "test-readability", Status: ReportStatusDone, Result: "failed"},
			wantStep: "code_cleaner",
		},
		{
			name:     "readability test passed",
			outcome:  Outcome{Step: "test-readability", Status: ReportStatusDone, Result: "passed"},
			wantStep: "readability-review",
		},
	}

	workflow := implementationWorkflow(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := evaluateRunningOutcome(t, workflow, tt.outcome, RetryLineage{})
			assertSelectedStep(t, decision, tt.wantStep)
		})
	}
}

func TestEvaluateImplementationWorkflowRoutesTerminalOutcomes(t *testing.T) {
	workflow := implementationWorkflow(t)
	outcome := Outcome{Step: "readability-review", Status: ReportStatusDone, Result: "approved"}

	decision := evaluateRunningOutcome(t, workflow, outcome, RetryLineage{})
	assertTerminalDecision(t, decision, RunStatusReadyForHuman)
}

func TestEvaluateImplementationWorkflowBlocksForHumanOnAnyStep(t *testing.T) {
	workflow := implementationWorkflow(t)
	for _, step := range []string{stepPlan, "code", "test", "review", "redundancy-review", "code_fixer", "test-redundancy", "readability-review", "code_cleaner", "test-readability"} {
		t.Run(step, func(t *testing.T) {
			outcome := Outcome{Step: step, Status: ReportStatusBlocked, Result: "blocked"}

			decision := evaluateRunningOutcome(t, workflow, outcome, RetryLineage{})
			assertTerminalDecision(t, decision, RunStatusBlockedForHuman)
		})
	}
}

func TestEvaluateSelectsStartForNewRunningRun(t *testing.T) {
	decision, err := Evaluate(implementationWorkflow(t), RunState{Status: RunStatusRunning})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	assertSelectedStep(t, decision, stepPlan)
}

func TestEvaluateSelectedStepPreservesRetryLineage(t *testing.T) {
	workflow := implementationWorkflow(t)
	retry := RetryLineage{
		Step:   stepPlan,
		Counts: map[string]int{pairFailedMissingReport: 1},
	}

	decision, err := Evaluate(workflow, RunState{
		Status:       RunStatusRunning,
		SelectedStep: stepPlan,
		Retry:        retry,
	})
	if err != nil {
		t.Fatalf("Evaluate selected retry step returned error: %v", err)
	}
	assertSelectedStepWithRetry(t, decision, stepPlan, pairFailedMissingReport, 1)

	outcome := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "missing_report"}
	decision = evaluateRunningOutcome(t, workflow, outcome, decision.Retry)
	assertTerminalDecision(t, decision, RunStatusBlockedForHuman)
}

func TestEvaluateWaitsWhenActiveAttemptExists(t *testing.T) {
	decision, err := Evaluate(implementationWorkflow(t), RunState{
		Status:        RunStatusRunning,
		ActiveAttempt: true,
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	assertWaitActiveAttempt(t, decision)
}

func TestEvaluateTreatsCancelledAsTerminal(t *testing.T) {
	decision, err := Evaluate(implementationWorkflow(t), RunState{Status: RunStatusCancelled})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	assertTerminalDecision(t, decision, RunStatusCancelled)
}

func TestEvaluateRetriesBeforeTransition(t *testing.T) {
	outcome := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "missing_report"}

	decision := evaluateRunningOutcome(t, implementationWorkflow(t), outcome, RetryLineage{})
	assertRetryStep(t, decision, stepPlan, pairFailedMissingReport, 1)
}

func TestEvaluateAppliesTransitionAfterRetryExhaustion(t *testing.T) {
	outcome := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "missing_report"}

	decision := evaluateRunningOutcome(t, implementationWorkflow(t), outcome, RetryLineage{
		Step:   stepPlan,
		Counts: map[string]int{pairFailedMissingReport: 1},
	})
	assertTerminalDecision(t, decision, RunStatusBlockedForHuman)
}

func TestEvaluateZeroRetrySynthesizedFailureAppliesTransition(t *testing.T) {
	outcome := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "timeout"}

	decision := evaluateRunningOutcome(t, implementationWorkflow(t), outcome, RetryLineage{})
	assertTerminalDecision(t, decision, RunStatusBlockedForHuman)
}

func TestEvaluateSameStepTransitionAfterExhaustionResetsRetryLineage(t *testing.T) {
	workflow := singleStepRetryWorkflow()
	outcome := Outcome{Step: "code", Status: ReportStatusFailed, Result: "error"}

	decision := evaluateRunningOutcome(t, workflow, outcome, RetryLineage{
		Step:   "code",
		Counts: map[string]int{pairFailedError: 1},
	})
	assertSelectedStep(t, decision, "code")

	decision = evaluateRunningOutcome(t, workflow, outcome, RetryLineage{})
	assertRetryStep(t, decision, "code", pairFailedError, 1)
}

func TestEvaluateCrossStepReentryResetsRetryLineage(t *testing.T) {
	workflow := reentryRetryWorkflow()
	codeFailure := Outcome{Step: "code", Status: ReportStatusFailed, Result: "error"}
	testFailure := Outcome{Step: "test", Status: ReportStatusDone, Result: "failed"}

	decision := evaluateRunningOutcome(t, workflow, codeFailure, RetryLineage{
		Step:   "code",
		Counts: map[string]int{pairFailedError: 1},
	})
	assertSelectedStep(t, decision, "test")

	decision = evaluateRunningOutcome(t, workflow, testFailure, decision.Retry)
	assertSelectedStep(t, decision, "code")

	decision = evaluateRunningOutcome(t, workflow, codeFailure, decision.Retry)
	assertRetryStep(t, decision, "code", pairFailedError, 1)
}

func TestEvaluateAlternatingRetryableOutcomesShareStepLineage(t *testing.T) {
	workflow := implementationWorkflow(t)
	missingReport := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "missing_report"}
	processError := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "process_error"}

	decision := evaluateRunningOutcome(t, workflow, missingReport, RetryLineage{})
	assertRetryStep(t, decision, stepPlan, pairFailedMissingReport, 1)

	decision = evaluateRunningOutcome(t, workflow, processError, decision.Retry)
	assertRetryStep(t, decision, stepPlan, pairFailedProcessError, 1)
	if decision.Retry.Counts[pairFailedMissingReport] != 1 {
		t.Fatalf("retry lineage = %+v, want missing_report count preserved", decision.Retry)
	}

	decision = evaluateRunningOutcome(t, workflow, missingReport, decision.Retry)
	assertTerminalDecision(t, decision, RunStatusBlockedForHuman)
}

func TestEvaluateRejectsUndeclaredSynthesizedFailure(t *testing.T) {
	workflow := implementationWorkflow(t)
	step := workflow.Steps[stepPlan]
	step.AllowedResults = map[string][]string{
		ReportStatusDone: {"ready"},
	}
	step.On = map[string]string{
		"done/ready": "code",
	}
	workflow.Steps[stepPlan] = step
	outcome := Outcome{Step: stepPlan, Status: ReportStatusFailed, Result: "timeout"}

	_, err := Evaluate(workflow, RunState{
		Status:       RunStatusRunning,
		SelectedStep: stepPlan,
		Outcome:      &outcome,
	})
	if err == nil {
		t.Fatal("Evaluate returned nil error, want undeclared synthesized failure error")
	}
	if !strings.Contains(err.Error(), `step "plan" outcome "failed/timeout" is not declared`) {
		t.Fatalf("error = %v, want undeclared outcome", err)
	}
}

func TestEvaluateRejectsInvalidSequentialState(t *testing.T) {
	_, err := Evaluate(implementationWorkflow(t), RunState{
		Status:        RunStatusRunning,
		SelectedStep:  stepPlan,
		ActiveAttempt: true,
	})
	if err == nil {
		t.Fatal("Evaluate returned nil error, want selected step plus active attempt error")
	}
}

func implementationWorkflow(t *testing.T) config.Workflow {
	t.Helper()

	project, err := config.Load(filepath.Join("..", "initconfig", "scaffold"))
	if err != nil {
		t.Fatalf("load scaffold config: %v", err)
	}
	workflow, ok := project.Workflows["implementation"]
	if !ok {
		t.Fatal("scaffold config did not load implementation workflow")
	}
	return workflow
}

func singleStepRetryWorkflow() config.Workflow {
	return retryWorkflow("same-step", map[string]string{pairFailedError: "code"}, nil)
}

func reentryRetryWorkflow() config.Workflow {
	testStep := config.Step{
		Agent: "tester",
		AllowedResults: map[string][]string{
			ReportStatusDone: {"failed"},
		},
		On: map[string]string{
			"done/failed": "code",
		},
	}
	return retryWorkflow("reentry", map[string]string{pairFailedError: "test"}, &testStep)
}

func retryWorkflow(name string, codeTransitions map[string]string, testStep *config.Step) config.Workflow {
	steps := map[string]config.Step{
		"code": retryableCodeStep(codeTransitions),
	}
	if testStep != nil {
		steps["test"] = *testStep
	}
	return config.Workflow{
		Name:  name,
		Start: "code",
		Defaults: config.Defaults{
			Retries: map[string]int{pairFailedError: 1},
		},
		Steps: steps,
	}
}

func retryableCodeStep(transitions map[string]string) config.Step {
	return config.Step{
		Agent: "coder",
		AllowedResults: map[string][]string{
			ReportStatusFailed: {"error"},
		},
		On: transitions,
	}
}

func emptyRetryLineage(retry RetryLineage) bool {
	return retry.Step == "" && len(retry.Counts) == 0
}

func evaluateRunningOutcome(t *testing.T, workflow config.Workflow, outcome Outcome, retry RetryLineage) Decision {
	t.Helper()

	decision, err := Evaluate(workflow, RunState{
		Status:       RunStatusRunning,
		SelectedStep: outcome.Step,
		Outcome:      &outcome,
		Retry:        retry,
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	return decision
}

func assertDecision(t *testing.T, got, want Decision) {
	t.Helper()

	if got.Kind != want.Kind {
		t.Fatalf("decision = %+v, want kind %q", got, want.Kind)
	}
	if got.Step != want.Step {
		t.Fatalf("decision = %+v, want step %q", got, want.Step)
	}
	if got.RunStatus != want.RunStatus {
		t.Fatalf("decision = %+v, want run status %q", got, want.RunStatus)
	}
}

func assertSelectedStep(t *testing.T, decision Decision, step string) {
	t.Helper()

	want := Decision{Kind: DecisionSelectStep, Step: step, RunStatus: RunStatusRunning}
	assertDecision(t, decision, want)
	assertNoRetryLineage(t, decision)
}

func assertSelectedStepWithRetry(t *testing.T, decision Decision, step, pair string, count int) {
	t.Helper()

	want := Decision{Kind: DecisionSelectStep, Step: step, RunStatus: RunStatusRunning}
	assertDecision(t, decision, want)
	if decision.Retry.Step != step {
		t.Fatalf("retry lineage = %+v, want step %q", decision.Retry, step)
	}
	if decision.Retry.Counts[pair] != count {
		t.Fatalf("retry lineage = %+v, want pair %q count %d", decision.Retry, pair, count)
	}
}

func assertRetryStep(t *testing.T, decision Decision, step, pair string, count int) {
	t.Helper()

	want := Decision{Kind: DecisionRetryStep, Step: step, RunStatus: RunStatusRunning}
	assertDecision(t, decision, want)
	if decision.Retry.Step != step {
		t.Fatalf("retry lineage = %+v, want step %q", decision.Retry, step)
	}
	if decision.Retry.Counts[pair] != count {
		t.Fatalf("retry lineage = %+v, want pair %q count %d", decision.Retry, pair, count)
	}
}

func assertWaitActiveAttempt(t *testing.T, decision Decision) {
	t.Helper()

	want := Decision{Kind: DecisionWaitActiveAttempt, RunStatus: RunStatusRunning}
	assertDecision(t, decision, want)
	assertNoRetryLineage(t, decision)
}

func assertTerminalDecision(t *testing.T, decision Decision, status string) {
	t.Helper()

	want := Decision{Kind: DecisionTerminal, RunStatus: status}
	assertDecision(t, decision, want)
	assertNoRetryLineage(t, decision)
}

func assertNoRetryLineage(t *testing.T, decision Decision) {
	t.Helper()

	if !emptyRetryLineage(decision.Retry) {
		t.Fatalf("decision = %+v, want no retry lineage", decision)
	}
}
