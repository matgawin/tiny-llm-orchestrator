package launcher

import (
	"fmt"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	reviewDone             = "done"
	reviewChangesRequested = "changes_requested"
	reviewReady            = "ready"
)

// ReviewFindingDecision resolves the repeated-finding guard for one routed step.
func ReviewFindingDecision(workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision, latest runstore.Attempt) (*runstore.ReviewFindingOverride, *runstore.ReviewFindingBlock, error) {
	return repeatedReviewFindingDecision(workflowConfig, run.Status, decision, latest, func(attempt runstore.Attempt) (string, error) {
		return correctionStepForReport(run, attempt)
	})
}

func repeatedReviewFindingDecision(workflowConfig config.Workflow, status runstore.Status, decision workflow.Decision, latest runstore.Attempt, correctionStep func(runstore.Attempt) (string, error)) (*runstore.ReviewFindingOverride, *runstore.ReviewFindingBlock, error) {
	if decision.Kind != workflow.DecisionSelectStep || workflowConfig.Steps[decision.Step].EffectiveKind() != config.StepKindAgent || !acceptedFindingReport(latest) {
		return nil, nil, nil
	}

	if override := status.PendingReviewFindingOverride; override != nil && override.TargetStep == decision.Step && override.RepeatedReportAttemptID == latest.AttemptID {
		return override, nil, nil
	}

	for _, finding := range latest.Report.Findings {
		occurrences, firstIndex, firstAttemptID := findingOccurrences(status.Attempts, finding.FindingID)

		if occurrences < 2 || firstAttemptID == latest.AttemptID {
			continue
		}

		expectedCorrectionStep, err := correctionStep(status.Attempts[firstIndex])
		if err != nil {
			return nil, nil, err
		}

		corrected := false

		for i := firstIndex + 1; i < len(status.Attempts); i++ {
			attempt := status.Attempts[i]
			if attempt.AttemptID == latest.AttemptID {
				break
			}

			if attempt.StepID == expectedCorrectionStep && attempt.State == runstore.AttemptStateReported && attempt.Status == reviewDone && attempt.Result == reviewReady && attempt.Report != nil && attempt.ReportRef != nil {
				corrected = true
				break
			}
		}

		if corrected {
			return nil, &runstore.ReviewFindingBlock{Reason: runstore.RepeatedReviewFindingReason, FindingID: finding.FindingID, ReviewerStepID: latest.StepID, ProposedCorrectionStepID: decision.Step, FirstReportAttemptID: firstAttemptID, RepeatedReportAttemptID: latest.AttemptID, OccurrenceCount: occurrences}, nil
		}
	}

	return nil, nil, nil
}

func acceptedFindingReport(attempt runstore.Attempt) bool {
	return attempt.State == runstore.AttemptStateReported && attempt.Status == reviewDone && attempt.Result == reviewChangesRequested && attempt.Report != nil && attempt.ReportRef != nil && len(attempt.Report.Findings) > 0
}

func correctionStepForReport(run *runstore.Run, attempt runstore.Attempt) (string, error) {
	version := attempt.ConfigSnapshotVersion
	if version == 0 {
		version = 1
	}

	snapshot, err := configsnapshot.LoadVersion(run, version)
	if err != nil {
		return "", fmt.Errorf("load config snapshot for first finding report: %w", err)
	}

	step := snapshot.Project.Workflows[run.Status.Workflow].Steps[attempt.StepID]

	return step.On[reviewDone+"/"+reviewChangesRequested], nil
}

func findingOccurrences(attempts []runstore.Attempt, findingID string) (int, int, string) {
	count, firstIndex, firstAttemptID := 0, -1, ""

	for i, attempt := range attempts {
		if !acceptedFindingReport(attempt) {
			continue
		}

		for _, finding := range attempt.Report.Findings {
			if finding.FindingID == findingID {
				count++

				if firstIndex < 0 {
					firstIndex, firstAttemptID = i, attempt.AttemptID
				}
			}
		}
	}

	return count, firstIndex, firstAttemptID
}
