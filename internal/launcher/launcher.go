package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/loopcap"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	resultCommandPassed = "passed"
	resultCommandFailed = "failed"
	resultMissingReport = runstore.AttemptResultMissingReport
	resultProcessError  = runstore.AttemptResultProcessError
	resultTimeout       = runstore.AttemptResultTimeout

	exitStateUnknown          = "unknown"
	exitStateCanceled         = "canceled"
	exitStateInvalidCommand   = "invalid_command"
	exitStatePromptRenderFail = "prompt_render_failed"
	exitStatePromptRecordFail = "prompt_record_failed"
	exitStateLogStartFailed   = "log_start_failed"
	exitStateStartFailed      = "start_failed"
	exitStateTimeout          = "timeout"
	exitStateExited           = "exited"
)

func init() {
	if len(os.Args) >= 3 && os.Args[1] == execHelperArg {
		os.Exit(runExecHelper(os.Args[2], os.Args[3:]))
	}
}

// Options describes a worker launch request.
type Options struct {
	Root     string
	RunID    string
	Command  []string
	Env      []string
	Time     time.Time
	Stdout   io.Writer
	Progress io.Writer
}

// Result describes the persisted launch outcome.
type Result struct {
	RunID     string
	Attempt   runstore.Attempt
	Prompt    runstore.ArtifactRef
	Log       runstore.ArtifactRef
	Launched  bool
	Recovered bool
	SoftCap   *runstore.WorkflowLoopSoftCap
}

// LaunchNext launches the workflow-selected worker process for a run.
func LaunchNext(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if opts.Root == "" {
		return Result{}, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return Result{}, errors.New("run id is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	loaded, err := loadLaunchContext(opts.Root, opts.RunID)
	if err != nil {
		return Result{}, err
	}
	if err := enforceWorkerSandboxGuard(loaded.Project.Root, loaded.Project.Config.Sandbox); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	latestOutcome, hasOutcome := runstore.LatestConsumableOutcome(loaded.Run.Status)
	state := runstate.WorkflowState(loaded.Run.Status)
	decision, err := workflow.Evaluate(loaded.Workflow, state)
	if err != nil {
		return Result{}, fmt.Errorf("evaluate run %q: %w", opts.RunID, err)
	}
	if decision.Kind == workflow.DecisionWaitActiveAttempt {
		result, err := recoverOrRefuseActiveAttempt(loaded.Store, loaded.Run)
		if err == nil {
			printLaunchResult(opts.Stdout, result)
		}
		return result, err
	}
	if decision.Kind == workflow.DecisionTerminal && hasOutcome && loaded.Run.Status.State == workflow.RunStatusRunning {
		if _, _, err := loaded.Store.UpdateStatus(opts.RunID, runstore.StatusUpdate{
			State: decision.RunStatus,
			Time:  normalizeTime(opts.Time),
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         decision.RunStatus,
				PreviousState: latestOutcome.StepID,
				TriggerStatus: latestOutcome.Status,
				TriggerResult: latestOutcome.Result,
			},
		}); err != nil {
			return Result{}, err
		}
		return Result{RunID: opts.RunID, Attempt: latestOutcome}, fmt.Errorf("run %q has no launchable worker; outcome %s/%s transitioned to %s", opts.RunID, latestOutcome.Status, latestOutcome.Result, decision.RunStatus)
	}
	if decision.Kind != workflow.DecisionSelectStep && decision.Kind != workflow.DecisionRetryStep {
		return Result{}, fmt.Errorf("run %q has no launchable worker; decision is %s", opts.RunID, decision.Kind)
	}
	step := loaded.Workflow.Steps[decision.Step]
	at := normalizeTime(opts.Time)
	attemptID, err := newAttemptID(at, decision.Step)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	routing := startRoutingForDecision(decision, latestOutcome, hasOutcome)
	workflowOutcome, hasWorkflowOutcome := workflowEntryOutcome(loaded.Run.Status, latestOutcome, hasOutcome)
	workflowEntry := workflowStateEntryForDecision(decision, workflowOutcome, hasWorkflowOutcome)
	var consumeLoopCapOverride *runstore.WorkflowLoopHardCapOverride
	capDecision := loopcap.Evaluate(loaded.Workflow.Name, loaded.Workflow.LoopCaps, loaded.Run.Status, decision, workflowOutcome, hasWorkflowOutcome)
	if capDecision.Kind == loopcap.DecisionHard {
		if override := loaded.Run.Status.WorkflowLoop.PendingHardCapOverride; workflowLoopHardCapOverrideMatches(override, capDecision) {
			consumeLoopCapOverride = override
		} else {
			status, _, err := loaded.Store.BlockWorkflowLoopHardCap(opts.RunID, capDecision.HardCap(), at)
			if err != nil {
				return Result{}, err
			}
			return Result{RunID: opts.RunID, Attempt: latestOutcome}, fmt.Errorf("run %q workflow loop hard cap reached for state %q: current count %d, prospective count %d, hard cap %d; transitioned to %s with reason %s", opts.RunID, capDecision.State, capDecision.CurrentCount, capDecision.ProspectiveCount, capDecision.Hard, status.State, runstore.WorkflowLoopHardCapReason)
		}
	}
	attempt, _, err := loaded.Store.StartAttemptContext(ctx, opts.RunID, runstore.StartAttemptRequest{
		StepID:                             decision.Step,
		AgentID:                            step.EffectiveAgentID(),
		AttemptID:                          attemptID,
		ConfigSnapshotVersion:              loaded.ConfigSnapshotVersion,
		Timeout:                            loaded.Workflow.Defaults.Timeout.Duration,
		ReportExitGrace:                    loaded.Workflow.Defaults.ReportExitGrace.Duration,
		Time:                               at,
		ConsumeAttemptID:                   routing.consumeAttemptID,
		RetryLineage:                       routing.retryLineage,
		SupersedeReason:                    routing.supersedeReason,
		WorkflowStateEntry:                 workflowEntry,
		ConsumeWorkflowLoopHardCapOverride: consumeLoopCapOverride,
	})
	if err != nil {
		return Result{}, err
	}
	var softCap *runstore.WorkflowLoopSoftCap
	if capDecision.Kind == loopcap.DecisionSoft {
		loopCap := capDecision.SoftCap()
		if _, _, err := loaded.Store.RecordWorkflowLoopSoftCap(opts.RunID, loopCap, at); err != nil {
			return Result{}, err
		}
		softCap = &loopCap
	}
	progressDisplay, err := startLiveProgress(opts, attempt)
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateStartFailed, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResult(opts.Stdout, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}
	defer func() {
		_ = progressDisplay.Close()
	}()
	if err := ctx.Err(); err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateCanceled, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResultWithProgress(opts.Stdout, progressDisplay, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}
	if step.EffectiveKind() == config.StepKindCommand || step.EffectiveKind() == config.StepKindScript {
		attempt, stdoutRef, stderrRef, launched, err := runDeterministicStep(ctx, loaded, opts, attempt, step, at, progressDisplay.Env())
		result := Result{RunID: opts.RunID, Attempt: attempt, Log: stderrRef, Launched: launched, SoftCap: softCap}
		if stdoutRef.Path != "" {
			result.Log = stdoutRef
		}
		_ = progressDisplay.Close()
		if err != nil {
			if result.Attempt.AttemptID != "" {
				printLaunchResult(opts.Stdout, result)
			}
			return result, err
		}
		printLaunchResult(opts.Stdout, result)
		return result, nil
	}

	prompt, err := promptrender.Render(ctx, promptrender.Options{
		Root:      opts.Root,
		RunID:     opts.RunID,
		StepID:    attempt.StepID,
		AgentID:   attempt.AgentID,
		AttemptID: attempt.AttemptID,
		Time:      at,
	})
	if err != nil {
		exitState := exitStatePromptRenderFail
		if isContextError(err) {
			exitState = exitStateCanceled
		}
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitState, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResultWithProgress(opts.Stdout, progressDisplay, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}
	attempt, _, err = loaded.Store.RecordAttemptPrompt(opts.RunID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: prompt.Ref,
		Time:      at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStatePromptRecordFail, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResultWithProgress(opts.Stdout, progressDisplay, Result{RunID: opts.RunID, Attempt: finished, Prompt: prompt.Ref, SoftCap: softCap}, finishErr)
	}

	attempt, logRef, launched, err := runProcess(ctx, loaded, opts, attempt, prompt, at, progressDisplay.Env())
	result := Result{RunID: opts.RunID, Attempt: attempt, Prompt: prompt.Ref, Log: logRef, Launched: launched, SoftCap: softCap}
	_ = progressDisplay.Close()
	if err != nil {
		if result.Attempt.AttemptID != "" {
			printLaunchResult(opts.Stdout, result)
		}
		return result, err
	}
	printLaunchResult(opts.Stdout, result)
	return result, nil
}
