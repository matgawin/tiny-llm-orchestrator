package runskip

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestSkipPersistsAuditedTransition(t *testing.T) {
	root := writeSkipProject(t, true, "review", "")
	store := openSkipStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{RunID: "skip-ok", Workflow: "implementation", InitialState: "plan"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	at := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	result, err := Skip(context.Background(), Options{
		Root:   root,
		RunID:  run.ID,
		StepID: "plan",
		Reason: "  not worth another review  ",
		Source: "unit-test",
		Time:   at,
	})
	if err != nil {
		t.Fatalf("Skip returned error: %v", err)
	}

	if result.Event.Type != "workflow.step_skipped" {
		t.Fatalf("event type = %q, want workflow.step_skipped", result.Event.Type)
	}
	if result.Status.State != "running" {
		t.Fatalf("state = %q, want running", result.Status.State)
	}
	if len(result.Status.Attempts) != 0 {
		t.Fatalf("attempts = %d, want unchanged empty attempts", len(result.Status.Attempts))
	}
	if len(result.Status.SkippedSteps) != 1 {
		t.Fatalf("skipped steps = %d, want 1", len(result.Status.SkippedSteps))
	}
	skipped := result.Status.SkippedSteps[0]
	if skipped.StepID != "plan" || skipped.Status != "done" || skipped.Result != "skipped" || skipped.Reason != "not worth another review" || skipped.Source != "unit-test" {
		t.Fatalf("skipped = %+v, want trimmed audited skip", skipped)
	}
	entry := result.Status.WorkflowLoop.Entries[len(result.Status.WorkflowLoop.Entries)-1]
	if entry.State != "review" || entry.PreviousState != "plan" || entry.TriggerStatus != "done" || entry.TriggerResult != "skipped" {
		t.Fatalf("workflow entry = %+v, want done/skipped transition to review", entry)
	}
}

func TestSkipConsumesPriorOutcomeWhenSelectedStepCameFromRouting(t *testing.T) {
	root := writeSkipRoutingProject(t)
	store := openSkipStore(t, root)
	run, err := store.Create(runstore.CreateRunRequest{RunID: "skip-routed-step", Workflow: "implementation", InitialState: "plan"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	recordReportedAttempt(t, store, run.ID, "plan-attempt", "plan", "planner", "done", "ready")

	before, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load before returned error: %v", err)
	}
	if outcome, ok := runstore.LatestConsumableOutcome(before.Status); !ok || outcome.AttemptID != "plan-attempt" {
		t.Fatalf("latest consumable outcome = %+v ok=%v, want plan-attempt", outcome, ok)
	}

	result, err := Skip(context.Background(), Options{
		Root:   root,
		RunID:  run.ID,
		StepID: "review",
		Reason: "not worth another pass",
	})
	if err != nil {
		t.Fatalf("Skip returned error: %v", err)
	}

	if len(result.Status.Attempts) != 1 {
		t.Fatalf("attempts = %d, want unchanged single prior attempt", len(result.Status.Attempts))
	}
	if got := result.Status.Attempts[0].ConsumedByEvent; got != result.Event.Sequence {
		t.Fatalf("consumed_by_event = %d, want skip event sequence %d", got, result.Event.Sequence)
	}
	if _, ok := runstore.LatestConsumableOutcome(result.Status); ok {
		t.Fatal("LatestConsumableOutcome ok = true after skip, want consumed prior outcome")
	}
	state := runstate.WorkflowState(result.Status)
	if state.Outcome != nil || state.SelectedStep != "code" {
		t.Fatalf("workflow state = %+v, want selected code with no pending outcome", state)
	}

	loaded, err := store.Load(run.ID)
	if err != nil {
		t.Fatalf("Load after returned error: %v", err)
	}
	if loaded.Status.Attempts[0].ConsumedByEvent != result.Event.Sequence {
		t.Fatalf("replayed consumed_by_event = %d, want %d", loaded.Status.Attempts[0].ConsumedByEvent, result.Event.Sequence)
	}
	replayedState := runstate.WorkflowState(loaded.Status)
	if replayedState.Outcome != nil || replayedState.SelectedStep != "code" {
		t.Fatalf("replayed workflow state = %+v, want selected code with no pending outcome", replayedState)
	}
}

func TestSkipRejectsIneligibleStateWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, store *runstore.Store, runID string)
		want  string
	}{
		{
			name: "active attempt",
			setup: func(t *testing.T, store *runstore.Store, runID string) {
				t.Helper()
				if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
					StepID:          "plan",
					AgentID:         "planner",
					AttemptID:       "attempt-active",
					Timeout:         time.Minute,
					ReportExitGrace: time.Second,
				}); err != nil {
					t.Fatalf("StartAttempt returned error: %v", err)
				}
			},
			want: "active attempt",
		},
		{
			name: "terminal run",
			setup: func(t *testing.T, store *runstore.Store, runID string) {
				t.Helper()
				if _, _, err := store.UpdateStatus(runID, runstore.StatusUpdate{State: "ready_for_human"}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			},
			want: "terminal",
		},
		{
			name: "retry decision",
			setup: func(t *testing.T, store *runstore.Store, runID string) {
				t.Helper()
				if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
					StepID:          "plan",
					AgentID:         "planner",
					AttemptID:       "attempt-retry",
					Timeout:         time.Minute,
					ReportExitGrace: time.Second,
				}); err != nil {
					t.Fatalf("StartAttempt returned error: %v", err)
				}
				promptRef, err := store.WriteArtifact(runID, runstore.Artifact{Kind: runstore.KindPrompt, Name: "plan", Content: []byte("prompt\n")})
				if err != nil {
					t.Fatalf("WriteArtifact prompt returned error: %v", err)
				}
				if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{AttemptID: "attempt-retry", PromptRef: promptRef}); err != nil {
					t.Fatalf("RecordAttemptPrompt returned error: %v", err)
				}
				logRef, err := store.WriteArtifact(runID, runstore.Artifact{Kind: runstore.KindLog, Name: "plan", Content: []byte("log\n")})
				if err != nil {
					t.Fatalf("WriteArtifact log returned error: %v", err)
				}
				if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{AttemptID: "attempt-retry", LogRef: logRef}); err != nil {
					t.Fatalf("RecordAttemptLog returned error: %v", err)
				}
				if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
					AttemptID:        "attempt-retry",
					PID:              123,
					ProcessStartTime: "123456789",
				}); err != nil {
					t.Fatalf("RecordAttemptProcess returned error: %v", err)
				}
				if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
					Report: runstore.Report{
						RunID:     runID,
						StepID:    "plan",
						AgentID:   "planner",
						AttemptID: "attempt-retry",
						Status:    "failed",
						Result:    "error",
						Summary:   "retry",
					},
					State: "reported",
				}); err != nil {
					t.Fatalf("RecordAttemptReport returned error: %v", err)
				}
			},
			want: "only a selected step can be skipped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeSkipProject(t, true, "review", "failed/error: 1")
			store := openSkipStore(t, root)
			run, err := store.Create(runstore.CreateRunRequest{RunID: strings.ReplaceAll(tt.name, " ", "-"), Workflow: "implementation", InitialState: "plan"})
			if err != nil {
				t.Fatalf("Create returned error: %v", err)
			}
			tt.setup(t, store, run.ID)
			before, err := store.Load(run.ID)
			if err != nil {
				t.Fatalf("Load before returned error: %v", err)
			}

			_, err = Skip(context.Background(), Options{Root: root, RunID: run.ID, StepID: "plan", Reason: "nope"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Skip error = %v, want containing %q", err, tt.want)
			}
			after, err := store.Load(run.ID)
			if err != nil {
				t.Fatalf("Load after returned error: %v", err)
			}
			if after.Status.LastSequence != before.Status.LastSequence || len(after.Status.SkippedSteps) != len(before.Status.SkippedSteps) {
				t.Fatalf("state mutated after rejection: before seq=%d skips=%d after seq=%d skips=%d", before.Status.LastSequence, len(before.Status.SkippedSteps), after.Status.LastSequence, len(after.Status.SkippedSteps))
			}
		})
	}
}

func TestSkipRejectsWrongStepNonSkippableAndBlankReason(t *testing.T) {
	tests := []struct {
		name      string
		skippable bool
		stepID    string
		reason    string
		want      string
	}{
		{name: "wrong step", skippable: true, stepID: "review", reason: "skip", want: `selected step is "plan"`},
		{name: "non skippable", skippable: false, stepID: "plan", reason: "skip", want: "not skippable"},
		{name: "blank reason", skippable: true, stepID: "plan", reason: " \n\t ", want: "skip reason is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeSkipProject(t, tt.skippable, "review", "")
			store := openSkipStore(t, root)
			run, err := store.Create(runstore.CreateRunRequest{RunID: strings.ReplaceAll(tt.name, " ", "-"), Workflow: "implementation", InitialState: "plan"})
			if err != nil {
				t.Fatalf("Create returned error: %v", err)
			}

			_, err = Skip(context.Background(), Options{Root: root, RunID: run.ID, StepID: tt.stepID, Reason: tt.reason})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Skip error = %v, want containing %q", err, tt.want)
			}
			loaded, err := store.Load(run.ID)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if loaded.Status.LastSequence != run.Status.LastSequence || len(loaded.Status.SkippedSteps) != 0 {
				t.Fatalf("state mutated after rejection: %+v", loaded.Status)
			}
		})
	}
}

func recordReportedAttempt(t *testing.T, store *runstore.Store, runID, attemptID, stepID, agentID, status, result string) {
	t.Helper()
	if _, _, err := store.StartAttempt(runID, runstore.StartAttemptRequest{
		StepID:          stepID,
		AgentID:         agentID,
		AttemptID:       attemptID,
		Timeout:         time.Minute,
		ReportExitGrace: time.Second,
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
	promptRef, err := store.WriteArtifact(runID, runstore.Artifact{Kind: runstore.KindPrompt, Name: stepID, Content: []byte("prompt\n")})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptPrompt(runID, runstore.AttemptPromptRequest{AttemptID: attemptID, PromptRef: promptRef}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}
	logRef, err := store.WriteArtifact(runID, runstore.Artifact{Kind: runstore.KindLog, Name: stepID, Content: []byte("log\n")})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptLog(runID, runstore.AttemptLogRequest{AttemptID: attemptID, LogRef: logRef}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptProcess(runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              123,
		ProcessStartTime: "123456789",
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}
	if _, _, err := store.RecordAttemptReport(runID, runstore.RecordReportRequest{
		Report: runstore.Report{
			RunID:     runID,
			StepID:    stepID,
			AgentID:   agentID,
			AttemptID: attemptID,
			Status:    status,
			Result:    result,
			Summary:   "reported",
		},
		State: runstore.AttemptStateReported,
	}); err != nil {
		t.Fatalf("RecordAttemptReport returned error: %v", err)
	}
}

func openSkipStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func writeSkipRoutingProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeSkipFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n  reviewer: agents/reviewer.md\n  coder: agents/coder.md\n")
	writeSkipFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Planner.\n---\n\nPlan.\n")
	writeSkipFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), "---\nid: reviewer\nrole: reviewer\ndescription: Reviewer.\n---\n\nReview.\n")
	writeSkipFile(t, filepath.Join(orcDir, "agents", "coder.md"), "---\nid: coder\nrole: coder\ndescription: Coder.\n---\n\nCode.\n")
	writeSkipFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), string(readSkipTestdata(t, "routing_workflow.yaml")))
	return root
}

func writeSkipProject(t *testing.T, skippable bool, skipTarget, retryLine string) string {
	t.Helper()
	root := t.TempDir()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	writeSkipFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n  reviewer: agents/reviewer.md\n")
	writeSkipFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Planner.\n---\n\nPlan.\n")
	writeSkipFile(t, filepath.Join(orcDir, "agents", "reviewer.md"), "---\nid: reviewer\nrole: reviewer\ndescription: Reviewer.\n---\n\nReview.\n")
	retries := "{}"
	if retryLine != "" {
		retries = "\n    " + retryLine
	}
	skipFields := ""
	doneResults := "ready"
	onSkip := ""
	if skippable {
		skipFields = "    skippable: true\n"
		doneResults = "ready, skipped"
		onSkip = "      done/skipped: " + skipTarget + "\n"
	}
	writeSkipFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  retries: `+retries+`
steps:
  plan:
    agent: planner
`+skipFields+`    allowed_results:
      done: [`+doneResults+`]
      failed: [error]
    on:
      done/ready: review
`+onSkip+`      failed/error: blocked_for_human
  review:
    agent: reviewer
    allowed_results:
      done: [approved]
    on:
      done/approved: ready_for_human
`)
	return root
}

func writeSkipFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readSkipTestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve skip testdata path")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return content
}
