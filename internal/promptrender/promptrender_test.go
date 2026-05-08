package promptrender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

func TestRenderSelectedPlanPromptPersistsContractAndContext(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n\nBuild prompt rendering.\n", fixedPromptTime().Add(time.Minute))
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindReport,
		Name:    "plan",
		Content: []byte("# Prior Plan\n\nUse existing run-store artifacts.\n"),
		Time:    fixedPromptTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteArtifact report returned error: %v", err)
	}

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
		Time:      fixedPromptTime().Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if result.Ref.Kind != runstore.KindPrompt || result.Ref.Path != "prompts/000004-plan.md" {
		t.Fatalf("prompt ref = %+v, want sequence 4 plan prompt", result.Ref)
	}
	persisted := readPromptFile(t, filepath.Join(root, ".orc", "runs", runID, filepath.FromSlash(result.Ref.Path)))
	if !bytes.Equal(result.Content, persisted) {
		t.Fatal("returned content differs from persisted prompt")
	}
	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{
		"# Tiny Orc Worker Prompt\n",
		"- run_id: `prompt-run`\n",
		"- step_id: `plan`\n",
		"- agent_id: `planner`\n",
		"- attempt_id: `attempt-001`\n",
		"Creates implementation plans and scope boundaries.",
		"Plan the work and report readiness.",
		"# Task\n\nBuild prompt rendering.",
		"### reports/000003-plan.md\n",
		"# Prior Plan\n\nUse existing run-store artifacts.",
		"`done/ready`",
		"`blocked/blocked`",
		"## Live Progress\n",
		"`orc progress <short update>`",
		"starting analysis, choosing an approach, beginning tests, or finding a blocker",
		"Do not stream logs, file lists, diffs, frequent heartbeat messages, or routine chatter",
		"`ORC_PROGRESS_SOCKET`",
		"`ORC_PROGRESS_TOKEN`",
		"orc report --run prompt-run --step plan --agent planner --attempt attempt-001 --status <status> --result <result> --summary \"<summary>\"",
		"Optional structured report fields:",
		"`--changed-path <path>`",
		"`--command <command>`",
		"`--test <test>`",
		"`--risk <risk>`",
		"`--follow-up <title>`",
		"`--report-file <path>`",
		"`orc report --json-file <path>`",
		"Do not combine `--json-file` with report field flags.",
	})
	for _, reserved := range []string{"done/skipped", "failed/error", "failed/invalid_report", "failed/missing_report", "failed/timeout", "failed/process_error"} {
		if strings.Contains(prompt, "`"+reserved+"`") {
			t.Fatalf("prompt includes system-owned report outcome %s:\n%s", reserved, prompt)
		}
	}

	loaded, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Status.LastSequence != 4 {
		t.Fatalf("last sequence = %d, want 4", loaded.Status.LastSequence)
	}
	if got := latestArtifactKind(loaded.Status.Artifacts); got != runstore.KindPrompt {
		t.Fatalf("latest artifact kind = %s, want prompt", got)
	}
	if got := latestEventType(t, loaded.Events); got != "artifact.written" {
		t.Fatalf("latest event type = %s, want artifact.written", got)
	}
}

func TestRenderTestStepUsesStepSpecificAllowedResultsWhenAllowed(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())

	result, err := Render(context.Background(), Options{
		Root:                root,
		RunID:               runID,
		StepID:              "test",
		AgentID:             "tester",
		AttemptID:           "attempt-test",
		AllowUnselectedStep: true,
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{
		"- step_id: `test`\n",
		"- agent_id: `tester`\n",
		"`done/passed`",
		"`done/failed`",
		"`blocked/blocked`",
	})
	if strings.Contains(prompt, "approved") || strings.Contains(prompt, "changes_requested") {
		t.Fatalf("tester prompt includes reviewer-only results:\n%s", prompt)
	}
}

func TestRenderIncludesStructuredPriorReportWithoutReportArtifact(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	recordReportedAttempt(t, store, runID, runstore.Report{
		RunID:        runID,
		StepID:       "plan",
		AgentID:      "planner",
		AttemptID:    "attempt-plan",
		Status:       "done",
		Result:       "ready",
		Summary:      "Plan is ready for verification.",
		Commands:     []string{"go test ./internal/promptrender"},
		Tests:        []string{"prompt renderer package tests passed"},
		Risks:        []string{"none"},
		ChangedPaths: []string{"internal/promptrender/promptrender.go"},
		Followups:    []runstore.Followup{{Title: "Document prompt report context", Details: "Keep docs aligned with renderer behavior."}},
	}, nil)

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "test",
		AgentID:   "tester",
		AttemptID: "attempt-test",
		Time:      fixedPromptTime().Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{
		"## Prior Report Context",
		"### attempt attempt-plan (plan done/ready)",
		"- step_id: `plan`",
		"- agent_id: `planner`",
		"- status/result: `done/ready`",
		"- summary: Plan is ready for verification.",
		"- command: go test ./internal/promptrender",
		"- test: prompt renderer package tests passed",
		"- risk: none",
		"- changed_path: internal/promptrender/promptrender.go",
		"- follow_up: Document prompt report context",
		"- follow_up_details: Keep docs aligned with renderer behavior.",
	})
	if strings.Contains(prompt, "### reports/") {
		t.Fatalf("prompt includes report artifact context, want structured report only:\n%s", prompt)
	}
}

func TestRenderIncludesWorkflowLoopContextAfterSoftCap(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	store := openPromptStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "prompt-run",
		Workflow:     "implementation",
		InitialState: "plan",
		Time:         fixedPromptTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	runID := run.ID
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	recordReportedLoopPromptAttempt(t, store, runID, "attempt-1", "")
	recordReportedLoopPromptAttempt(t, store, runID, "attempt-2", "attempt-1")
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:           "plan",
		AgentID:          "planner",
		AttemptID:        "attempt-3",
		Timeout:          30 * time.Minute,
		ReportExitGrace:  30 * time.Second,
		Time:             fixedPromptTime().Add(3 * time.Minute),
		ConsumeAttemptID: "attempt-2",
		WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
			State:         "plan",
			PreviousState: "plan",
			TriggerStatus: "done",
			TriggerResult: "ready",
		},
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-3",
		Time:      fixedPromptTime().Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	assertPromptContainsAll(t, string(result.Content), []string{
		"## Workflow Loop Context",
		"- workflow: `implementation`",
		"- repeated_state: `plan`",
		"- current_count: `3`",
		"- soft_cap: `2`",
		"- hard_cap: `4`",
		"- prior_statuses: `done/ready`, `done/ready`",
		"break the loop with new information",
	})
}

func TestRenderIncludesSkippedStepPriorContext(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n\nBuild prompt rendering.\n", fixedPromptTime().Add(time.Minute))
	if _, _, err := store.RecordStepSkip(runID, runstore.RecordStepSkipRequest{
		StepID: "plan",
		Reason: "not worth another review",
		Time:   fixedPromptTime().Add(2 * time.Minute),
	}, func(runstore.Status) (runstore.StepSkipTransition, error) {
		return runstore.StepSkipTransition{
			State: workflow.RunStatusRunning,
			WorkflowStateEntry: runstore.WorkflowStateEntryRequest{
				State:         "test",
				PreviousState: "plan",
				TriggerStatus: "done",
				TriggerResult: "skipped",
			},
		}, nil
	}); err != nil {
		t.Fatalf("RecordStepSkip returned error: %v", err)
	}

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "test",
		AgentID:   "tester",
		AttemptID: "attempt-002",
		Time:      fixedPromptTime().Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{
		"### step plan skipped\n",
		"step plan skipped by human decision: not worth another review",
	})
}

func TestRenderCombinesStructuredPriorReportWithReportArtifact(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	recordReportedAttempt(t, store, runID, runstore.Report{
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-plan",
		Status:    "done",
		Result:    "ready",
		Summary:   "Plan is ready for verification.",
	}, []byte("# Detail\n\nUse the focused test surface.\n"))

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "test",
		AgentID:   "tester",
		AttemptID: "attempt-test",
		Time:      fixedPromptTime().Add(8 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{
		"### attempt attempt-plan (plan done/ready)",
		"- summary: Plan is ready for verification.",
		"Report detail:",
		"# Detail\n\nUse the focused test surface.",
	})
	if strings.Contains(prompt, "### reports/") {
		t.Fatalf("prompt renders report artifact as a duplicate context entry:\n%s", prompt)
	}
}

func TestRenderRefusesNonSelectedStepUnlessAllowed(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())

	_, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "test",
		AgentID:   "tester",
		AttemptID: "attempt-test",
	})
	if err == nil {
		t.Fatal("Render returned nil error, want non-selected step refusal")
	}
	if !strings.Contains(err.Error(), `step "test" is not selected`) {
		t.Fatalf("error = %q, want non-selected step context", err)
	}
	loaded, loadErr := store.Load(runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := countArtifacts(loaded.Status.Artifacts, runstore.KindPrompt); got != 0 {
		t.Fatalf("prompt artifacts = %d, want none after refusal", got)
	}
}

func TestRenderRefusesTerminalRun(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusReadyForHuman)

	_, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
	})
	if err == nil {
		t.Fatal("Render returned nil error, want terminal refusal")
	}
	if !strings.Contains(err.Error(), "has no selected runnable step") {
		t.Fatalf("error = %q, want no selected runnable step", err)
	}
}

func TestRenderRequiresCallerProvidedAttemptMetadata(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)

	_, err := Render(context.Background(), Options{
		Root:    root,
		RunID:   runID,
		StepID:  "plan",
		AgentID: "planner",
	})
	if err == nil || !strings.Contains(err.Error(), "attempt id is required") {
		t.Fatalf("Render error = %v, want missing attempt id", err)
	}
}

func TestRenderHonorsCanceledContextBeforeWritingPrompt(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Render(ctx, Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Render error = %v, want context canceled", err)
	}
	loaded, loadErr := store.Load(runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if got := countArtifacts(loaded.Status.Artifacts, runstore.KindPrompt); got != 0 {
		t.Fatalf("prompt artifacts = %d, want none after canceled context", got)
	}
}

func TestRenderReturnsCommittedPromptRefOnStatusMaterializationFailure(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	runPath := filepath.Join(root, ".orc", "runs", runID)
	denyStatusMaterializationOrSkip(t, runPath)

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
	})
	var materializationErr *runstore.StatusMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("Render error = %T %v, want StatusMaterializationError", err, err)
	}
	if result.Ref.Kind != runstore.KindPrompt || result.Ref.Path == "" || result.Path == "" {
		t.Fatalf("result = %+v, want committed prompt ref despite error", result)
	}
	persisted := readPromptFile(t, filepath.Join(runPath, filepath.FromSlash(result.Ref.Path)))
	if !bytes.Equal(result.Content, persisted) {
		t.Fatal("returned content differs from committed prompt")
	}
}

func TestRenderRejectsInvalidRequestedMetadataBeforeWritingPrompt(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "undeclared step",
			opts: Options{
				StepID:              "deploy",
				AgentID:             "tester",
				AttemptID:           "attempt-001",
				AllowUnselectedStep: true,
			},
			want: `step "deploy" is not declared`,
		},
		{
			name: "agent mismatch",
			opts: Options{
				StepID:              "test",
				AgentID:             "planner",
				AttemptID:           "attempt-001",
				AllowUnselectedStep: true,
			},
			want: `step "test" uses agent "tester", not "planner"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePromptProject(t, root)
			runID := createPromptRun(t, root, workflow.RunStatusRunning)
			tt.opts.Root = root
			tt.opts.RunID = runID

			_, err := Render(context.Background(), tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render error = %v, want %q", err, tt.want)
			}
			store := openPromptStore(t, root)
			loaded, loadErr := store.Load(runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			if got := countArtifacts(loaded.Status.Artifacts, runstore.KindPrompt); got != 0 {
				t.Fatalf("prompt artifacts = %d, want none after invalid metadata", got)
			}
		})
	}
}

func TestRenderRequiresTaskContextArtifact(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)

	_, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-001",
	})
	if err == nil || !strings.Contains(err.Error(), "has no task context artifact") {
		t.Fatalf("Render error = %v, want missing task context", err)
	}
}

func TestShellQuoteQuotesOpaqueAttemptIDs(t *testing.T) {
	got := shellQuote("attempt with space")
	if got != "'attempt with space'" {
		t.Fatalf("shellQuote returned %q, want quoted opaque id", got)
	}
}

func writePromptProject(t *testing.T, root string) {
	t.Helper()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "runtimes"), 0o750); err != nil {
		t.Fatalf("create runtimes dir: %v", err)
	}
	writePromptFile(t, filepath.Join(orcDir, "config.yaml"), `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
  tester: agents/tester.md
  reviewer: agents/reviewer.md
runtimes:
  codex: runtimes/codex.yaml
`)
	writePromptFile(t, filepath.Join(orcDir, "runtimes", "codex.yaml"), testutil.CodexRuntimeYAML())
	writePromptFile(t, filepath.Join(orcDir, "agents", "planner.md"), `---
id: planner
role: planner
description: Creates implementation plans and scope boundaries.
---

Plan the work and report readiness.
`)
	writePromptFile(t, filepath.Join(orcDir, "agents", "tester.md"), `---
id: tester
role: tester
description: Runs verification and reports pass, fail, or blocked outcomes.
---

Run relevant tests and report exact command results.
`)
	writePromptFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), `---
id: reviewer
role: reviewer
description: Reviews completed work.
---

Review the change and report approval or requested changes.
`)
	writePromptFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), string(readPromptTestdata(t, "implementation_workflow.yaml")))
}

func createPromptRun(t *testing.T, root, state string) string {
	t.Helper()
	store := openPromptStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    "prompt-run",
		Workflow: "implementation",
		Time:     fixedPromptTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if state != workflow.RunStatusRunning {
		if _, _, err := store.UpdateStatus(run.ID, runstore.StatusUpdate{State: state, Time: fixedPromptTime().Add(time.Minute)}); err != nil {
			t.Fatalf("UpdateStatus returned error: %v", err)
		}
	}
	return run.ID
}

func openPromptStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func writeTaskContextArtifact(t *testing.T, store *runstore.Store, runID, content string, at time.Time) {
	t.Helper()
	if _, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte(content),
		Time:    at,
	}); err != nil {
		t.Fatalf("WriteArtifact task returned error: %v", err)
	}
}

func recordReportedAttempt(t *testing.T, store *runstore.Store, runID string, report runstore.Report, reportContent []byte) {
	t.Helper()
	// The prompt, log, and process records are run-store lifecycle preconditions
	// for a worker-authored report.
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          report.StepID,
		AgentID:         report.AgentID,
		AttemptID:       report.AttemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedPromptTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    report.StepID,
		Content: []byte("prior prompt\n"),
		Time:    fixedPromptTime().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: report.AttemptID,
		PromptRef: promptRef,
		Time:      fixedPromptTime().Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    report.StepID,
		Content: []byte("prior log\n"),
		Time:    fixedPromptTime().Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: report.AttemptID,
		LogRef:    logRef,
		Time:      fixedPromptTime().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        report.AttemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedPromptTime().Add(6 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	req := runstore.RecordReportRequest{
		State:  runstore.AttemptStateReported,
		Report: report,
		Time:   fixedPromptTime().Add(7 * time.Minute),
	}
	if reportContent != nil {
		req.ReportName = report.StepID
		req.ReportContent = reportContent
		req.ReportContentSet = true
	}
	if _, _, err := store.RecordAttemptReport(runID, req); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}
}

func recordReportedLoopPromptAttempt(t *testing.T, store *runstore.Store, runID, attemptID, consumeAttemptID string) {
	t.Helper()
	req := runstore.StartAttemptRequest{
		StepID:           "plan",
		AgentID:          "planner",
		AttemptID:        attemptID,
		Timeout:          30 * time.Minute,
		ReportExitGrace:  30 * time.Second,
		Time:             fixedPromptTime().Add(time.Minute),
		ConsumeAttemptID: consumeAttemptID,
	}
	if consumeAttemptID != "" {
		req.WorkflowStateEntry = runstore.WorkflowStateEntryRequest{
			State:         "plan",
			PreviousState: "plan",
			TriggerStatus: "done",
			TriggerResult: "ready",
		}
	}
	if _, _, err := store.StartAttempt(runID, req); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
		Time:    fixedPromptTime().Add(80 * time.Second),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedPromptTime().Add(85 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan",
		Content: []byte("log\n"),
		Time:    fixedPromptTime().Add(88 * time.Second),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedPromptTime().Add(89 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedPromptTime().Add(90 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		State: runstore.AttemptStateReported,
		Report: runstore.Report{
			RunID:     runID,
			StepID:    "plan",
			AgentID:   "planner",
			AttemptID: attemptID,
			Status:    "done",
			Result:    "ready",
			Summary:   "Looping.",
		},
		Time: fixedPromptTime().Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}
}

func latestArtifactKind(refs []runstore.ArtifactRef) runstore.ArtifactKind {
	if len(refs) == 0 {
		return ""
	}
	return refs[len(refs)-1].Kind
}

func latestEventType(t *testing.T, events []runstore.Event) string {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("events are empty")
	}
	var payload struct {
		Artifact runstore.ArtifactRef `json:"artifact"`
	}
	if err := json.Unmarshal(events[len(events)-1].Payload, &payload); err != nil {
		t.Fatalf("unmarshal latest payload: %v", err)
	}
	if payload.Artifact.Kind != runstore.KindPrompt {
		t.Fatalf("latest artifact payload = %+v, want prompt", payload.Artifact)
	}
	return events[len(events)-1].Type
}

func countArtifacts(refs []runstore.ArtifactRef, kind runstore.ArtifactKind) int {
	count := 0
	for _, ref := range refs {
		if ref.Kind == kind {
			count++
		}
	}
	return count
}

func assertPromptContainsAll(t *testing.T, prompt string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func readPromptFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func readPromptTestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve prompt testdata path")
	}
	return readPromptFile(t, filepath.Join(filepath.Dir(file), "testdata", name))
}

func writePromptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func denyStatusMaterializationOrSkip(t *testing.T, runPath string) {
	t.Helper()
	if err := os.Chmod(runPath, 0o500); err != nil {
		t.Fatalf("chmod run dir read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(runPath, 0o750)
	})
	temp, err := os.CreateTemp(runPath, ".status-probe-*.tmp")
	if err == nil {
		name := temp.Name()
		_ = temp.Close()
		_ = os.Remove(name)
		t.Skip("chmod did not deny temp file creation in run directory")
	}
}

func fixedPromptTime() time.Time {
	return time.Date(2026, 5, 3, 21, 30, 0, 0, time.UTC)
}
