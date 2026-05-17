package launcher

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/loopcap"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/workflow"

	"go.uber.org/zap"
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
	Logger   *zap.Logger
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
		return Result{}, stableerr.New("context is required")
	}

	if opts.Root == "" {
		return Result{}, stableerr.New("project root is required")
	}

	if opts.RunID == "" {
		return Result{}, stableerr.New("run id is required")
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("launch next: %w", err)
	}

	loaded, err := loadLaunchContext(ctx, opts.Root, opts.RunID)
	if err != nil {
		return Result{}, err
	}

	if err := enforceWorkerSandboxGuard(loaded.Project.Root, loaded.Project.Config.Sandbox); err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("launch next: %w", err)
	}

	latestOutcome, hasOutcome := runstore.LatestConsumableOutcome(loaded.Run.Status)
	state := runstate.WorkflowState(loaded.Run.Status)

	decision, err := workflow.Evaluate(loaded.Workflow, state)
	if err != nil {
		return Result{}, fmt.Errorf("evaluate run %q: %w", opts.RunID, err)
	}

	if result, handled, err := handleNonLaunchableDecision(ctx, opts, loaded, decision, latestOutcome, hasOutcome); handled || err != nil {
		return result, err
	}

	step := loaded.Workflow.Steps[decision.Step]
	at := normalizeTime(opts.Time)

	attemptID, err := newAttemptID(at, decision.Step)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("launch next: %w", err)
	}

	routing := startRoutingForDecision(decision, latestOutcome, hasOutcome)
	workflowOutcome, hasWorkflowOutcome := workflowEntryOutcome(loaded.Run.Status, latestOutcome, hasOutcome)
	workflowEntry := workflowStateEntryForDecision(decision, workflowOutcome, hasWorkflowOutcome)
	capDecision := loopcap.Evaluate(loaded.Workflow.Name, loaded.Workflow.LoopCaps, loaded.Run.Status, decision, workflowOutcome, hasWorkflowOutcome)

	consumeLoopCapOverride, result, handled, err := handleLaunchLoopHardCap(ctx, opts, loaded, capDecision, latestOutcome, at)
	if handled || err != nil {
		return result, err
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
		return Result{}, fmt.Errorf("launch next: %w", err)
	}

	var softCap *runstore.WorkflowLoopSoftCap

	if capDecision.Kind == loopcap.DecisionSoft {
		loopCap := capDecision.SoftCap()
		if _, _, err := loaded.Store.RecordWorkflowLoopSoftCapContext(ctx, opts.RunID, loopCap, at); err != nil {
			return Result{}, fmt.Errorf("launch next: %w", err)
		}

		softCap = &loopCap
	}

	progressDisplay, err := startLiveProgress(ctx, opts, attempt)
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(ctx, loaded.Store, opts.RunID, attempt.AttemptID, exitStateStartFailed, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResult(opts.Stdout, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}

	defer func() {
		_ = progressDisplay.Close()
	}()

	if err := ctx.Err(); err != nil {
		finished, finishErr := finishProcessErrorAttempt(ctx, loaded.Store, opts.RunID, attempt.AttemptID, exitStateCanceled, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResultWithProgress(opts.Stdout, progressDisplay, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}

	if step.EffectiveKind() == config.StepKindCommand || step.EffectiveKind() == config.StepKindScript {
		return launchDeterministicStep(ctx, loaded, opts, attempt, step, at, softCap, progressDisplay)
	}

	return launchAgentStep(ctx, loaded, opts, attempt, at, softCap, progressDisplay)
}

func handleNonLaunchableDecision(ctx context.Context, opts Options, loaded runcontext.Context, decision workflow.Decision, latestOutcome runstore.Attempt, hasOutcome bool) (Result, bool, error) {
	if decision.Kind == workflow.DecisionWaitActiveAttempt {
		result, err := recoverOrRefuseActiveAttempt(ctx, loaded.Store, loaded.Run, loggerOrNop(opts.Logger))
		if err == nil {
			printLaunchResult(opts.Stdout, result)
		}

		return result, true, err
	}

	if decision.Kind == workflow.DecisionTerminal && hasOutcome && loaded.Run.Status.State == workflow.RunStatusRunning {
		if _, _, err := loaded.Store.UpdateStatusContext(ctx, opts.RunID, runstore.StatusUpdate{
			State: decision.RunStatus,
			Time:  normalizeTime(opts.Time),
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         decision.RunStatus,
				PreviousState: latestOutcome.StepID,
				TriggerStatus: latestOutcome.Status,
				TriggerResult: latestOutcome.Result,
			},
		}); err != nil {
			return Result{}, true, fmt.Errorf("launch next: %w", err)
		}

		err := stableerr.Errorf("run %q has no launchable worker; outcome %s/%s transitioned to %s", opts.RunID, latestOutcome.Status, latestOutcome.Result, decision.RunStatus)

		return Result{RunID: opts.RunID, Attempt: latestOutcome}, true, err
	}

	if decision.Kind != workflow.DecisionSelectStep && decision.Kind != workflow.DecisionRetryStep {
		return Result{}, true, stableerr.Errorf("run %q has no launchable worker; decision is %s", opts.RunID, decision.Kind)
	}

	return Result{}, false, nil
}

func handleLaunchLoopHardCap(ctx context.Context, opts Options, loaded runcontext.Context, capDecision loopcap.Decision, latestOutcome runstore.Attempt, at time.Time) (*runstore.WorkflowLoopHardCapOverride, Result, bool, error) {
	if capDecision.Kind != loopcap.DecisionHard {
		return nil, Result{}, false, nil
	}

	if override := loaded.Run.Status.WorkflowLoop.PendingHardCapOverride; workflowLoopHardCapOverrideMatches(override, capDecision) {
		return override, Result{}, false, nil
	}

	status, _, err := loaded.Store.BlockWorkflowLoopHardCapContext(ctx, opts.RunID, capDecision.HardCap(), at)
	if err != nil {
		return nil, Result{}, true, fmt.Errorf("launch next: %w", err)
	}

	err = stableerr.Errorf("run %q workflow loop hard cap reached for state %q: current count %d, prospective count %d, hard cap %d; transitioned to %s with reason %s", opts.RunID, capDecision.State, capDecision.CurrentCount, capDecision.ProspectiveCount, capDecision.Hard, status.State, runstore.WorkflowLoopHardCapReason)

	return nil, Result{RunID: opts.RunID, Attempt: latestOutcome}, true, err
}

func launchDeterministicStep(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, step config.Step, at time.Time, softCap *runstore.WorkflowLoopSoftCap, progressDisplay *liveProgressDisplay) (Result, error) {
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

func launchAgentStep(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, at time.Time, softCap *runstore.WorkflowLoopSoftCap, progressDisplay *liveProgressDisplay) (Result, error) {
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

		finished, finishErr := finishProcessErrorAttempt(ctx, loaded.Store, opts.RunID, attempt.AttemptID, exitState, runstore.ArtifactRef{}, at, err)

		return terminalLaunchResultWithProgress(opts.Stdout, progressDisplay, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}

	attempt, _, err = loaded.Store.RecordAttemptPromptContext(ctx, opts.RunID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: prompt.Ref,
		Time:      at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(ctx, loaded.Store, opts.RunID, attempt.AttemptID, exitStatePromptRecordFail, runstore.ArtifactRef{}, at, err)
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
