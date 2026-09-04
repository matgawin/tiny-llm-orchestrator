package launcher

import (
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const reviewFixerStep = "code_fixer"

func TestRepeatedReviewFindingDecisionStopsAfterCorrection(t *testing.T) {
	first := reportedFindingAttempt("review-1", "review", "same-finding")
	correction := runstore.Attempt{AttemptID: "fix-1", StepID: reviewFixerStep, State: runstore.AttemptStateReported, Status: reviewDone, Result: reviewReady, Report: &runstore.Report{}, ReportRef: &runstore.ArtifactRef{EventSequence: 2}}
	repeated := reportedFindingAttempt("review-2", "review", "same-finding")
	status := runstore.Status{Attempts: []runstore.Attempt{first, correction, repeated}}

	workflowConfig := config.Workflow{Steps: map[string]config.Step{"review": {On: map[string]string{"done/changes_requested": reviewFixerStep}}, reviewFixerStep: {Kind: config.StepKindAgent}}}

	override, block, err := repeatedReviewFindingDecision(workflowConfig, status, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: reviewFixerStep}, repeated, func(runstore.Attempt) (string, error) { return reviewFixerStep, nil })
	if err != nil {
		t.Fatal(err)
	}

	if override != nil || block == nil {
		t.Fatalf("decision = override %+v, block %+v", override, block)
	}

	if block.Reason != runstore.RepeatedReviewFindingReason || block.FirstReportAttemptID != "review-1" || block.RepeatedReportAttemptID != "review-2" || block.OccurrenceCount != 2 {
		t.Fatalf("block = %+v", block)
	}
}

func TestRepeatedReviewFindingDecisionAllowsNewFinding(t *testing.T) {
	first := reportedFindingAttempt("review-1", "review", "first-finding")
	correction := runstore.Attempt{AttemptID: "fix-1", StepID: reviewFixerStep, State: runstore.AttemptStateReported, Status: reviewDone, Result: reviewReady, Report: &runstore.Report{}, ReportRef: &runstore.ArtifactRef{EventSequence: 2}}
	latest := reportedFindingAttempt("review-2", "review", "new-regression")
	status := runstore.Status{Attempts: []runstore.Attempt{first, correction, latest}}

	workflowConfig := config.Workflow{Steps: map[string]config.Step{"review": {On: map[string]string{"done/changes_requested": reviewFixerStep}}, reviewFixerStep: {Kind: config.StepKindAgent}}}

	override, block, err := repeatedReviewFindingDecision(workflowConfig, status, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: reviewFixerStep}, latest, func(runstore.Attempt) (string, error) { return reviewFixerStep, nil })
	if err != nil {
		t.Fatal(err)
	}

	if override != nil || block != nil {
		t.Fatalf("decision = override %+v, block %+v", override, block)
	}
}

func TestRepeatedReviewFindingDecisionRequiresSelectedCorrection(t *testing.T) {
	first := reportedFindingAttempt("review-1", "review", "same-finding")
	unrelated := runstore.Attempt{AttemptID: "other-1", StepID: "other", State: runstore.AttemptStateReported, Status: reviewDone, Result: reviewReady, Report: &runstore.Report{}, ReportRef: &runstore.ArtifactRef{EventSequence: 2}}
	repeated := reportedFindingAttempt("review-2", "review", "same-finding")
	status := runstore.Status{Attempts: []runstore.Attempt{first, unrelated, repeated}}
	wf := config.Workflow{Steps: map[string]config.Step{reviewFixerStep: {Kind: config.StepKindAgent}}}

	_, block, err := repeatedReviewFindingDecision(wf, status, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: reviewFixerStep}, repeated, func(runstore.Attempt) (string, error) { return reviewFixerStep, nil })
	if err != nil {
		t.Fatal(err)
	}

	if block != nil {
		t.Fatalf("block = %+v, want no stop without the selected correction", block)
	}
}

func TestRepeatedReviewFindingDecisionRequiresAcceptedReports(t *testing.T) {
	first := reportedFindingAttempt("review-1", "review", "same-finding")
	first.ReportRef = nil
	repeated := reportedFindingAttempt("review-2", "review", "same-finding")
	status := runstore.Status{Attempts: []runstore.Attempt{first, repeated}}
	wf := config.Workflow{Steps: map[string]config.Step{reviewFixerStep: {Kind: config.StepKindAgent}}}

	_, block, err := repeatedReviewFindingDecision(wf, status, workflow.Decision{Kind: workflow.DecisionSelectStep, Step: reviewFixerStep}, repeated, func(runstore.Attempt) (string, error) { return reviewFixerStep, nil })
	if err != nil {
		t.Fatal(err)
	}

	if block != nil {
		t.Fatalf("block = %+v, want no stop for a report without ReportRef", block)
	}
}

func reportedFindingAttempt(id, step, findingID string) runstore.Attempt {
	return runstore.Attempt{AttemptID: id, StepID: step, State: runstore.AttemptStateReported, Status: reviewDone, Result: reviewChangesRequested, Report: &runstore.Report{Findings: []runstore.Finding{{FindingID: findingID}}}, ReportRef: &runstore.ArtifactRef{EventSequence: 1}}
}
