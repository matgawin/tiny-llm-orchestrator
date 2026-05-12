// Package runconfigrefresh implements explicit adoption of live .orc config for an existing run.
package runconfigrefresh

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/vcs"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	defaultSource = "cli"
	hashAlgorithm = "sha256"
)

// Options describes an explicit config refresh request.
type Options struct {
	Root   string
	RunID  string
	Source string
	Env    []string
	Time   time.Time
}

// Result describes the committed refresh.
type Result struct {
	RunID                 string
	OldVersion            int
	OldVersionDir         string
	NewVersion            int
	NewVersionDir         string
	ManifestHashAlgorithm string
	ManifestHash          string
	Source                string
	Event                 runstore.Event
}

// Refresh loads live .orc config, validates compatibility, and publishes the next run snapshot.
func Refresh(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if opts.Root == "" {
		return Result{}, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return Result{}, errors.New("run id is required")
	}
	source := opts.Source
	if source == "" {
		source = defaultSource
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	project, err := config.Load(opts.Root)
	if err != nil {
		return Result{}, fmt.Errorf("load live .orc config: %w", err)
	}
	store, err := runstore.Open(opts.Root)
	if err != nil {
		return Result{}, err
	}
	run, err := store.Load(opts.RunID)
	if err != nil {
		return Result{}, err
	}
	current, err := configsnapshot.LoadCurrent(run)
	if err != nil {
		return Result{}, err
	}
	vcsSnapshot, err := vcs.InspectRefresh(ctx, vcs.Options{Root: opts.Root, Env: opts.Env, Time: opts.Time})
	if err != nil {
		return Result{}, fmt.Errorf("inspect VCS for config refresh: %w", err)
	}
	snapshot, err := configsnapshot.BuildRefresh(project, run.Status.Workflow, current.Version+1, source, vcsSnapshot, opts.Time)
	if err != nil {
		return Result{}, err
	}
	manifestHash := configsnapshot.ManifestHash(snapshot.Manifest)
	refresh, err := store.RefreshConfigSnapshot(opts.RunID, runstore.RefreshConfigSnapshotRequest{
		Snapshot:              snapshot,
		Source:                source,
		ManifestHashAlgorithm: hashAlgorithm,
		ManifestHash:          manifestHash,
		Time:                  opts.Time,
	}, func(lockedRun *runstore.Run, lockedCurrent runstore.CurrentConfigSnapshot) error {
		if lockedCurrent.Version != current.Version || lockedCurrent.VersionDir != current.VersionDir {
			return fmt.Errorf("run %q config snapshot changed from %s to %s during refresh; retry refresh-config", opts.RunID, current.VersionDir, lockedCurrent.VersionDir)
		}
		lockedOld, err := configsnapshot.LoadCurrent(lockedRun)
		if err != nil {
			return err
		}
		return validateCompatibility(lockedRun, lockedOld.Project, project)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		RunID:                 opts.RunID,
		OldVersion:            refresh.OldVersion,
		OldVersionDir:         refresh.OldVersionDir,
		NewVersion:            refresh.NewVersion,
		NewVersionDir:         refresh.NewVersionDir,
		ManifestHashAlgorithm: refresh.ManifestHashAlgorithm,
		ManifestHash:          refresh.ManifestHash,
		Source:                refresh.Source,
		Event:                 refresh.Event,
	}, nil
}

func validateCompatibility(run *runstore.Run, oldProject, newProject *config.Project) error {
	if run.Status.ActiveAttempt != nil {
		return fmt.Errorf("run %q has active attempt %q; cannot refresh config", run.ID, run.Status.ActiveAttempt.AttemptID)
	}
	oldWorkflow, ok := oldProject.Workflows[run.Status.Workflow]
	if !ok {
		return fmt.Errorf("run %q old snapshot workflow %q is missing", run.ID, run.Status.Workflow)
	}
	newWorkflow, ok := newProject.Workflows[run.Status.Workflow]
	if !ok {
		return fmt.Errorf("run %q live .orc config is missing workflow %q", run.ID, run.Status.Workflow)
	}
	if oldWorkflow.Name != newWorkflow.Name {
		return fmt.Errorf("run %q workflow name changed from %q to %q", run.ID, oldWorkflow.Name, newWorkflow.Name)
	}
	if err := validateReferencedSteps(run.Status, newWorkflow); err != nil {
		return fmt.Errorf("run %q config refresh is incompatible: %w", run.ID, err)
	}
	state := runstate.WorkflowState(run.Status)
	if err := validatePendingAllowedPairs(state, oldWorkflow, newWorkflow); err != nil {
		return fmt.Errorf("run %q config refresh is incompatible: %w", run.ID, err)
	}
	if _, err := workflow.Evaluate(oldWorkflow, state); err != nil {
		return fmt.Errorf("run %q current state is not evaluable against old snapshot workflow: %w", run.ID, err)
	}
	if _, err := workflow.Evaluate(newWorkflow, state); err != nil {
		return fmt.Errorf("run %q current state is not evaluable against live workflow: %w", run.ID, err)
	}
	return nil
}

func validatePendingAllowedPairs(state workflow.RunState, oldWorkflow, newWorkflow config.Workflow) error {
	if state.Outcome != nil {
		return nil
	}
	selectedStep := state.SelectedStep
	if selectedStep == "" {
		selectedStep = oldWorkflow.Start
	}
	oldStep, ok := oldWorkflow.Steps[selectedStep]
	if !ok {
		return nil
	}
	newStep, ok := newWorkflow.Steps[selectedStep]
	if !ok {
		return nil
	}
	for _, status := range sortedKeys(oldStep.AllowedResults) {
		for _, result := range oldStep.AllowedResults[status] {
			if !slices.Contains(newStep.AllowedResults[status], result) {
				return fmt.Errorf("allowed result pair %q is no longer declared for selected step %q", status+"/"+result, selectedStep)
			}
		}
	}
	for pair := range state.Retry.Counts {
		status, result, ok := strings.Cut(pair, "/")
		if !ok || status == "" || result == "" {
			return fmt.Errorf("retry lineage pair %q is invalid", pair)
		}
		if !slices.Contains(newStep.AllowedResults[status], result) {
			return fmt.Errorf("retry lineage pair %q is not declared in allowed_results for selected step %q", pair, selectedStep)
		}
	}
	return nil
}

func validateReferencedSteps(status runstore.Status, workflowConfig config.Workflow) error {
	steps := map[string]string{}
	add := func(stepID, source string) {
		if stepID == "" || terminalStatus(stepID) {
			return
		}
		steps[stepID] = source
	}
	add(workflowConfig.Start, "workflow start")
	if state := status.State; state == workflow.RunStatusRunning {
		evalState := runstate.WorkflowState(status)
		add(evalState.SelectedStep, "current selected step")
		if evalState.Outcome != nil {
			add(evalState.Outcome.Step, "current outcome step")
		}
	} else {
		add(status.State, "current status")
	}
	if status.RetryLineage != nil {
		add(status.RetryLineage.StepID, "retry lineage")
	}
	if status.Continued != nil {
		add(status.Continued.ResolvedStepID, "continued resolved step")
	}
	for _, attempt := range status.Attempts {
		add(attempt.StepID, "attempt history")
	}
	for _, skipped := range status.SkippedSteps {
		add(skipped.StepID, "skipped step")
	}
	for _, entry := range status.WorkflowLoop.Entries {
		add(entry.State, "workflow loop entry")
		add(entry.PreviousState, "workflow loop previous state")
	}
	for state := range status.WorkflowLoop.Counts {
		add(state, "workflow loop count")
	}
	for _, state := range status.WorkflowLoop.RepeatedStates {
		add(state, "workflow loop repeated state")
	}
	for _, warning := range status.WorkflowLoop.SoftCapWarnings {
		add(warning.State, "workflow loop soft cap")
		add(warning.PreviousState, "workflow loop soft cap previous state")
	}
	if block := status.WorkflowLoop.HardCapBlock; block != nil {
		add(block.BlockedState, "workflow loop hard cap block")
		add(block.PreviousState, "workflow loop hard cap previous state")
	}
	if override := status.WorkflowLoop.PendingHardCapOverride; override != nil {
		add(override.TargetState, "workflow loop hard cap override")
	}

	for _, stepID := range sortedKeys(steps) {
		if _, ok := workflowConfig.Steps[stepID]; !ok {
			return fmt.Errorf("%s %q is not declared in live workflow", steps[stepID], stepID)
		}
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := slices.Collect(maps.Keys(values))
	slices.Sort(keys)
	return keys
}

func terminalStatus(status string) bool {
	switch status {
	case workflow.RunStatusReadyForHuman, workflow.RunStatusBlockedForHuman, workflow.RunStatusCancelled:
		return true
	default:
		return false
	}
}
