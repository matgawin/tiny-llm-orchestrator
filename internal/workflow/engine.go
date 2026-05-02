package workflow

import (
	"fmt"
	"maps"
	"slices"

	"tiny-llm-orchestrator/orc/internal/config"
)

// Evaluate applies deterministic workflow routing to a validated workflow and
// current in-memory run state.
func Evaluate(workflow config.Workflow, state RunState) (Decision, error) {
	switch state.Status {
	case RunStatusReadyForHuman, RunStatusBlockedForHuman, RunStatusCancelled:
		return Decision{Kind: DecisionTerminal, RunStatus: state.Status}, nil
	case RunStatusRunning:
	default:
		if state.Status == "" {
			return Decision{}, fmt.Errorf("run status is required")
		}
		return Decision{}, fmt.Errorf("unsupported run status %q", state.Status)
	}

	if state.ActiveAttempt {
		if state.SelectedStep != "" {
			return Decision{}, fmt.Errorf("running state has both selected step %q and active attempt", state.SelectedStep)
		}
		if state.Outcome != nil {
			return Decision{}, fmt.Errorf("running state has both active attempt and terminal outcome")
		}
		return Decision{Kind: DecisionWaitActiveAttempt, RunStatus: RunStatusRunning}, nil
	}

	if state.Outcome == nil {
		step := state.SelectedStep
		if step == "" {
			step = workflow.Start
		}
		if _, ok := workflow.Steps[step]; !ok {
			return Decision{}, fmt.Errorf("selected step %q is not declared", step)
		}
		if err := validateRetryLineage(state.Retry, step); err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionSelectStep, Step: step, RunStatus: RunStatusRunning, Retry: state.Retry}, nil
	}

	return evaluateOutcome(workflow, state)
}

func evaluateOutcome(workflow config.Workflow, state RunState) (Decision, error) {
	if state.SelectedStep == "" {
		return Decision{}, fmt.Errorf("selected step is required when evaluating an outcome")
	}
	outcome := *state.Outcome
	if outcome.Step == "" {
		return Decision{}, fmt.Errorf("outcome step is required")
	}
	if outcome.Step != state.SelectedStep {
		return Decision{}, fmt.Errorf("outcome step %q does not match selected step %q", outcome.Step, state.SelectedStep)
	}
	step, ok := workflow.Steps[outcome.Step]
	if !ok {
		return Decision{}, fmt.Errorf("outcome step %q is not declared", outcome.Step)
	}
	pair := pairKey(outcome.Status, outcome.Result)
	if !allowedPair(step, outcome.Status, outcome.Result) {
		return Decision{}, fmt.Errorf("step %q outcome %q is not declared in allowed_results", outcome.Step, pair)
	}
	target, ok := step.On[pair]
	if !ok {
		return Decision{}, fmt.Errorf("step %q outcome %q has no deterministic transition", outcome.Step, pair)
	}
	if err := validateRetryLineage(state.Retry, outcome.Step); err != nil {
		return Decision{}, err
	}

	retryCount := 0
	if state.Retry.Step == outcome.Step {
		retryCount = state.Retry.Counts[pair]
	}
	maxRetries := workflow.Defaults.Retries[pair]
	if retryCount < maxRetries {
		counts := maps.Clone(state.Retry.Counts)
		if counts == nil {
			counts = map[string]int{}
		}
		counts[pair] = retryCount + 1
		nextRetry := RetryLineage{
			Step:   outcome.Step,
			Counts: counts,
		}
		return Decision{
			Kind:      DecisionRetryStep,
			Step:      outcome.Step,
			RunStatus: RunStatusRunning,
			Retry:     nextRetry,
		}, nil
	}

	if _, targetIsStep := workflow.Steps[target]; targetIsStep {
		return Decision{Kind: DecisionSelectStep, Step: target, RunStatus: RunStatusRunning}, nil
	}
	if isTerminalRunStatus(target) {
		return Decision{Kind: DecisionTerminal, RunStatus: target}, nil
	}
	return Decision{}, fmt.Errorf("step %q outcome %q targets unknown step or terminal state %q", outcome.Step, pair, target)
}

func validateRetryLineage(retry RetryLineage, step string) error {
	if retry.Step != "" && retry.Step != step {
		return fmt.Errorf("retry lineage step %q does not match selected step %q", retry.Step, step)
	}
	for pair, count := range retry.Counts {
		if count < 0 {
			return fmt.Errorf("retry count for %q must be >= 0, got %d", pair, count)
		}
	}
	if retry.Step == "" && len(retry.Counts) > 0 {
		return fmt.Errorf("retry lineage step is required when retry counts are present")
	}
	return nil
}

func allowedPair(step config.Step, status, result string) bool {
	results, ok := step.AllowedResults[status]
	if !ok {
		return false
	}
	return slices.Contains(results, result)
}

func pairKey(status, result string) string {
	return status + "/" + result
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case RunStatusReadyForHuman, RunStatusBlockedForHuman, RunStatusCancelled:
		return true
	default:
		return false
	}
}
