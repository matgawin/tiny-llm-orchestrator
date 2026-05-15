package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/loopcap"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"

	"go.uber.org/zap"
)

const DefaultAdvanceMaxSteps = 20

const (
	StopReasonReadyForHuman       = "ready_for_human"
	StopReasonBlockedForHuman     = "blocked_for_human"
	StopReasonWorkerBlocked       = "worker_blocked"
	StopReasonWorkerFailed        = "worker_failed"
	StopReasonLoopHardCap         = "loop_hard_cap"
	StopReasonLoopSoftCap         = "loop_soft_cap"
	StopReasonActiveAttemptExists = "active_attempt_exists"
	StopReasonMaxStepsReached     = "max_steps_reached"
	StopReasonError               = "error"
)

// AdvanceOptions describes a conservative run advancement request.
type AdvanceOptions struct {
	Root     string
	RunID    string
	Command  []string
	Env      []string
	Time     time.Time
	Stdout   io.Writer
	Progress io.Writer
	Logger   *zap.Logger
	MaxSteps int
	Once     bool
}

// AdvanceResult describes the final state reached by Advance.
type AdvanceResult struct {
	RunID            string
	LaunchedAttempts []AdvanceAttempt
	FinalStatus      string
	FinalDecision    string
	StopReason       string
	ExitCode         int
	Error            string
}

// AdvanceAttempt describes one worker attempt launched by Advance.
type AdvanceAttempt struct {
	StepID    string `json:"step_id"`
	AgentID   string `json:"agent_id"`
	AttemptID string `json:"attempt_id"`
	Status    string `json:"status"`
	Result    string `json:"result"`
	State     string `json:"state,omitempty"`
}

// Advance evaluates and launches workflow-selected workers until a conservative
// stop condition is reached.
func Advance(ctx context.Context, opts AdvanceOptions) (AdvanceResult, error) {
	if ctx == nil {
		return advanceError(opts.RunID, "", "", StopReasonError, 1, errors.New("context is required"))
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = DefaultAdvanceMaxSteps
	}
	if opts.MaxSteps < 1 {
		return advanceError(opts.RunID, "", "", StopReasonError, 1, fmt.Errorf("max steps must be positive, got %d", opts.MaxSteps))
	}
	if opts.Root == "" {
		return advanceError(opts.RunID, "", "", StopReasonError, 1, errors.New("project root is required"))
	}
	if opts.RunID == "" {
		return advanceError("", "", "", StopReasonError, 1, errors.New("run id is required"))
	}
	if err := ctx.Err(); err != nil {
		return advanceError(opts.RunID, "", "", StopReasonError, 1, err)
	}

	result := AdvanceResult{RunID: opts.RunID, ExitCode: 0}
	for {
		eval, err := evaluateAdvance(ctx, opts.Root, opts.RunID)
		if err != nil {
			return result.withError(StopReasonError, 1, err), err
		}
		result.FinalStatus = eval.status.State
		result.FinalDecision = string(eval.decision.Kind)

		switch eval.decision.Kind {
		case workflow.DecisionTerminal:
			if eval.decision.RunStatus == workflow.RunStatusReadyForHuman {
				if eval.hasOutcome && eval.status.State == workflow.RunStatusRunning {
					status, err := terminalizeAdvanceOutcome(ctx, eval, normalizeTime(opts.Time))
					if err != nil {
						return result.withError(StopReasonError, 1, err), err
					}
					result.FinalStatus = status.State
				}
				result.StopReason = StopReasonReadyForHuman
				result.ExitCode = 0
				return result, nil
			}
			if eval.decision.RunStatus == workflow.RunStatusBlockedForHuman {
				if eval.hasOutcome && eval.status.State == workflow.RunStatusRunning {
					status, err := terminalizeAdvanceOutcome(ctx, eval, normalizeTime(opts.Time))
					if err != nil {
						return result.withError(StopReasonError, 1, err), err
					}
					result.FinalStatus = status.State
				}
				result.StopReason = StopReasonBlockedForHuman
				result.ExitCode = 2
				return result, nil
			}
			result.StopReason = eval.decision.RunStatus
			return result, nil
		case workflow.DecisionWaitActiveAttempt:
			result.StopReason = StopReasonActiveAttemptExists
			result.ExitCode = 1
			return result, fmt.Errorf("run %q has an active attempt", opts.RunID)
		case workflow.DecisionSelectStep, workflow.DecisionRetryStep:
		default:
			err := fmt.Errorf("run %q has unsupported workflow decision %q", opts.RunID, eval.decision.Kind)
			return result.withError(StopReasonError, 1, err), err
		}

		if len(result.LaunchedAttempts) >= opts.MaxSteps {
			result.StopReason = StopReasonMaxStepsReached
			result.ExitCode = 0
			return result, nil
		}

		capDecision := loopcap.Evaluate(eval.workflowName, eval.loopCaps, eval.status, eval.decision, eval.workflowOutcome, eval.hasWorkflowOutcome)
		switch capDecision.Kind {
		case loopcap.DecisionNone:
		case loopcap.DecisionHard:
			launchResult, err := LaunchNext(ctx, launchOptions(opts))
			result.FinalStatus = workflow.RunStatusBlockedForHuman
			result.FinalDecision = string(eval.decision.Kind)
			result.StopReason = StopReasonLoopHardCap
			result.ExitCode = 2
			if launchResult.Attempt.AttemptID != "" && launchResult.Launched {
				result.LaunchedAttempts = append(result.LaunchedAttempts, advanceAttempt(launchResult.Attempt))
			}
			if err != nil {
				return result, err
			}
			return result, nil
		case loopcap.DecisionSoft:
			loopCap := capDecision.SoftCap()
			if _, _, err := eval.store.RecordWorkflowLoopSoftCapContext(ctx, opts.RunID, loopCap, normalizeTime(opts.Time)); err != nil {
				return result.withError(StopReasonError, 1, err), err
			}
			result.StopReason = StopReasonLoopSoftCap
			result.ExitCode = 2
			return result, nil
		}

		if opts.Progress != nil {
			_, _ = fmt.Fprintf(opts.Progress, "advancing run %s: launching %s (%s)\n", opts.RunID, eval.decision.Step, eval.decision.Kind)
		}
		launchResult, err := LaunchNext(ctx, launchOptions(opts))
		if launchResult.Attempt.AttemptID != "" && launchResult.Launched {
			result.LaunchedAttempts = append(result.LaunchedAttempts, advanceAttempt(launchResult.Attempt))
		}
		if err != nil {
			return result.withError(StopReasonError, 1, err), err
		}
		if launchResult.Attempt.Status == workflow.ReportStatusBlocked {
			eval, err := evaluateAdvance(ctx, opts.Root, opts.RunID)
			if err != nil {
				return result.withError(StopReasonError, 1, err), err
			}
			result.FinalStatus = eval.status.State
			result.FinalDecision = string(eval.decision.Kind)
			result.StopReason = StopReasonWorkerBlocked
			result.ExitCode = 2
			return result, nil
		}
		if launchResult.Attempt.Status == workflow.ReportStatusFailed {
			eval, err := evaluateAdvance(ctx, opts.Root, opts.RunID)
			if err != nil {
				return result.withError(StopReasonError, 1, err), err
			}
			stopErr := fmt.Errorf("worker attempt %s failed with %s/%s", launchResult.Attempt.AttemptID, launchResult.Attempt.Status, launchResult.Attempt.Result)
			result.FinalStatus = eval.status.State
			result.FinalDecision = string(eval.decision.Kind)
			return result.withError(StopReasonWorkerFailed, 1, stopErr), stopErr
		}
		if opts.Once {
			eval, err := evaluateAdvance(ctx, opts.Root, opts.RunID)
			if err != nil {
				return result.withError(StopReasonError, 1, err), err
			}
			result.FinalStatus = eval.status.State
			result.FinalDecision = string(eval.decision.Kind)
			result.StopReason = "once"
			result.ExitCode = 0
			return result, nil
		}
	}
}

type advanceEvaluation struct {
	store              *runstore.Store
	status             runstore.Status
	workflowName       string
	loopCaps           config.EffectiveLoopCaps
	decision           workflow.Decision
	latestOutcome      runstore.Attempt
	hasOutcome         bool
	workflowOutcome    runstore.Attempt
	hasWorkflowOutcome bool
}

func evaluateAdvance(ctx context.Context, root, runID string) (advanceEvaluation, error) {
	loaded, err := loadLaunchContext(ctx, root, runID)
	if err != nil {
		return advanceEvaluation{}, err
	}
	if err := enforceWorkerSandboxGuard(loaded.Project.Root, loaded.Project.Config.Sandbox); err != nil {
		return advanceEvaluation{}, err
	}
	state := runstate.WorkflowState(loaded.Run.Status)
	decision, err := workflow.Evaluate(loaded.Workflow, state)
	if err != nil {
		return advanceEvaluation{}, fmt.Errorf("evaluate run %q: %w", runID, err)
	}
	latestOutcome, hasOutcome := runstore.LatestConsumableOutcome(loaded.Run.Status)
	workflowOutcome, hasWorkflowOutcome := workflowEntryOutcome(loaded.Run.Status, latestOutcome, hasOutcome)
	return advanceEvaluation{
		store:              loaded.Store,
		status:             loaded.Run.Status,
		workflowName:       loaded.Workflow.Name,
		loopCaps:           loaded.Workflow.LoopCaps,
		decision:           decision,
		latestOutcome:      latestOutcome,
		hasOutcome:         hasOutcome,
		workflowOutcome:    workflowOutcome,
		hasWorkflowOutcome: hasWorkflowOutcome,
	}, nil
}

func terminalizeAdvanceOutcome(ctx context.Context, eval advanceEvaluation, at time.Time) (runstore.Status, error) {
	status, _, err := eval.store.UpdateStatusContext(ctx, eval.status.RunID, runstore.StatusUpdate{
		State: eval.decision.RunStatus,
		Time:  at,
		WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
			State:         eval.decision.RunStatus,
			PreviousState: eval.latestOutcome.StepID,
			TriggerStatus: eval.latestOutcome.Status,
			TriggerResult: eval.latestOutcome.Result,
		},
	})
	return status, err
}

func launchOptions(opts AdvanceOptions) Options {
	return Options{
		Root:     opts.Root,
		RunID:    opts.RunID,
		Command:  opts.Command,
		Env:      opts.Env,
		Time:     opts.Time,
		Stdout:   opts.Stdout,
		Progress: opts.Progress,
		Logger:   opts.Logger,
	}
}

func advanceAttempt(attempt runstore.Attempt) AdvanceAttempt {
	return AdvanceAttempt{
		StepID:    attempt.StepID,
		AgentID:   attempt.AgentID,
		AttemptID: attempt.AttemptID,
		Status:    attempt.Status,
		Result:    attempt.Result,
		State:     attempt.State,
	}
}

func advanceError(runID, status, decision, reason string, code int, err error) (AdvanceResult, error) {
	return AdvanceResult{RunID: runID, FinalStatus: status, FinalDecision: decision, StopReason: reason, ExitCode: code, Error: err.Error()}, err
}

func (r AdvanceResult) withError(reason string, code int, err error) AdvanceResult {
	r.StopReason = reason
	r.ExitCode = code
	r.Error = err.Error()
	return r
}
