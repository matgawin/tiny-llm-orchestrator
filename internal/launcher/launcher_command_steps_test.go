package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestLaunchNextExecutesSuccessfulCommandStep(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"sh", "-c", "printf stdout-$ORC_TEST; printf stderr >&2"},
		Env:  map[string]string{"ORC_TEST": "override"},
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}

	if result.Attempt.State != runstore.AttemptStateReported || result.Attempt.Status != launcherStatusDone || result.Attempt.Result != resultCommandPassed {
		t.Fatalf("attempt = %+v, want reported done/passed", result.Attempt)
	}

	if result.Attempt.Report == nil || !strings.Contains(result.Attempt.Report.Summary, "command step finished with done/passed") {
		t.Fatalf("report = %+v, want system command report", result.Attempt.Report)
	}

	if result.Attempt.ReportRef == nil || result.Attempt.Report.ReportRef == nil || *result.Attempt.Report.ReportRef != *result.Attempt.ReportRef {
		t.Fatalf("report refs = %+v/%+v, want canonical report artifact refs", result.Attempt.ReportRef, result.Attempt.Report)
	}

	loaded := loadLauncherRun(t, root, runID)

	reportContent := string(readLauncherArtifact(t, root, runID, *result.Attempt.ReportRef))
	for _, want := range []string{
		"# Worker Report\n",
		"## Metadata\n",
		"## Summary\n\ncommand step finished with done/passed",
		"## Commands\n\n- sh -c printf stdout-$ORC_TEST; printf stderr >&2",
		"## Tests\n\n- command step finished with done/passed",
	} {
		if !strings.Contains(reportContent, want) {
			t.Fatalf("report content missing %q:\n%s", want, reportContent)
		}
	}

	assertLogArtifactContains(t, root, loaded, result.Attempt.AttemptID, "stdout", "stdout-override")
	assertLogArtifactContains(t, root, loaded, result.Attempt.AttemptID, "stderr", "stderr")
}

func TestLaunchNextResolvesCommandStepFromConfiguredPATH(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"orc-test-command"},
		Env:  map[string]string{"PATH": "tools"},
	})
	writeLauncherExecutable(t, filepath.Join(root, "tools", "orc-test-command"), "#!/bin/sh\nprintf command-path-override\n")

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if result.Attempt.State != runstore.AttemptStateReported || result.Attempt.Status != launcherStatusDone || result.Attempt.Result != resultCommandPassed {
		t.Fatalf("attempt = %+v, want reported done/passed", result.Attempt)
	}

	loaded := loadLauncherRun(t, root, runID)
	assertLogArtifactContains(t, root, loaded, result.Attempt.AttemptID, "stdout", "command-path-override")
}

func TestLaunchNextExecutesScriptStep(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Kind:       config.StepKindScript,
		ScriptPath: "scripts/check.sh",
		ScriptArgs: []string{"alpha"},
		Env:        map[string]string{"ORC_TEST": "script-env"},
	})
	writeLauncherExecutable(t, filepath.Join(root, "scripts", "check.sh"), "#!/bin/sh\nprintf 'script-%s-%s' \"$1\" \"$ORC_TEST\"\n")

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if result.Attempt.State != runstore.AttemptStateReported || result.Attempt.Status != launcherStatusDone || result.Attempt.Result != resultCommandPassed {
		t.Fatalf("attempt = %+v, want reported done/passed", result.Attempt)
	}

	if result.Attempt.Report == nil || !strings.Contains(result.Attempt.Report.Summary, "script step finished with done/passed") {
		t.Fatalf("report = %+v, want system script report", result.Attempt.Report)
	}

	loaded := loadLauncherRun(t, root, runID)
	assertLogArtifactContains(t, root, loaded, result.Attempt.AttemptID, "stdout", "script-alpha-script-env")
}

func TestLaunchNextRoutesFailingCommandStepBackToCode(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"sh", "-c", "echo nope >&2; exit 7"},
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if result.Attempt.Status != workflow.ReportStatusDone || result.Attempt.Result != resultCommandFailed {
		t.Fatalf("attempt outcome = %s/%s, want done/failed", result.Attempt.Status, result.Attempt.Result)
	}

	if result.Attempt.ExitCode == nil || *result.Attempt.ExitCode != 7 || result.Attempt.ExitState != exitStateExited {
		t.Fatalf("attempt exit = code %+v state %q, want 7/exited", result.Attempt.ExitCode, result.Attempt.ExitState)
	}

	if result.Attempt.Report == nil || !strings.Contains(result.Attempt.Report.Summary, "stderr tail:\nnope") {
		t.Fatalf("summary = %q, want stderr tail", result.Attempt.Report.Summary)
	}

	loaded, err := runcontext.Load(root, runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	decision, err := workflow.Evaluate(loaded.Workflow, runstate.WorkflowState(loaded.Run.Status))
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}

	if decision.Kind != workflow.DecisionSelectStep || decision.Step != launcherCodeStep {
		t.Fatalf("decision = %+v, want select code", decision)
	}

	reloaded := loadLauncherRun(t, root, runID)

	got := reloaded.Status.Attempts[len(reloaded.Status.Attempts)-1]
	if got.ExitCode == nil || *got.ExitCode != 7 || got.ExitState != exitStateExited {
		t.Fatalf("reloaded attempt exit = code %+v state %q, want 7/exited", got.ExitCode, got.ExitState)
	}
}

func TestLaunchNextUndeclaredCommandOutcomePersistsOriginalConfigError(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"sh", "-c", "echo nope >&2; exit 7"},
		AllowedResults: `    allowed_results:
      done: [passed]
      failed: [timeout, process_error]
`,
		On: `    on:
      done/passed: ready_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
`,
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err == nil || !strings.Contains(err.Error(), `step generated outcome "done/failed" is not declared in allowed_results`) {
		t.Fatalf("LaunchNext error = %v, want undeclared generated outcome", err)
	}

	if result.Attempt.State != runstore.AttemptStateReported ||
		result.Attempt.Status != workflow.ReportStatusDone ||
		result.Attempt.Result != resultCommandFailed {
		t.Fatalf("attempt = %+v, want original reported done/failed", result.Attempt)
	}

	if result.Attempt.Report == nil || !strings.Contains(result.Attempt.Report.Summary, "command step finished with done/failed") {
		t.Fatalf("report = %+v, want original generated outcome report", result.Attempt.Report)
	}

	loaded, err := runcontext.Load(root, runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if _, err := workflow.Evaluate(loaded.Workflow, runstate.WorkflowState(loaded.Run.Status)); err == nil ||
		!strings.Contains(err.Error(), `step "check" outcome "done/failed" is not declared in allowed_results`) {
		t.Fatalf("Evaluate error = %v, want undeclared original outcome config error", err)
	}
}

func TestLaunchNextRecordsSystemReportForCommandSpawnError(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Argv: []string{"/definitely-missing-orc-command"},
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err == nil || !strings.Contains(err.Error(), "definitely-missing-orc-command") {
		t.Fatalf("LaunchNext error = %v, want missing command error", err)
	}

	if result.Launched {
		t.Fatal("Launched = true, want false for spawn error")
	}

	if result.Attempt.State != runstore.AttemptStateReported ||
		result.Attempt.Status != workflow.ReportStatusFailed ||
		result.Attempt.Result != resultProcessError ||
		result.Attempt.ExitState != exitStateStartFailed {
		t.Fatalf("attempt = %+v, want reported failed/process_error start_failed", result.Attempt)
	}

	if result.Attempt.Report == nil || !strings.Contains(result.Attempt.Report.Summary, "command step finished with failed/process_error") {
		t.Fatalf("report = %+v, want system process_error report", result.Attempt.Report)
	}

	reloaded := loadLauncherRun(t, root, runID)

	got := reloaded.Status.Attempts[len(reloaded.Status.Attempts)-1]
	if got.Report == nil || got.ExitState != exitStateStartFailed {
		t.Fatalf("reloaded attempt = %+v, want persisted report and exit state", got)
	}
}

func TestLaunchNextMapsCommandTimeout(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Timeout: "20ms",
		Argv:    []string{"sh", "-c", "sleep 1"},
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if result.Attempt.State != runstore.AttemptStateReported || result.Attempt.Status != workflow.ReportStatusFailed || result.Attempt.Result != resultTimeout {
		t.Fatalf("attempt = %+v, want reported failed/timeout", result.Attempt)
	}
}

func TestLaunchNextCanceledCommandPersistsTerminalReport(t *testing.T) {
	root, runID := createCommandLauncherRun(t, commandWorkflowOptions{
		Timeout: "5s",
		Argv: []string{
			"sh",
			"-c",
			"printf cancel-before-sleep; touch command-ready; sleep 5",
		},
	})
	readyPath := filepath.Join(root, "command-ready")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan launchOutcome, 1)

	go func() {
		result, err := LaunchNext(ctx, Options{
			Root:  root,
			RunID: runID,
			Time:  fixedLauncherTime(),
		})
		done <- launchOutcome{result: result, err: err}
	}()

	eventually(t, time.Second, func() bool {
		_, err := os.Stat(readyPath)
		return err == nil
	})
	cancel()

	var outcome launchOutcome
	select {
	case outcome = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("LaunchNext did not return after command cancellation")
	}

	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("LaunchNext error = %v, want context.Canceled", outcome.err)
	}

	if outcome.result.Attempt.State != runstore.AttemptStateReported ||
		outcome.result.Attempt.Status != workflow.ReportStatusFailed ||
		outcome.result.Attempt.Result != resultProcessError ||
		outcome.result.Attempt.ExitState != exitStateCanceled {
		t.Fatalf("attempt = %+v, want reported canceled process_error", outcome.result.Attempt)
	}

	if outcome.result.Attempt.Report == nil || !strings.Contains(outcome.result.Attempt.Report.Summary, "command step finished with failed/process_error") {
		t.Fatalf("report = %+v, want persisted process_error report", outcome.result.Attempt.Report)
	}

	loaded := loadLauncherRun(t, root, runID)
	if loaded.Status.ActiveAttempt != nil {
		t.Fatalf("active attempt = %+v, want cleared", loaded.Status.ActiveAttempt)
	}

	got := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if got.Report == nil || got.ExitState != exitStateCanceled {
		t.Fatalf("persisted attempt = %+v, want canceled report", got)
	}

	assertLogArtifactContains(t, root, loaded, outcome.result.Attempt.AttemptID, "stdout", "cancel-before-sleep")
}
