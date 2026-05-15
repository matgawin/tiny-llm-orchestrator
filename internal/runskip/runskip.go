// Package runskip owns the internal audited workflow-step skip primitive.
package runskip

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// Options describes one explicit human skip decision.
type Options struct {
	Root   string
	RunID  string
	StepID string
	Reason string
	Source string
	Time   time.Time
}

// Result describes the applied skip transition.
type Result struct {
	RunID  string
	StepID string
	Status runstore.Status
	Event  runstore.Event
}

// Skip validates and persists a system-owned done/skipped transition for the
// currently selected workflow step.
func Skip(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opts.Root == "" {
		return Result{}, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return Result{}, errors.New("run id is required")
	}
	opts.StepID = strings.TrimSpace(opts.StepID)
	if opts.StepID == "" {
		return Result{}, errors.New("step id is required")
	}
	opts.Reason = strings.TrimSpace(opts.Reason)
	if opts.Reason == "" {
		return Result{}, errors.New("skip reason is required")
	}
	loaded, err := runcontext.Load(opts.Root, opts.RunID)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	status, event, err := loaded.Store.RecordStepSkip(opts.RunID, runstore.RecordStepSkipRequest{
		StepID: opts.StepID,
		Reason: opts.Reason,
		Source: opts.Source,
		Time:   opts.Time,
	}, func(status runstore.Status) (runstore.StepSkipTransition, error) {
		return validateSkip(loaded.Workflow, status, opts.StepID)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{RunID: opts.RunID, StepID: opts.StepID, Status: status, Event: event}, nil
}

func validateSkip(workflowConfig config.Workflow, status runstore.Status, stepID string) (runstore.StepSkipTransition, error) {
	if status.ActiveAttempt != nil {
		return runstore.StepSkipTransition{}, fmt.Errorf("run %q has active attempt %q; cannot skip step", status.RunID, status.ActiveAttempt.AttemptID)
	}
	switch status.State {
	case workflow.RunStatusReadyForHuman, workflow.RunStatusBlockedForHuman, workflow.RunStatusCancelled:
		return runstore.StepSkipTransition{}, fmt.Errorf("run %q is terminal (%s); cannot skip step", status.RunID, status.State)
	}
	decision, err := workflow.Evaluate(workflowConfig, runstate.WorkflowState(status))
	if err != nil {
		return runstore.StepSkipTransition{}, fmt.Errorf("evaluate run %q: %w", status.RunID, err)
	}
	if decision.Kind != workflow.DecisionSelectStep {
		return runstore.StepSkipTransition{}, fmt.Errorf("run %q decision is %s; only a selected step can be skipped", status.RunID, decision.Kind)
	}
	if decision.Step != stepID {
		return runstore.StepSkipTransition{}, fmt.Errorf("step %q is not selected for run %q; selected step is %q", stepID, status.RunID, decision.Step)
	}
	step, ok := workflowConfig.Steps[stepID]
	if !ok {
		return runstore.StepSkipTransition{}, fmt.Errorf("step %q is not declared in workflow %q", stepID, workflowConfig.Name)
	}
	if !step.Skippable {
		return runstore.StepSkipTransition{}, fmt.Errorf("step %q is not skippable", stepID)
	}
	if !declaresSkipOutcome(step) {
		return runstore.StepSkipTransition{}, fmt.Errorf("step %q does not declare %s", stepID, config.SystemSkipPair)
	}
	skipDecision, err := workflow.Evaluate(workflowConfig, workflow.RunState{
		Status:       workflow.RunStatusRunning,
		SelectedStep: stepID,
		Retry:        decision.Retry,
		Outcome: &workflow.Outcome{
			Step:   stepID,
			Status: config.SystemSkipStatus,
			Result: config.SystemSkipResult,
		},
	})
	if err != nil {
		return runstore.StepSkipTransition{}, fmt.Errorf("evaluate skip transition for step %q: %w", stepID, err)
	}
	switch skipDecision.Kind {
	case workflow.DecisionSelectStep:
		return runstore.StepSkipTransition{
			State: workflow.RunStatusRunning,
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         skipDecision.Step,
				PreviousState: stepID,
				TriggerStatus: config.SystemSkipStatus,
				TriggerResult: config.SystemSkipResult,
			},
		}, nil
	case workflow.DecisionTerminal:
		return runstore.StepSkipTransition{
			State: skipDecision.RunStatus,
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         skipDecision.RunStatus,
				PreviousState: stepID,
				TriggerStatus: config.SystemSkipStatus,
				TriggerResult: config.SystemSkipResult,
			},
		}, nil
	case workflow.DecisionRetryStep, workflow.DecisionWaitActiveAttempt:
		return runstore.StepSkipTransition{}, fmt.Errorf("step %q %s transition produced %s; skip cannot be retried or wait", stepID, config.SystemSkipPair, skipDecision.Kind)
	}
	return runstore.StepSkipTransition{}, fmt.Errorf("step %q %s transition produced %s; skip cannot be retried or wait", stepID, config.SystemSkipPair, skipDecision.Kind)
}

func declaresSkipOutcome(step config.Step) bool {
	if slices.Contains(step.AllowedResults[config.SystemSkipStatus], config.SystemSkipResult) {
		_, hasTransition := step.On[config.SystemSkipPair]
		return hasTransition
	}
	return false
}
