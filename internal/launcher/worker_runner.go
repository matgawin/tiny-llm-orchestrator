package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/sandbox"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/workflow"

	"go.uber.org/zap"
)

const (
	warningKindPostReportGraceTerminated = "post_report_grace_terminated"
	warningKindPostReportProcessExit     = "post_report_process_exit"

	runtimePromptDeliveryStdin = "stdin"
	runtimePromptDeliveryFile  = "file"
)

var runtimeArgPlaceholderRegex = regexp.MustCompile(`\{[^{}]+\}`)

func runProcess(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, prompt promptrender.Result, at time.Time, progressEnv map[string]string) (runstore.Attempt, runstore.ArtifactRef, bool, error) {
	runner := workerRunner{
		ctx:         ctx,
		loaded:      loaded,
		opts:        opts,
		attempt:     attempt,
		prompt:      prompt,
		at:          at,
		progressEnv: progressEnv,
	}
	return runner.run(ctx)
}

type workerRunner struct {
	ctx         context.Context
	loaded      runcontext.Context
	opts        Options
	attempt     runstore.Attempt
	prompt      promptrender.Result
	at          time.Time
	progressEnv map[string]string
	command     []string
	promptMode  string
	workerEnv   []string
	logRef      runstore.ArtifactRef
	logFile     *os.File
	cmd         *exec.Cmd
	releaseExec func(bool) error
	stdin       io.WriteCloser
}

func (r *workerRunner) run(ctx context.Context) (runstore.Attempt, runstore.ArtifactRef, bool, error) {
	if finished, err := r.selectCommand(ctx); err != nil {
		return finished, runstore.ArtifactRef{}, false, err
	}
	if finished, err := r.openLog(ctx); err != nil {
		return finished, r.logRef, false, err
	}
	defer func() {
		_ = r.logFile.Close()
	}()
	if finished, err := r.prepareWorkerCommand(ctx); err != nil {
		return finished, r.logRef, false, err
	}
	defer func() {
		if r.releaseExec != nil {
			_ = r.releaseExec(false)
		}
	}()
	if finished, err := r.startWorker(ctx); err != nil {
		return finished, r.logRef, false, err
	}
	if finished, err := r.recordProcessAndRelease(ctx); err != nil {
		return finished, r.logRef, false, err
	}
	finished, err := r.feedPromptWaitAndFinish(ctx)
	return finished, r.logRef, true, err
}

func (r *workerRunner) context() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

func (r *workerRunner) selectCommand(ctx context.Context) (runstore.Attempt, error) {
	r.command = r.opts.Command
	if len(r.command) == 0 {
		command, promptMode, err := r.runtimeCommand()
		if err != nil {
			return r.finishPreStartContext(ctx, exitStateInvalidCommand, runstore.ArtifactRef{}, err)
		}
		r.command = command
		r.promptMode = promptMode
	} else {
		r.promptMode = runtimePromptDeliveryStdin
	}
	if r.command[0] == "" {
		err := stableerr.New("worker command is required")
		return r.finishPreStartContext(ctx, exitStateInvalidCommand, runstore.ArtifactRef{}, err)
	}
	if err := ctx.Err(); err != nil {
		return r.finishPreStartContext(ctx, exitStateCanceled, runstore.ArtifactRef{}, err)
	}
	return runstore.Attempt{}, nil
}

func (r *workerRunner) runtimeCommand() ([]string, string, error) {
	step, ok := r.loaded.Workflow.Steps[r.attempt.StepID]
	if !ok {
		return nil, "", stableerr.Errorf("step %q is not configured", r.attempt.StepID)
	}
	runtimeID := r.loaded.Workflow.EffectiveRuntime(step)
	if runtimeID == "" {
		return nil, "", stableerr.Errorf("step %q has no effective runtime", r.attempt.StepID)
	}
	runtime, ok := r.loaded.Project.Runtimes[runtimeID]
	if !ok {
		return nil, "", stableerr.Errorf("step %q references missing runtime %q", r.attempt.StepID, runtimeID)
	}
	promptMode := runtime.Prompt.Delivery
	if promptMode != runtimePromptDeliveryStdin && promptMode != runtimePromptDeliveryFile {
		return nil, "", stableerr.Errorf("runtime %q prompt.delivery %q is unsupported by launcher", runtimeID, promptMode)
	}
	sandboxMode := r.loaded.Project.Config.Sandbox != nil && verifyWorkerRepoSandbox(r.loaded.Project.Root) == nil
	if sandboxMode && !runtime.Sandbox.Supported {
		return nil, "", stableerr.Errorf("runtime %q does not support sandbox worker launches", runtimeID)
	}
	if !sandboxMode && runtime.Sandbox.Required {
		return nil, "", stableerr.Errorf("runtime %q requires sandbox worker launch but Orc sandbox markers are not verified", runtimeID)
	}

	values := runtimePlaceholderValues{
		model:      r.loaded.Workflow.EffectiveModel(step, runtime),
		reasoning:  r.loaded.Workflow.EffectiveReasoning(step, runtime),
		promptFile: r.prompt.Path,
		agentID:    r.attempt.AgentID,
		stepID:     r.attempt.StepID,
		attemptID:  r.attempt.AttemptID,
		runID:      r.loaded.Run.ID,
	}
	if runtime.Model.Required && values.model == "" {
		return nil, "", stableerr.Errorf("runtime %q requires a model but no effective model resolved", runtimeID)
	}
	if values.model != "" && !runtime.Model.Supported {
		return nil, "", stableerr.Errorf("runtime %q does not support model arguments", runtimeID)
	}
	if runtime.Reasoning.Required && values.reasoning == "" {
		return nil, "", stableerr.Errorf("runtime %q requires reasoning but no effective reasoning resolved", runtimeID)
	}
	if values.reasoning != "" && !runtime.Reasoning.Supported {
		return nil, "", stableerr.Errorf("runtime %q does not support reasoning arguments", runtimeID)
	}
	runtimeDirs := r.loaded.Workflow.EffectiveRuntimeDirs(step)
	if len(runtimeDirs) > 0 && !runtime.Directories.Supported {
		return nil, "", stableerr.Errorf("runtime %q does not support runtime_dirs", runtimeID)
	}
	if len(runtimeDirs) > 0 && len(runtime.Directories.Args) == 0 {
		return nil, "", stableerr.Errorf("runtime %q directories.args are required for runtime_dirs", runtimeID)
	}
	if sandboxMode {
		if err := r.verifySandboxRuntimeDirs(runtimeID, runtimeDirs); err != nil {
			return nil, "", err
		}
	}
	if promptMode == runtimePromptDeliveryFile && values.promptFile == "" {
		return nil, "", stableerr.Errorf("runtime %q prompt.delivery=file requires a persisted prompt artifact path", runtimeID)
	}

	command := []string{runtime.Command.Executable}
	if sandboxMode {
		command = append(command, runtime.Command.SandboxArgs...)
	} else {
		command = append(command, runtime.Command.NormalArgs...)
	}
	command = append(command, runtime.Command.Args...)
	if values.model != "" {
		command = append(command, runtime.Model.Args...)
	}
	if values.reasoning != "" {
		command = append(command, runtime.Reasoning.Args...)
	}
	var err error
	command, err = substituteRuntimePlaceholders(command, values)
	if err != nil {
		return nil, "", fmt.Errorf("runtime %q command args: %w", runtimeID, err)
	}
	for _, dir := range runtimeDirs {
		dirValues := values
		dirValues.dir = effectiveRuntimeDir(r.loaded.Project.Root, dir)
		dirArgs, err := substituteRuntimePlaceholders(runtime.Directories.Args, dirValues)
		if err != nil {
			return nil, "", fmt.Errorf("runtime %q directories args: %w", runtimeID, err)
		}
		command = append(command, dirArgs...)
	}
	return command, promptMode, nil
}

func (r *workerRunner) verifySandboxRuntimeDirs(runtimeID string, runtimeDirs []string) error {
	coverage, coverageErr := activeSandboxRuntimeDirCoverage()
	for _, dir := range runtimeDirs {
		resolved := effectiveRuntimeDir(r.loaded.Project.Root, dir)
		if coverageErr != nil {
			return runtimeDirSandboxCoverageError(r.attempt.StepID, runtimeID, dir, resolved, coverageErr.Error())
		}
		if !pathCoveredByAny(resolved, coverage) {
			return runtimeDirSandboxCoverageError(r.attempt.StepID, runtimeID, dir, resolved, "not covered by the repository mount, project sandbox.mounts, or selected runtime sandbox requirements")
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return runtimeDirSandboxCoverageError(r.attempt.StepID, runtimeID, dir, resolved, fmt.Sprintf("not visible inside the active sandbox: %v", err))
		}
		if !info.IsDir() {
			return runtimeDirSandboxCoverageError(r.attempt.StepID, runtimeID, dir, resolved, "visible path is not a directory")
		}
	}
	return nil
}

func activeSandboxRuntimeDirCoverage() ([]string, error) {
	value := os.Getenv(sandbox.RuntimeDirCoverageEnv)
	if value == "" {
		return nil, stableerr.Errorf("active sandbox runtime_dir coverage marker %s is not set", sandbox.RuntimeDirCoverageEnv)
	}
	var targets []string
	if err := json.Unmarshal([]byte(value), &targets); err != nil {
		return nil, fmt.Errorf("active sandbox runtime_dir coverage marker %s is invalid: %w", sandbox.RuntimeDirCoverageEnv, err)
	}
	coverage := make([]string, 0, len(targets))
	for _, target := range targets {
		if filepath.IsAbs(target) {
			coverage = append(coverage, filepath.Clean(target))
		}
	}
	return coverage, nil
}

func pathCoveredByAny(path string, coverage []string) bool {
	cleanPath := filepath.Clean(path)
	for _, root := range coverage {
		if pathCoveredBy(cleanPath, root) {
			return true
		}
	}
	return false
}

func pathCoveredBy(path, root string) bool {
	cleanRoot := filepath.Clean(root)
	if !filepath.IsAbs(cleanRoot) {
		return false
	}
	if path == cleanRoot {
		return true
	}
	rel, err := filepath.Rel(cleanRoot, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runtimeDirSandboxCoverageError(stepID, runtimeID, original, resolved, reason string) error {
	return stableerr.Errorf("runtime_dir sandbox coverage error for step %q runtime %q: runtime_dirs value %q resolved to %q: %s", stepID, runtimeID, original, resolved, reason)
}

type runtimePlaceholderValues struct {
	model      string
	reasoning  string
	promptFile string
	agentID    string
	stepID     string
	attemptID  string
	runID      string
	dir        string
}

func effectiveRuntimeDir(root, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(root, filepath.FromSlash(dir))
}

func substituteRuntimePlaceholders(args []string, values runtimePlaceholderValues) ([]string, error) {
	out := make([]string, len(args))
	for i, arg := range args {
		var err error
		out[i], err = substituteRuntimeArgPlaceholders(arg, values)
		if err != nil {
			return nil, fmt.Errorf("argv[%d]: %w", i, err)
		}
	}
	return out, nil
}

func substituteRuntimeArgPlaceholders(arg string, values runtimePlaceholderValues) (string, error) {
	result := arg
	for _, placeholder := range runtimeArgPlaceholderRegex.FindAllString(arg, -1) {
		value, ok := runtimePlaceholderValue(placeholder, values)
		if !ok {
			return "", stableerr.Errorf("unknown placeholder %s", placeholder)
		}
		if value == "" {
			return "", stableerr.Errorf("placeholder %s has no value", placeholder)
		}
		result = strings.ReplaceAll(result, placeholder, value)
	}
	if strings.ContainsAny(runtimeArgPlaceholderRegex.ReplaceAllString(arg, ""), "{}") {
		return "", stableerr.New("malformed placeholder syntax")
	}
	return result, nil
}

func runtimePlaceholderValue(placeholder string, values runtimePlaceholderValues) (string, bool) {
	switch placeholder {
	case "{model}":
		return values.model, true
	case "{reasoning}":
		return values.reasoning, true
	case "{prompt_file}":
		return values.promptFile, true
	case "{agent_id}":
		return values.agentID, true
	case "{step_id}":
		return values.stepID, true
	case "{attempt_id}":
		return values.attemptID, true
	case "{run_id}":
		return values.runID, true
	case "{dir}":
		return values.dir, true
	default:
		return "", false
	}
}

func (r *workerRunner) openLog(ctx context.Context) (runstore.Attempt, error) {
	logRef, logFile, err := openStreamingLog(cleanupContext(ctx), r.loaded.Store, r.loaded.Run, r.attempt, r.at)
	if err != nil {
		return r.finishPreStartContext(ctx, exitStateLogStartFailed, runstore.ArtifactRef{}, err)
	}
	r.logRef = logRef
	r.logFile = logFile
	return runstore.Attempt{}, nil
}

func (r *workerRunner) prepareWorkerCommand(ctx context.Context) (runstore.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return r.finishPreStartContext(ctx, exitStateCanceled, r.logRef, err)
	}
	r.workerEnv = os.Environ()
	if r.opts.Env != nil {
		r.workerEnv = r.opts.Env
	}
	r.workerEnv = mergeEnv(r.workerEnv, r.progressEnv)
	cmd, releaseExec, err := newWorkerCommand(ctx, r.command, r.workerEnv, r.loaded.Project.Root)
	if err != nil {
		return r.finishLoggedStartFailure(ctx, exitStateStartFailed, err)
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

func (r *workerRunner) startWorker(ctx context.Context) (runstore.Attempt, error) {
	if r.promptMode == runtimePromptDeliveryStdin {
		stdin, err := r.cmd.StdinPipe()
		if err != nil {
			return r.finishLoggedStartFailure(ctx, exitStateStartFailed, err)
		}
		r.stdin = stdin
	}
	if err := r.cmd.Start(); err != nil {
		return r.finishLoggedStartFailure(ctx, exitStateStartFailed, err)
	}
	return runstore.Attempt{}, nil
}

func (r *workerRunner) recordProcessAndRelease(ctx context.Context) (runstore.Attempt, error) {
	processStartTime, err := processStartIdentity(r.cmd.Process.Pid)
	if err != nil {
		return r.finishStartedProcessErrorWithLog(ctx, err)
	}
	started, _, err := r.loaded.Store.RecordAttemptProcessContext(ctx, r.loaded.Run.ID, runstore.AttemptProcessRequest{
		AttemptID:        r.attempt.AttemptID,
		PID:              r.cmd.Process.Pid,
		ProcessStartTime: processStartTime,
		Time:             r.at,
	})
	if err != nil {
		return r.finishStartedProcessErrorWithLog(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return r.finishStartedProcessErrorSilent(ctx, err)
	}
	if err := r.releaseExec(true); err != nil {
		return r.finishStartedProcessErrorWithLog(ctx, err)
	}
	r.attempt = started
	return runstore.Attempt{}, nil
}

func (r *workerRunner) feedPromptWaitAndFinish(ctx context.Context) (runstore.Attempt, error) {
	promptWriteDone := make(chan error, 1)
	if r.promptMode == runtimePromptDeliveryStdin {
		go func() {
			_, err := io.Copy(r.stdin, bytes.NewReader(r.prompt.Content))
			closeErr := r.stdin.Close()
			promptWriteDone <- errors.Join(err, closeErr)
		}()
	} else {
		promptWriteDone <- nil
	}
	waitResult := r.waitWithTimeoutAndReport(ctx)
	promptWriteErr := <-promptWriteDone
	if promptWriteErr != nil && waitResult.err != nil {
		_, _ = r.logFile.WriteString(promptWriteErr.Error() + "\n")
	}
	logErr := r.logFile.Sync()
	if terminal, ok, err := r.reportTerminalAttemptAfterWait(ctx); err != nil {
		return terminal, errors.Join(logErr, err)
	} else if ok {
		return terminal, errors.Join(logErr, r.recordPostReportWarning(ctx, terminal, waitResult))
	}
	finished, finishErr := r.finishWaitOutcome(ctx, waitResult)
	var ctxErr error
	if waitResult.ctxErr != nil && !waitResult.workflowTimeout {
		ctxErr = waitResult.ctxErr
	}
	return finished, errors.Join(logErr, finishErr, ctxErr)
}

func (r *workerRunner) recordPostReportWarning(ctx context.Context, attempt runstore.Attempt, waitResult waitResult) error {
	warningTime := normalizeTime(waitResult.warningTime)
	switch {
	case waitResult.postReportGraceExceeded:
		return r.recordAttemptWarning(ctx, attempt, warningKindPostReportGraceTerminated, nil, "report_exit_grace_exceeded", "worker was terminated after valid report and report-exit grace", warningTime)
	case waitResult.err != nil && waitResult.ctxErr == nil && !waitResult.workflowTimeout:
		var exitErr *exec.ExitError
		if !errors.As(waitResult.err, &exitErr) {
			return nil
		}
		code := exitErr.ExitCode()
		return r.recordAttemptWarning(ctx, attempt, warningKindPostReportProcessExit, &code, exitStateExited, "worker exited nonzero after valid report; report remains authoritative", warningTime)
	default:
		return nil
	}
}

func (r *workerRunner) recordAttemptWarning(ctx context.Context, attempt runstore.Attempt, kind string, exitCode *int, exitState, message string, at time.Time) error {
	_, _, err := r.loaded.Store.RecordAttemptWarningContext(cleanupContext(ctx), r.loaded.Run.ID, runstore.AttemptWarning{
		AttemptID: attempt.AttemptID,
		Kind:      kind,
		ExitCode:  exitCode,
		ExitState: exitState,
		Message:   message,
		Time:      at,
	})
	return err
}

func (r *workerRunner) waitWithTimeoutAndReport(ctx context.Context) waitResult {
	return waitForWorkerProcessWithReport(ctx, r.loaded.Workflow.Defaults.Timeout.Duration, r.loaded.Workflow.Defaults.ReportExitGrace.Duration, r.cmd, r.pollReportedAttemptIgnoringLoadError)
}

func (r *workerRunner) pollReportedAttemptIgnoringLoadError() bool {
	_, ok, err := loadAttemptByID(r.context(), r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, func(attempt runstore.Attempt) bool {
		return attempt.State == runstore.AttemptStateReported
	})
	return ok && err == nil
}

func (r *workerRunner) reportTerminalAttemptAfterWait(ctx context.Context) (runstore.Attempt, bool, error) {
	return loadAttemptByID(cleanupContext(ctx), r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, func(attempt runstore.Attempt) bool {
		return attempt.State == runstore.AttemptStateReported || attempt.State == runstore.AttemptStateInvalidReport
	})
}

func (r *workerRunner) finishWaitOutcome(ctx context.Context, waitResult waitResult) (runstore.Attempt, error) {
	state, result, exitCode, exitState := outcomeFromWait(waitResult)
	finishReq := runstore.FinishAttemptRequest{
		AttemptID: r.attempt.AttemptID,
		State:     state,
		Status:    workflow.ReportStatusFailed,
		Result:    result,
		ExitCode:  exitCode,
		ExitState: exitState,
		LogRef:    refPtr(r.logRef),
		Time:      r.at,
	}
	if waitResult.ctxErr != nil && !waitResult.workflowTimeout {
		return finishAttemptWithCleanupContext(ctx, r.loaded.Store, r.loaded.Run.ID, finishReq)
	}
	finished, _, err := r.loaded.Store.FinishAttemptContext(ctx, r.loaded.Run.ID, finishReq)
	return finished, err
}

func (r *workerRunner) finishPreStartContext(ctx context.Context, exitState string, logRef runstore.ArtifactRef, causes ...error) (runstore.Attempt, error) {
	return finishProcessErrorAttempt(ctx, r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, exitState, logRef, r.at, causes...)
}

func (r *workerRunner) finishLoggedStartFailure(ctx context.Context, exitState string, err error) (runstore.Attempt, error) {
	_, logErr := r.logFile.WriteString(err.Error() + "\n")
	return r.finishPreStartContext(ctx, exitState, r.logRef, err, logErr)
}

func (r *workerRunner) finishStartedProcessErrorWithLog(ctx context.Context, err error) (runstore.Attempt, error) {
	_, logErr := r.logFile.WriteString(err.Error() + "\n")
	return r.finishStartedProcessError(ctx, err, logErr)
}

func (r *workerRunner) finishStartedProcessErrorSilent(ctx context.Context, err error) (runstore.Attempt, error) {
	return r.finishStartedProcessError(ctx, err, nil)
}

func (r *workerRunner) finishStartedProcessError(ctx context.Context, err, logErr error) (runstore.Attempt, error) {
	terminateProcessGroup(r.cmd.Process.Pid)
	_, _ = r.cmd.Process.Wait()
	exitState := exitStateStartFailed
	if isContextError(err) {
		exitState = exitStateCanceled
	}
	return finishProcessErrorAttempt(ctx, r.loaded.Store, r.loaded.Run.ID, r.attempt.AttemptID, exitState, r.logRef, r.at, err, logErr)
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

func openStreamingLog(ctx context.Context, store *runstore.Store, run *runstore.Run, attempt runstore.Attempt, at time.Time) (runstore.ArtifactRef, *os.File, error) {
	ref, err := store.WriteArtifactContext(ctx, run.ID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    attempt.StepID + "-" + attempt.AttemptID,
		Content: nil,
		Time:    at,
	})
	if err != nil {
		return runstore.ArtifactRef{}, nil, err
	}
	_, _, err = store.RecordAttemptLogContext(ctx, run.ID, runstore.AttemptLogRequest{
		AttemptID: attempt.AttemptID,
		LogRef:    ref,
		Time:      at,
	})
	if err != nil {
		return ref, nil, err
	}
	file, err := store.OpenArtifactAppendContext(ctx, run.ID, ref)
	if err != nil {
		return ref, nil, err
	}
	return ref, file, nil
}

func loggerOrNop(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}

func recoverOrRefuseActiveAttempt(ctx context.Context, store *runstore.Store, run *runstore.Run, logger *zap.Logger) (Result, error) {
	active := *run.Status.ActiveAttempt
	if attemptStillStarting(active, time.Now().UTC()) {
		return Result{RunID: run.ID, Attempt: active}, stableerr.Errorf("run %q already has starting attempt %q", run.ID, active.AttemptID)
	}
	if active.PID > 0 && processIdentityMatches(active.PID, active.ProcessStartTime) {
		if attemptTimedOut(active, time.Now().UTC()) {
			terminateProcessGroup(active.PID)
			recovered, err := recoverActiveAttempt(ctx, store, run, active, runstore.AttemptStateTimedOut, resultTimeout, exitStateTimeout, logger)
			return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, err
		}
		return Result{RunID: run.ID, Attempt: active}, stableerr.Errorf("run %q already has active attempt %q", run.ID, active.AttemptID)
	}
	recovered, err := recoverActiveAttempt(ctx, store, run, active, runstore.AttemptStateProcessError, resultProcessError, exitStateUnknown, logger)
	if err != nil {
		return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, err
	}
	return Result{RunID: run.ID, Attempt: recovered, Recovered: true}, nil
}

func recoverActiveAttempt(ctx context.Context, store *runstore.Store, run *runstore.Run, active runstore.Attempt, state, result, exitState string, logger *zap.Logger) (runstore.Attempt, error) {
	recovered, _, err := store.RecoverAttemptContext(ctx, run.ID, runstore.FinishAttemptRequest{
		AttemptID: active.AttemptID,
		State:     state,
		Status:    workflow.ReportStatusFailed,
		Result:    result,
		ExitState: exitState,
		LogRef:    active.LogRef,
		Time:      time.Now().UTC(),
	})
	if err == nil {
		logger.Debug("recovered active attempt",
			zap.String("run_id", run.ID),
			zap.String("step_id", active.StepID),
			zap.String("agent_id", active.AgentID),
			zap.String("attempt_id", active.AttemptID),
			zap.String("recovered_state", state),
			zap.String("recovered_result", result),
			zap.String("exit_state", exitState),
		)
	}
	return recovered, err
}

func loadAttemptByID(ctx context.Context, store *runstore.Store, runID, attemptID string, match func(runstore.Attempt) bool) (runstore.Attempt, bool, error) {
	run, err := store.LoadContext(ctx, runID)
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
