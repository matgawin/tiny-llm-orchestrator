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
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	failureTailLines = 100
	failureTailBytes = 12 * 1024
)

func runDeterministicStep(ctx context.Context, loaded runcontext.Context, opts Options, attempt runstore.Attempt, step config.Step, at time.Time, progressEnv map[string]string) (runstore.Attempt, runstore.ArtifactRef, runstore.ArtifactRef, bool, error) {
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
	env = mergeEnv(env, progressEnv)
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
