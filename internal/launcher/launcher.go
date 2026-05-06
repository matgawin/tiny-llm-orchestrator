package launcher

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/loopcap"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	reportStatusFailed = "failed"
	reportStatusDone   = "done"

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

	warningKindPostReportGraceTerminated = "post_report_grace_terminated"
	warningKindPostReportProcessExit     = "post_report_process_exit"

	execHelperEnv = "ORC_LAUNCHER_EXEC_HELPER"
	execHelperArg = "__orc-launcher-exec-helper"
	execHelperFD  = uintptr(3)

	launchCleanupTimeout = 2 * time.Second
	failureTailLines     = 100
	failureTailBytes     = 12 * 1024
)

var defaultCommand = []string{"codex", "--ask-for-approval", "never", "exec", "--skip-git-repo-check", "-"}

func init() {
	if len(os.Args) >= 3 && os.Args[1] == execHelperArg {
		os.Exit(runExecHelper(os.Args[2], os.Args[3:]))
	}
}

// Options describes a worker launch request.
type Options struct {
	Root    string
	RunID   string
	Command []string
	Env     []string
	Time    time.Time
	Stdout  io.Writer
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
	workflowEntry := workflowStateEntryForDecision(decision, latestOutcome, hasOutcome)
	var consumeLoopCapOverride *runstore.WorkflowLoopHardCapOverride
	capDecision := loopcap.Evaluate(loaded.Workflow.Name, loaded.Workflow.LoopCaps, loaded.Run.Status, decision, latestOutcome, hasOutcome)
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
	if err := ctx.Err(); err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateCanceled, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResult(opts.Stdout, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}
	if step.EffectiveKind() == config.StepKindCommand || step.EffectiveKind() == config.StepKindScript {
		attempt, stdoutRef, stderrRef, launched, err := runDeterministicStep(ctx, loaded, opts, attempt, step, at)
		result := Result{RunID: opts.RunID, Attempt: attempt, Log: stderrRef, Launched: launched, SoftCap: softCap}
		if stdoutRef.Path != "" {
			result.Log = stdoutRef
		}
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
		return terminalLaunchResult(opts.Stdout, Result{RunID: opts.RunID, Attempt: finished, SoftCap: softCap}, finishErr)
	}
	attempt, _, err = loaded.Store.RecordAttemptPrompt(opts.RunID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: prompt.Ref,
		Time:      at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStatePromptRecordFail, runstore.ArtifactRef{}, at, err)
		return terminalLaunchResult(opts.Stdout, Result{RunID: opts.RunID, Attempt: finished, Prompt: prompt.Ref, SoftCap: softCap}, finishErr)
	}

	attempt, logRef, launched, err := runProcess(ctx, loaded, opts, attempt, prompt.Content, at)
	result := Result{RunID: opts.RunID, Attempt: attempt, Prompt: prompt.Ref, Log: logRef, Launched: launched, SoftCap: softCap}
	if err != nil {
		if result.Attempt.AttemptID != "" {
			printLaunchResult(opts.Stdout, result)
		}
		return result, err
	}
	printLaunchResult(opts.Stdout, result)
	return result, nil
}

func finishAttemptWithCleanupContext(store *runstore.Store, runID string, req runstore.FinishAttemptRequest) (runstore.Attempt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchCleanupTimeout)
	defer cancel()
	finished, _, err := store.FinishAttemptContext(ctx, runID, req)
	return finished, err
}

func finishProcessErrorAttempt(store *runstore.Store, runID, attemptID, exitState string, logRef runstore.ArtifactRef, at time.Time, causes ...error) (runstore.Attempt, error) {
	finished, finishErr := finishAttemptWithCleanupContext(store, runID, runstore.FinishAttemptRequest{
		AttemptID: attemptID,
		State:     runstore.AttemptStateProcessError,
		Status:    reportStatusFailed,
		Result:    resultProcessError,
		ExitState: exitState,
		LogRef:    refPtr(logRef),
		Time:      at,
	})
	return finished, errors.Join(append(causes, finishErr)...)
}

func terminalLaunchResult(stdout io.Writer, result Result, err error) (Result, error) {
	if result.Attempt.AttemptID != "" {
		printLaunchResult(stdout, result)
	}
	return result, err
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func loadLaunchContext(root, runID string) (runcontext.Context, error) {
	return runcontext.Load(root, runID)
}

type startRouting struct {
	consumeAttemptID string
	retryLineage     *runstore.RetryLineage
	supersedeReason  string
}

func startRoutingForDecision(decision workflow.Decision, attempt runstore.Attempt, ok bool) startRouting {
	if !ok {
		return startRouting{}
	}
	routing := startRouting{consumeAttemptID: attempt.AttemptID}
	if decision.Kind != workflow.DecisionRetryStep {
		return routing
	}
	routing.retryLineage = &runstore.RetryLineage{
		StepID: decision.Retry.Step,
		Counts: maps.Clone(decision.Retry.Counts),
	}
	routing.supersedeReason = attempt.Status + "/" + attempt.Result
	return routing
}

func workflowStateEntryForDecision(decision workflow.Decision, attempt runstore.Attempt, ok bool) runstore.WorkflowStateEntryRequest {
	if decision.Kind != workflow.DecisionSelectStep || !ok {
		return runstore.WorkflowStateEntryRequest{}
	}
	return runstore.WorkflowStateEntryRequest{
		State:         decision.Step,
		PreviousState: attempt.StepID,
		TriggerStatus: attempt.Status,
		TriggerResult: attempt.Result,
	}
}

func workflowLoopHardCapOverrideMatches(override *runstore.WorkflowLoopHardCapOverride, decision loopcap.Decision) bool {
	if override == nil {
		return false
	}
	return override.Workflow == decision.Workflow &&
		override.TargetState == decision.State &&
		override.CountBeforeOverride == decision.CurrentCount &&
		override.CountAfterOverride == decision.ProspectiveCount &&
		override.Soft == decision.Soft &&
		override.Hard == decision.Hard &&
		override.Reason == runstore.WorkflowLoopHardCapReason
}

func runProcess(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, prompt []byte, at time.Time) (runstore.Attempt, runstore.ArtifactRef, bool, error) {
	runner := workerRunner{
		ctx:     ctx,
		loaded:  loaded,
		opts:    opts,
		attempt: attempt,
		prompt:  prompt,
		at:      at,
	}
	return runner.run()
}

func runDeterministicStep(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, step config.Step, at time.Time) (runstore.Attempt, runstore.ArtifactRef, runstore.ArtifactRef, bool, error) {
	if err := ctx.Err(); err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateCanceled, runstore.ArtifactRef{}, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	promptRef, err := loaded.Store.WriteArtifact(opts.RunID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    attempt.StepID + "-system",
		Content: deterministicPromptContent(loaded.Workflow.Name, attempt, step),
		Time:    at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStatePromptRecordFail, runstore.ArtifactRef{}, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	attempt, _, err = loaded.Store.RecordAttemptPrompt(opts.RunID, runstore.AttemptPromptRequest{
		AttemptID: attempt.AttemptID,
		PromptRef: promptRef,
		Time:      at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStatePromptRecordFail, runstore.ArtifactRef{}, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	processLogRef, err := loaded.Store.WriteArtifact(opts.RunID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    attempt.StepID + "-" + attempt.AttemptID + "-process",
		Content: nil,
		Time:    at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateLogStartFailed, runstore.ArtifactRef{}, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	attempt, _, err = loaded.Store.RecordAttemptLog(opts.RunID, runstore.AttemptLogRequest{
		AttemptID: attempt.AttemptID,
		LogRef:    processLogRef,
		Time:      at,
	})
	if err != nil {
		finished, finishErr := finishProcessErrorAttempt(loaded.Store, opts.RunID, attempt.AttemptID, exitStateLogStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	command, cwd, env, err := deterministicExecSpec(loaded.Project.Root, step)
	if err != nil {
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, nil, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	execPath, err := deterministicCommandPath(command, env, cwd)
	if err != nil {
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	cmd := exec.Command(execPath, command[1:]...) // #nosec G204 -- workflow command argv is the configured v1 command-step contract.
	cmd.Args = append([]string(nil), command...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutFile, stdoutPath, err := openDeterministicOutputFile(attempt, "stdout")
	if err != nil {
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	defer removeDeterministicOutput(stdoutFile, stdoutPath)
	stderrFile, stderrPath, err := openDeterministicOutputFile(attempt, "stderr")
	if err != nil {
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	defer removeDeterministicOutput(stderrFile, stderrPath)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, false, finishErr
	}
	processStartTime, err := processStartIdentity(cmd.Process.Pid)
	if err != nil {
		terminateProcessGroup(cmd.Process.Pid)
		_, _ = cmd.Process.Wait()
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, true, finishErr
	}
	attempt, _, err = loaded.Store.RecordAttemptProcessContext(ctx, opts.RunID, runstore.AttemptProcessRequest{
		AttemptID:        attempt.AttemptID,
		PID:              cmd.Process.Pid,
		ProcessStartTime: processStartTime,
		Time:             at,
	})
	if err != nil {
		terminateProcessGroup(cmd.Process.Pid)
		_, _ = cmd.Process.Wait()
		finished, finishErr := recordDeterministicProcessError(loaded.Store, opts.RunID, attempt, step, command, exitStateStartFailed, processLogRef, at, err)
		return finished, runstore.ArtifactRef{}, runstore.ArtifactRef{}, true, finishErr
	}
	waitResult := waitForDeterministicProcess(ctx, loaded.Workflow.Defaults.Timeout.Duration, cmd)
	stdoutCloseErr := stdoutFile.Close()
	stderrCloseErr := stderrFile.Close()
	stdoutTail, stdoutTailErr := boundedLogTailFromFile(stdoutPath)
	stderrTail, stderrTailErr := boundedLogTailFromFile(stderrPath)
	stdoutRef, stdoutErr := loaded.Store.WriteArtifactFromFile(opts.RunID, runstore.Artifact{
		Kind: runstore.KindLog,
		Name: attempt.StepID + "-" + attempt.AttemptID + "-stdout",
		Time: at,
	}, stdoutPath)
	stderrRef, stderrErr := loaded.Store.WriteArtifactFromFile(opts.RunID, runstore.Artifact{
		Kind: runstore.KindLog,
		Name: attempt.StepID + "-" + attempt.AttemptID + "-stderr",
		Time: at,
	}, stderrPath)
	status, result, exitCode, exitState := deterministicOutcome(waitResult)
	summary := deterministicReportSummary(step.EffectiveKind(), command, status, result, stdoutTail, stderrTail)
	report := runstore.Report{
		RunID:     opts.RunID,
		StepID:    attempt.StepID,
		AgentID:   attempt.AgentID,
		AttemptID: attempt.AttemptID,
		Status:    status,
		Result:    result,
		Summary:   summary,
		Commands:  []string{strings.Join(command, " ")},
		Tests:     []string{summary},
	}
	outcomeErr := validateGeneratedOutcome(step, status, result)
	finished, _, reportErr := loaded.Store.RecordAttemptReport(opts.RunID, runstore.RecordReportRequest{
		State:     runstore.AttemptStateReported,
		Report:    report,
		ExitCode:  exitCode,
		ExitState: exitState,
		LogRef:    refPtr(processLogRef),
		Time:      at,
	})
	return finished, stdoutRef, stderrRef, true, errors.Join(stdoutCloseErr, stderrCloseErr, stdoutTailErr, stderrTailErr, stdoutErr, stderrErr, reportErr, outcomeErr, waitResult.ctxErr)
}

func deterministicPromptContent(workflowName string, attempt runstore.Attempt, step config.Step) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# Tiny Orc System Step\n\n")
	fmt.Fprintf(&out, "- workflow: `%s`\n", workflowName)
	fmt.Fprintf(&out, "- step_id: `%s`\n", attempt.StepID)
	fmt.Fprintf(&out, "- kind: `%s`\n", step.EffectiveKind())
	fmt.Fprintf(&out, "- attempt_id: `%s`\n", attempt.AttemptID)
	return []byte(out.String())
}

func recordDeterministicProcessError(store *runstore.Store, runID string, attempt runstore.Attempt, step config.Step, command []string, exitState string, logRef runstore.ArtifactRef, at time.Time, causes ...error) (runstore.Attempt, error) {
	status, result := reportStatusFailed, resultProcessError
	if err := validateGeneratedOutcome(step, status, result); err != nil {
		causes = append(causes, err)
	}
	if len(command) == 0 {
		command = []string{step.EffectiveKind()}
	}
	summary := deterministicReportSummary(step.EffectiveKind(), command, status, result, "", "")
	finished, _, reportErr := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State:                runstore.AttemptStateReported,
		Report:               deterministicReport(runID, attempt, status, result, summary, command),
		ExitState:            exitState,
		LogRef:               refPtr(logRef),
		AllowStartingAttempt: true,
		Time:                 at,
	})
	if reportErr == nil {
		return finished, errors.Join(causes...)
	}
	finished, finishErr := finishProcessErrorAttempt(store, runID, attempt.AttemptID, exitState, logRef, at, causes...)
	return finished, errors.Join(reportErr, finishErr)
}

func deterministicReport(runID string, attempt runstore.Attempt, status, result, summary string, command []string) runstore.Report {
	return runstore.Report{
		RunID:     runID,
		StepID:    attempt.StepID,
		AgentID:   attempt.AgentID,
		AttemptID: attempt.AttemptID,
		Status:    status,
		Result:    result,
		Summary:   summary,
		Commands:  []string{strings.Join(command, " ")},
		Tests:     []string{summary},
	}
}

func validateGeneratedOutcome(step config.Step, status, result string) error {
	if slices.Contains(step.AllowedResults[status], result) {
		return nil
	}
	return fmt.Errorf("step generated outcome %q is not declared in allowed_results", status+"/"+result)
}

func openDeterministicOutputFile(attempt runstore.Attempt, stream string) (*os.File, string, error) {
	file, err := os.CreateTemp("", "orc-"+attempt.StepID+"-"+attempt.AttemptID+"-"+stream+"-*")
	if err != nil {
		return nil, "", err
	}
	name := file.Name()
	return file, name, nil
}

func removeDeterministicOutput(file *os.File, path string) {
	if file != nil {
		_ = file.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

func deterministicExecSpec(root string, step config.Step) ([]string, string, []string, error) {
	cwd := root
	if step.CWD != "" {
		resolved, err := resolveRepoRelativeDir(root, step.CWD)
		if err != nil {
			return nil, "", nil, err
		}
		cwd = resolved
	}
	var command []string
	switch step.EffectiveKind() {
	case config.StepKindCommand:
		command = append([]string(nil), step.Command.Argv...)
	case config.StepKindScript:
		path, err := resolveRepoRelativeExecutable(root, step.Script.Path)
		if err != nil {
			return nil, "", nil, err
		}
		command = append([]string{path}, step.Script.Args...)
	default:
		return nil, "", nil, fmt.Errorf("step kind %q is not deterministic", step.EffectiveKind())
	}
	env := mergeEnv(os.Environ(), step.Env)
	return command, cwd, env, nil
}

func deterministicCommandPath(command, env []string, cwd string) (string, error) {
	if len(command) == 0 || command[0] == "" {
		return "", errors.New("command argv[0] is required")
	}
	return resolveWorkerExecutable(command[0], env, cwd)
}

func resolveRepoRelativeDir(root, rel string) (string, error) {
	path, err := resolveRepoRelative(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", rel)
	}
	return path, nil
}

func resolveRepoRelativeExecutable(root, rel string) (string, error) {
	path, err := resolveRepoRelative(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("script %q is a directory", rel)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("script %q is not executable", rel)
	}
	return path, nil
}

func resolveRepoRelative(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be repo-relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must be clean and stay under repository root", rel)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, clean)
	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(rootReal, realPath)
	if err != nil {
		return "", err
	}
	if relToRoot == "." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || relToRoot == ".." || filepath.IsAbs(relToRoot) {
		return "", fmt.Errorf("path %q escapes repository root", rel)
	}
	return realPath, nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := append([]string(nil), base...)
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for i := range out {
			if strings.HasPrefix(out[i], prefix) {
				out[i] = prefix + value
				replaced = true
			}
		}
		if !replaced {
			out = append(out, prefix+value)
		}
	}
	return out
}

type deterministicWaitResult struct {
	err             error
	ctxErr          error
	workflowTimeout bool
}

func waitForDeterministicProcess(ctx context.Context, timeout time.Duration, cmd *exec.Cmd) deterministicWaitResult {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		terminateProcessGroup(cmd.Process.Pid)
		return deterministicWaitResult{err: err}
	case <-timer.C:
		terminateProcessGroup(cmd.Process.Pid)
		err := <-done
		if err == nil {
			err = context.DeadlineExceeded
		}
		return deterministicWaitResult{err: err, workflowTimeout: true}
	case <-ctx.Done():
		terminateProcessGroup(cmd.Process.Pid)
		err := <-done
		if err == nil {
			err = ctx.Err()
		}
		return deterministicWaitResult{err: err, ctxErr: ctx.Err()}
	}
}

func deterministicOutcome(result deterministicWaitResult) (string, string, *int, string) {
	if result.workflowTimeout {
		return reportStatusFailed, resultTimeout, nil, exitStateTimeout
	}
	if result.ctxErr != nil {
		return reportStatusFailed, resultProcessError, nil, exitStateCanceled
	}
	if result.err == nil {
		code := 0
		return reportStatusDone, resultCommandPassed, &code, exitStateExited
	}
	var exitErr *exec.ExitError
	if errors.As(result.err, &exitErr) {
		code := exitErr.ExitCode()
		return reportStatusDone, resultCommandFailed, &code, exitStateExited
	}
	return reportStatusFailed, resultProcessError, nil, result.err.Error()
}

func deterministicReportSummary(kind string, command []string, status, result, stdoutTail, stderrTail string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s step finished with %s/%s: %s", kind, status, result, strings.Join(command, " "))
	if status == reportStatusDone && result == resultCommandPassed {
		return out.String()
	}
	if stdoutTail != "" {
		fmt.Fprintf(&out, "\n\nstdout tail:\n%s", stdoutTail)
	}
	if stderrTail != "" {
		fmt.Fprintf(&out, "\n\nstderr tail:\n%s", stderrTail)
	}
	return out.String()
}

func boundedLogTailFromFile(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- path is a launcher-owned temp file.
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	truncated := false
	if info.Size() > failureTailBytes {
		start = info.Size() - failureTailBytes
		truncated = true
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	if truncated {
		if i := bytes.IndexByte(content, '\n'); i >= 0 && i+1 < len(content) {
			content = content[i+1:]
		}
	}
	return boundedLogTail(content), nil
}

func boundedLogTail(content []byte) string {
	if len(content) > failureTailBytes {
		content = content[len(content)-failureTailBytes:]
		if i := bytes.IndexByte(content, '\n'); i >= 0 && i+1 < len(content) {
			content = content[i+1:]
		}
	}
	lines := bytes.Split(content, []byte{'\n'})
	if len(lines) > failureTailLines {
		lines = lines[len(lines)-failureTailLines:]
	}
	return strings.TrimRight(string(bytes.Join(lines, []byte{'\n'})), "\n")
}

type workerRunner struct {
	ctx         context.Context
	loaded      runcontext.Context
	opts        Options
	attempt     runstore.Attempt
	prompt      []byte
	at          time.Time
	command     []string
	workerEnv   []string
	logRef      runstore.ArtifactRef
	logFile     *os.File
	cmd         *exec.Cmd
	releaseExec func(bool) error
	stdin       io.WriteCloser
}

func (r *workerRunner) run() (runstore.Attempt, runstore.ArtifactRef, bool, error) {
	if finished, err := r.selectCommand(); err != nil {
		return finished, runstore.ArtifactRef{}, false, err
	}
	if finished, err := r.openLog(); err != nil {
		return finished, r.logRef, false, err
	}
	defer func() {
		_ = r.logFile.Close()
	}()
	if finished, err := r.prepareWorkerCommand(); err != nil {
		return finished, r.logRef, false, err
	}
	defer func() {
		if r.releaseExec != nil {
			_ = r.releaseExec(false)
		}
	}()
	if finished, err := r.startWorker(); err != nil {
		return finished, r.logRef, false, err
	}
	if finished, err := r.recordProcessAndRelease(); err != nil {
		return finished, r.logRef, false, err
	}
	finished, err := r.feedPromptWaitAndFinish()
	return finished, r.logRef, true, err
}

func (r *workerRunner) selectCommand() (runstore.Attempt, error) {
	r.command = r.opts.Command
	if len(r.command) == 0 {
		r.command = defaultCommand
	}
	if r.command[0] == "" {
		err := errors.New("worker command is required")
		return r.finishPreStart(exitStateInvalidCommand, runstore.ArtifactRef{}, err)
	}
	if err := r.ctx.Err(); err != nil {
		return r.finishPreStart(exitStateCanceled, runstore.ArtifactRef{}, err)
	}
	return runstore.Attempt{}, nil
}

func (r *workerRunner) openLog() (runstore.Attempt, error) {
	logRef, logFile, err := openStreamingLog(r.loaded.Store, r.loaded.Run, r.attempt, r.at)
	if err != nil {
		return r.finishPreStart(exitStateLogStartFailed, runstore.ArtifactRef{}, err)
	}
	r.logRef = logRef
	r.logFile = logFile
	return runstore.Attempt{}, nil
}

func (r *workerRunner) prepareWorkerCommand() (runstore.Attempt, error) {
	if err := r.ctx.Err(); err != nil {
		return r.finishPreStart(exitStateCanceled, r.logRef, err)
	}
	r.workerEnv = os.Environ()
	if r.opts.Env != nil {
		r.workerEnv = r.opts.Env
	}
	cmd, releaseExec, err := newWorkerCommand(r.command, r.workerEnv, r.loaded.Project.Root)
	if err != nil {
		return r.finishLoggedStartFailure(exitStateStartFailed, err)
	}
	r.cmd = cmd
	r.releaseExec = releaseExec
	r.cmd.Dir = r.loaded.Project.Root
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r.cmd.Env = append(filteredExecEnv(append([]string(nil), r.workerEnv...)), r.cmd.Env...)
	r.cmd.Stdout = r.logFile
	r.cmd.Stderr = r.logFile
	return runstore.Attempt{}, nil
}

func (r *workerRunner) startWorker() (runstore.Attempt, error) {
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		return r.finishLoggedStartFailure(exitStateStartFailed, err)
	}
	r.stdin = stdin
	if err := r.cmd.Start(); err != nil {
		return r.finishLoggedStartFailure(exitStateStartFailed, err)
	}
	return runstore.Attempt{}, nil
}

func (r *workerRunner) recordProcessAndRelease() (runstore.Attempt, error) {
	processStartTime, err := processStartIdentity(r.cmd.Process.Pid)
	if err != nil {
		return r.finishStartedProcessErrorWithLog(err)
	}
	started, _, err := r.loaded.Store.RecordAttemptProcessContext(r.ctx, r.loaded.Run.ID, runstore.AttemptProcessRequest{
		AttemptID:        r.attempt.AttemptID,
		PID:              r.cmd.Process.Pid,
		ProcessStartTime: processStartTime,
		Time:             r.at,
	})
	if err != nil {
		return r.finishStartedProcessErrorWithLog(err)
	}
	if err := r.ctx.Err(); err != nil {
		return r.finishStartedProcessErrorSilent(err)
	}
	if err := r.releaseExec(true); err != nil {
		return r.finishStartedProcessErrorWithLog(err)
	}
	r.attempt = started
	return runstore.Attempt{}, nil
}

func (r *workerRunner) feedPromptWaitAndFinish() (runstore.Attempt, error) {
	promptWriteDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(r.stdin, bytes.NewReader(r.prompt))
		closeErr := r.stdin.Close()
		promptWriteDone <- errors.Join(err, closeErr)
	}()
	waitResult := r.waitWithTimeoutAndReport()
	promptWriteErr := <-promptWriteDone
	if promptWriteErr != nil && waitResult.err != nil {
		_, _ = r.logFile.WriteString(promptWriteErr.Error() + "\n")
	}
	logErr := r.logFile.Sync()
	if terminal, ok, err := r.reportTerminalAttemptAfterWait(); err != nil {
		return terminal, errors.Join(logErr, err)
	} else if ok {
		return terminal, errors.Join(logErr, r.recordPostReportWarning(terminal, waitResult))
	}
	finished, finishErr := r.finishWaitOutcome(waitResult)
	var ctxErr error
	if waitResult.ctxErr != nil && !waitResult.workflowTimeout {
		ctxErr = waitResult.ctxErr
	}
	return finished, errors.Join(logErr, finishErr, ctxErr)
}

func (r *workerRunner) recordPostReportWarning(attempt runstore.Attempt, waitResult waitResult) error {
	warningTime := normalizeTime(waitResult.warningTime)
	switch {
	case waitResult.postReportGraceExceeded:
		return r.recordAttemptWarning(attempt, warningKindPostReportGraceTerminated, nil, "report_exit_grace_exceeded", "worker was terminated after valid report and report-exit grace", warningTime)
	case waitResult.err != nil && waitResult.ctxErr == nil && !waitResult.workflowTimeout:
		var exitErr *exec.ExitError
		if !errors.As(waitResult.err, &exitErr) {
			return nil
		}
		code := exitErr.ExitCode()
		return r.recordAttemptWarning(attempt, warningKindPostReportProcessExit, &code, exitStateExited, "worker exited nonzero after valid report; report remains authoritative", warningTime)
	default:
		return nil
	}
}

func (r *workerRunner) recordAttemptWarning(attempt runstore.Attempt, kind string, exitCode *int, exitState, message string, at time.Time) error {
	_, _, err := r.loaded.Store.RecordAttemptWarning(r.loaded.Run.ID, runstore.AttemptWarning{
		AttemptID: attempt.AttemptID,
		Kind:      kind,
		ExitCode:  exitCode,
		ExitState: exitState,
		Message:   message,
		Time:      at,
	})
	return err
}

func (r *workerRunner) waitWithTimeoutAndReport() waitResult {
	return waitForWorkerProcessWithReport(r.ctx, r.loaded.Workflow.Defaults.Timeout.Duration, r.loaded.Workflow.Defaults.ReportExitGrace.Duration, r.cmd, r.pollReportedAttemptIgnoringLoadError)
}

func (r *workerRunner) pollReportedAttemptIgnoringLoadError() bool {
	_, ok, err := loadAttemptByID(r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, func(attempt runstore.Attempt) bool {
		return attempt.State == runstore.AttemptStateReported
	})
	return ok && err == nil
}

func (r *workerRunner) reportTerminalAttemptAfterWait() (runstore.Attempt, bool, error) {
	return loadAttemptByID(r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, func(attempt runstore.Attempt) bool {
		return attempt.State == runstore.AttemptStateReported || attempt.State == runstore.AttemptStateInvalidReport
	})
}

func (r *workerRunner) finishWaitOutcome(waitResult waitResult) (runstore.Attempt, error) {
	state, result, exitCode, exitState := outcomeFromWait(waitResult)
	finishReq := runstore.FinishAttemptRequest{
		AttemptID: r.attempt.AttemptID,
		State:     state,
		Status:    reportStatusFailed,
		Result:    result,
		ExitCode:  exitCode,
		ExitState: exitState,
		LogRef:    refPtr(r.logRef),
		Time:      r.at,
	}
	if waitResult.ctxErr != nil && !waitResult.workflowTimeout {
		return finishAttemptWithCleanupContext(r.loaded.Store, r.loaded.Run.ID, finishReq)
	}
	finished, _, err := r.loaded.Store.FinishAttempt(r.loaded.Run.ID, finishReq)
	return finished, err
}

func (r *workerRunner) finishPreStart(exitState string, logRef runstore.ArtifactRef, causes ...error) (runstore.Attempt, error) {
	return finishProcessErrorAttempt(r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, exitState, logRef, r.at, causes...)
}

func (r *workerRunner) finishLoggedStartFailure(exitState string, err error) (runstore.Attempt, error) {
	_, logErr := r.logFile.WriteString(err.Error() + "\n")
	return r.finishPreStart(exitState, r.logRef, err, logErr)
}

func (r *workerRunner) finishStartedProcessErrorWithLog(err error) (runstore.Attempt, error) {
	_, logErr := r.logFile.WriteString(err.Error() + "\n")
	return r.finishStartedProcessError(err, logErr)
}

func (r *workerRunner) finishStartedProcessErrorSilent(err error) (runstore.Attempt, error) {
	return r.finishStartedProcessError(err, nil)
}

func (r *workerRunner) finishStartedProcessError(err, logErr error) (runstore.Attempt, error) {
	terminateProcessGroup(r.cmd.Process.Pid)
	_, _ = r.cmd.Process.Wait()
	exitState := exitStateStartFailed
	if isContextError(err) {
		exitState = exitStateCanceled
	}
	return finishProcessErrorAttempt(r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, exitState, r.logRef, r.at, err, logErr)
}

func newWorkerCommand(command, env []string, dir string) (*exec.Cmd, func(bool) error, error) {
	execPath, err := resolveWorkerExecutable(command[0], env, dir)
	if err != nil {
		return nil, nil, err
	}
	helperPath, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve launcher helper: %w", err)
	}
	helperToken, err := newExecHelperToken()
	if err != nil {
		return nil, nil, err
	}
	helperArgs := append([]string{execHelperArg, helperToken, execPath}, command[1:]...)
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	released := false
	release := func(start bool) error {
		if released {
			return nil
		}
		released = true
		defer func() {
			_ = writeFile.Close()
		}()
		_ = readFile.Close()
		if !start {
			return nil
		}
		if _, err := writeFile.WriteString(helperToken + "\n"); err != nil {
			return fmt.Errorf("release worker exec: %w", err)
		}
		return nil
	}
	cmd := exec.Command(helperPath, helperArgs...) // #nosec G204,G702 -- re-execing the current launcher binary is intentional; helper execs the configured worker only after durable PID recording.
	cmd.ExtraFiles = []*os.File{readFile}
	cmd.Env = []string{execHelperEnv + "=" + helperToken}
	return cmd, release, nil
}

func resolveWorkerExecutable(name string, env []string, cwd string) (string, error) {
	execPath := name
	if strings.ContainsRune(execPath, os.PathSeparator) {
		statPath := execPath
		if !filepath.IsAbs(statPath) {
			statPath = filepath.Join(cwd, statPath)
		}
		info, err := os.Stat(statPath)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", execPath)
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%s is not executable", execPath)
		}
		return execPath, nil
	}
	for _, dir := range workerPath(env) {
		if dir == "" {
			dir = "."
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		candidate := filepath.Join(dir, execPath)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", exec.ErrNotFound
}

func workerPath(env []string) []string {
	const prefix = "PATH="
	for i := len(env) - 1; i >= 0; i-- {
		if after, ok := strings.CutPrefix(env[i], prefix); ok {
			return filepath.SplitList(after)
		}
	}
	return nil
}

func runExecHelper(wantToken string, command []string) int {
	if wantToken == "" || os.Getenv(execHelperEnv) != wantToken {
		return 125
	}
	if len(command) == 0 || command[0] == "" {
		return 125
	}
	handshake := os.NewFile(execHelperFD, "launcher-handshake")
	if handshake == nil {
		return 125
	}
	token, err := readExecHelperToken(handshake)
	closeErr := handshake.Close()
	if err != nil || closeErr != nil || token != wantToken {
		return 125
	}
	env := filteredExecEnv(os.Environ())
	if err := syscall.Exec(command[0], command, env); err != nil { // #nosec G204,G702 -- worker launching intentionally execs the configured Codex command after the parent records process metadata.
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 126
	}
	return 0
}

func newExecHelperToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate exec helper token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func readExecHelperToken(reader io.Reader) (string, error) {
	var buf [33]byte
	n, err := io.ReadFull(reader, buf[:])
	if err != nil {
		return "", err
	}
	if n != len(buf) || buf[len(buf)-1] != '\n' {
		return "", errors.New("invalid exec helper token")
	}
	return string(buf[:len(buf)-1]), nil
}

func filteredExecEnv(env []string) []string {
	out := env[:0]
	prefix := execHelperEnv + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		out = append(out, item)
	}
	return out
}

type waitResult struct {
	err                     error
	ctxErr                  error
	workflowTimeout         bool
	postReportGraceExceeded bool
	warningTime             time.Time
}

func outcomeFromWait(result waitResult) (string, string, *int, string) {
	if result.workflowTimeout {
		return runstore.AttemptStateTimedOut, resultTimeout, nil, exitStateTimeout
	}
	if result.ctxErr != nil {
		return runstore.AttemptStateProcessError, resultProcessError, nil, exitStateCanceled
	}
	if result.err == nil {
		code := 0
		return runstore.AttemptStateMissingReport, resultMissingReport, &code, exitStateExited
	}
	var exitErr *exec.ExitError
	if errors.As(result.err, &exitErr) {
		code := exitErr.ExitCode()
		return runstore.AttemptStateProcessError, resultProcessError, &code, exitStateExited
	}
	return runstore.AttemptStateProcessError, resultProcessError, nil, result.err.Error()
}

func waitForWorkerProcessWithReport(ctx context.Context, timeout, reportExitGrace time.Duration, cmd *exec.Cmd, reportPersisted func() bool) waitResult {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	workflowTimer := time.NewTimer(timeout)
	defer workflowTimer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var graceTimer *time.Timer
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	var reportGraceDone <-chan time.Time
	for {
		select {
		case err := <-done:
			terminateProcessGroup(cmd.Process.Pid)
			return waitResult{err: err, warningTime: time.Now().UTC()}
		case <-workflowTimer.C:
			return terminateAndCollectWaitResult(cmd.Process.Pid, done, waitResult{workflowTimeout: true}, context.DeadlineExceeded)
		case <-reportGraceDone:
			return terminateAndCollectWaitResult(cmd.Process.Pid, done, waitResult{postReportGraceExceeded: true, warningTime: time.Now().UTC()}, context.DeadlineExceeded)
		case <-ticker.C:
			if reportGraceDone == nil && reportPersisted != nil && reportPersisted() {
				graceTimer, reportGraceDone = startReportExitGrace(workflowTimer, reportExitGrace)
			}
		case <-ctx.Done():
			ctxErr := ctx.Err()
			return terminateAndCollectWaitResult(cmd.Process.Pid, done, waitResult{ctxErr: ctxErr}, ctxErr)
		}
	}
}

func startReportExitGrace(workflowTimer *time.Timer, grace time.Duration) (*time.Timer, <-chan time.Time) {
	if !workflowTimer.Stop() {
		select {
		case <-workflowTimer.C:
		default:
		}
	}
	graceTimer := time.NewTimer(grace)
	return graceTimer, graceTimer.C
}

func terminateAndCollectWaitResult(pid int, done <-chan error, result waitResult, fallback error) waitResult {
	terminateProcessGroup(pid)
	err := <-done
	if err == nil {
		err = fallback
	}
	result.err = err
	return result
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func openStreamingLog(store *runstore.Store, run *runstore.Run, attempt runstore.Attempt, at time.Time) (runstore.ArtifactRef, *os.File, error) {
	ref, err := store.WriteArtifact(run.ID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    attempt.StepID + "-" + attempt.AttemptID,
		Content: nil,
		Time:    at,
	})
	if err != nil {
		return runstore.ArtifactRef{}, nil, err
	}
	_, _, err = store.RecordAttemptLog(run.ID, runstore.AttemptLogRequest{
		AttemptID: attempt.AttemptID,
		LogRef:    ref,
		Time:      at,
	})
	if err != nil {
		return ref, nil, err
	}
	file, err := store.OpenArtifactAppend(run.ID, ref)
	if err != nil {
		return ref, nil, err
	}
	return ref, file, nil
}

func recoverOrRefuseActiveAttempt(store *runstore.Store, run *runstore.Run) (Result, error) {
	active := *run.Status.ActiveAttempt
	if attemptStillStarting(active, time.Now().UTC()) {
		return Result{RunID: run.ID, Attempt: active}, fmt.Errorf("run %q already has starting attempt %q", run.ID, active.AttemptID)
	}
	if active.PID > 0 && processIdentityMatches(active.PID, active.ProcessStartTime) {
		if attemptTimedOut(active, time.Now().UTC()) {
			terminateProcessGroup(active.PID)
			recovered, err := recoverActiveAttempt(store, run, active, runstore.AttemptStateTimedOut, resultTimeout, exitStateTimeout)
			return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, err
		}
		return Result{RunID: run.ID, Attempt: active}, fmt.Errorf("run %q already has active attempt %q", run.ID, active.AttemptID)
	}
	recovered, err := recoverActiveAttempt(store, run, active, runstore.AttemptStateProcessError, resultProcessError, exitStateUnknown)
	if err != nil {
		return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, err
	}
	return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, nil
}

func recoverActiveAttempt(store *runstore.Store, run *runstore.Run, active runstore.Attempt, state, result, exitState string) (runstore.Attempt, error) {
	recovered, _, err := store.RecoverAttempt(run.ID, runstore.FinishAttemptRequest{
		AttemptID: active.AttemptID,
		State:     state,
		Status:    reportStatusFailed,
		Result:    result,
		ExitState: exitState,
		LogRef:    active.LogRef,
		Time:      time.Now().UTC(),
	})
	return recovered, err
}

func loadAttemptByID(store *runstore.Store, runID, attemptID string, match func(runstore.Attempt) bool) (runstore.Attempt, bool, error) {
	run, err := store.Load(runID)
	if err != nil {
		return runstore.Attempt{}, false, err
	}
	for i := len(run.Status.Attempts) - 1; i >= 0; i-- {
		attempt := run.Status.Attempts[i]
		if attempt.AttemptID == attemptID && (match == nil || match(attempt)) {
			return attempt, true, nil
		}
	}
	return runstore.Attempt{}, false, nil
}

func attemptStillStarting(attempt runstore.Attempt, now time.Time) bool {
	if attempt.State != runstore.AttemptStateStarting || attempt.PID != 0 {
		return false
	}
	return !attemptTimedOut(attempt, now)
}

func attemptTimedOut(attempt runstore.Attempt, now time.Time) bool {
	timeout, err := time.ParseDuration(attempt.Timeout)
	if err != nil || timeout <= 0 {
		return false
	}
	return now.Sub(attempt.StartedAt) > timeout
}

func processIdentityMatches(pid int, wantStartTime string) bool {
	if wantStartTime == "" {
		return false
	}
	gotStartTime, err := processStartIdentity(pid)
	if err != nil {
		return false
	}
	return gotStartTime == wantStartTime
}

func processStartIdentity(pid int) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("process identity requires linux procfs, got %s", runtime.GOOS)
	}
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- pid is numeric and scoped to procfs.
	if err != nil {
		return "", fmt.Errorf("read process identity for pid %d: %w", pid, err)
	}
	return parseProcStatStartTime(string(content))
}

func parseProcStatStartTime(stat string) (string, error) {
	end := strings.LastIndex(stat, ") ")
	if end == -1 {
		return "", errors.New("parse process identity: missing command field")
	}
	fields := strings.Fields(stat[end+2:])
	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return "", errors.New("parse process identity: missing starttime field")
	}
	if _, err := strconv.ParseUint(fields[startTimeIndexAfterCommand], 10, 64); err != nil {
		return "", fmt.Errorf("parse process identity starttime: %w", err)
	}
	return fields[startTimeIndexAfterCommand], nil
}

func newAttemptID(now time.Time, step string) (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate attempt id: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("20060102T150405Z"), step, hex.EncodeToString(buf[:])), nil
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func refPtr(ref runstore.ArtifactRef) *runstore.ArtifactRef {
	if ref.Path == "" {
		return nil
	}
	return &ref
}

func printLaunchResult(w io.Writer, result Result) {
	if w == nil {
		return
	}
	if result.SoftCap != nil {
		loopCap := result.SoftCap
		_, _ = fmt.Fprintf(w, "warning: workflow loop soft cap reached for workflow %s state %s at count %d (soft %d, hard %d); continue only if this attempt can break the loop or escalate\n", loopCap.Workflow, loopCap.State, loopCap.Count, loopCap.Soft, loopCap.Hard)
	}
	action := "launched"
	if result.Recovered {
		action = "recovered"
	} else if !result.Launched {
		action = "terminalized"
	}
	_, _ = fmt.Fprintf(w, "%s attempt %s\n", action, result.Attempt.AttemptID)
	_, _ = fmt.Fprintf(w, "step: %s\n", result.Attempt.StepID)
	_, _ = fmt.Fprintf(w, "agent: %s\n", result.Attempt.AgentID)
	_, _ = fmt.Fprintf(w, "result: %s/%s\n", result.Attempt.Status, result.Attempt.Result)
	if result.Log.Path != "" {
		_, _ = fmt.Fprintf(w, "log: %s\n", result.Log.Path)
	}
}
