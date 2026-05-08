package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	warningKindPostReportGraceTerminated = "post_report_grace_terminated"
	warningKindPostReportProcessExit     = "post_report_process_exit"
)

var (
	defaultCommand        = []string{"codex", "--ask-for-approval", "never", "exec", "--skip-git-repo-check", "-"}
	sandboxDefaultCommand = []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "exec", "--skip-git-repo-check", "-"}
)

func runProcess(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, prompt []byte, at time.Time, progressEnv map[string]string) (runstore.Attempt, runstore.ArtifactRef, bool, error) {
	runner := workerRunner{
		ctx:         ctx,
		loaded:      loaded,
		opts:        opts,
		attempt:     attempt,
		prompt:      prompt,
		at:          at,
		progressEnv: progressEnv,
	}
	return runner.run()
}

type workerRunner struct {
	ctx         context.Context
	loaded      runcontext.Context
	opts        Options
	attempt     runstore.Attempt
	prompt      []byte
	at          time.Time
	progressEnv map[string]string
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
		r.command = r.defaultCommand()
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

func (r *workerRunner) defaultCommand() []string {
	if r.loaded.Project.Config.Sandbox != nil && verifyWorkerRepoSandbox(r.loaded.Project.Root) == nil {
		return slices.Clone(sandboxDefaultCommand)
	}
	return slices.Clone(defaultCommand)
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
	r.workerEnv = mergeEnv(r.workerEnv, r.progressEnv)
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
