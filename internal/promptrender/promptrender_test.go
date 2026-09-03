//nolint:goconst // Test strings are clearer in place.
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

	"tiny-llm-orchestrator/orc/internal/attemptdeadline"
	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runconfigrefresh"
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
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-001")

	if _, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
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

	if result.Ref.Kind != runstore.KindPrompt || result.Ref.Path != "prompts/000005-plan.md" {
		t.Fatalf("prompt ref = %+v, want sequence 5 plan prompt", result.Ref)
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
		"`done/ready`",
		"`blocked/blocked`",
		"## Live Progress\n",
		"`orc progress <short update>`",
		"starting analysis, choosing an approach, beginning tests, or finding a blocker",
		"Do not stream logs, file lists, diffs, frequent heartbeat messages, or routine chatter",
		"`ORC_PROGRESS_SOCKET`",
		"`ORC_PROGRESS_TOKEN`",
		"`ORC_PROJECT_ROOT`",
		"`ORC_ATTEMPT_STARTED_AT`",
		"`ORC_ATTEMPT_DEADLINE`",
		"`ORC_ATTEMPT_TIMEOUT`",
		"## Attempt Deadline",
		"- started_at: `2026-05-03T21:30:00Z`",
		"- deadline: `2026-05-03T22:00:00Z`",
		"- timeout: `30m0s`",
		"- calculated_at: `2026-05-03T21:33:00Z`",
		"- initial_remaining: `27m0s`",
		"- initial_phase: `NORMAL`",
		"- initial_action: `produce the smallest complete scope envelope`",
		"`orc time-left`",
		"deadline, elapsed time, remaining time, timeout, phase, and action guidance",
		"`--json`",
		"Final completion or blockage still goes through `orc report`.",
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

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if loaded.Status.LastSequence != 5 {
		t.Fatalf("last sequence = %d, want 5", loaded.Status.LastSequence)
	}

	if got := latestArtifactKind(loaded.Status.Artifacts); got != runstore.KindPrompt {
		t.Fatalf("latest artifact kind = %s, want prompt", got)
	}

	if got := latestEventType(t, loaded.Events); got != "artifact.written" {
		t.Fatalf("latest event type = %s, want artifact.written", got)
	}
}

func TestRenderUsesPinnedAgentDescriptorAfterLiveMutation(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n\nUse the pinned descriptor.\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-1")
	writePromptFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), `---
id: planner
role: planner
description: Live mutated planner.
---

LIVE MUTATED BODY
`)

	result, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "plan",
		AgentID:   "planner",
		AttemptID: "attempt-1",
		Time:      fixedPromptTime().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	prompt := string(result.Content)
	assertPromptContainsAll(t, prompt, []string{"Creates implementation plans and scope boundaries.", "Plan the work and report readiness."})

	if strings.Contains(prompt, "LIVE MUTATED BODY") {
		t.Fatalf("prompt used live mutated agent descriptor:\n%s", prompt)
	}
}

func TestRenderUsesAttemptPinnedRoleAfterConfigRefresh(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	writePromptFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), `---
id: planner
role: coder
description: Refreshed descriptor.
---

Implement the change.
`)

	if _, err := runconfigrefresh.Refresh(context.Background(), runconfigrefresh.Options{
		Root: root, RunID: runID, Source: "test", Time: fixedPromptTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-pinned")

	result, err := Render(context.Background(), Options{
		Root: root, RunID: runID, StepID: "plan", AgentID: "planner", AttemptID: "attempt-pinned",
		Time: fixedPromptTime().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if !strings.Contains(string(result.Content), "- initial_action: `produce the smallest complete scope envelope`") {
		t.Fatalf("prompt did not use the version 1 planning action:\n%s", result.Content)
	}
}

func TestRenderDeadlineCanStartExpired(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-expired")

	result, err := Render(context.Background(), Options{
		Root: root, RunID: runID, StepID: "plan", AgentID: "planner", AttemptID: "attempt-expired",
		Time: fixedPromptTime().Add(31 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	assertPromptContainsAll(t, string(result.Content), []string{
		"- initial_remaining: `-1m0s`",
		"- initial_phase: `REPORT_NOW`",
	})
}

func TestRenderNormalizesZeroCalculationTime(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-zero-time")

	before := time.Now().UTC()
	result, err := Render(context.Background(), Options{
		Root: root, RunID: runID, StepID: "plan", AgentID: "planner", AttemptID: "attempt-zero-time",
	})
	after := time.Now().UTC()

	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	prompt := string(result.Content)

	const calculatedAtPrefix = "- calculated_at: `"

	_, after0, ok := strings.Cut(prompt, calculatedAtPrefix)
	if !ok {
		t.Fatalf("prompt has no calculated_at field:\n%s", prompt)
	}

	calculatedAtText := strings.SplitN(after0, "`", 2)[0]

	calculatedAt, err := time.Parse(time.RFC3339Nano, calculatedAtText)
	if err != nil {
		t.Fatalf("parse calculated_at %q: %v", calculatedAtText, err)
	}

	if calculatedAt.Before(before) || calculatedAt.After(after) {
		t.Fatalf("calculated_at = %s, want between %s and %s", calculatedAt, before, after)
	}

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := loaded.Events[len(loaded.Events)-1].Time; !got.Equal(calculatedAt) {
		t.Fatalf("artifact event time = %s, want calculation time %s", got, calculatedAt)
	}
}

func TestAttemptDeadlineRoleLookupSupportsLegacyVersionAndReturnsSnapshotErrors(t *testing.T) {
	t.Run("legacy version zero", func(t *testing.T) {
		root := t.TempDir()
		writePromptProject(t, root)
		runID := createPromptRun(t, root, workflow.RunStatusRunning)
		store := openPromptStore(t, root)
		startPromptAttemptVersion(t, store, runID, "plan", "planner", "attempt-legacy", 0)

		loaded, err := store.LoadContext(context.Background(), runID)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		group, err := attemptdeadline.GroupForAttempt(loaded, *loaded.Status.ActiveAttempt)
		if err != nil || group != attemptdeadline.ActionGroupPlanning {
			t.Fatalf("GroupForAttempt = %q, %v, want planning", group, err)
		}
	})

	t.Run("missing snapshot version", func(t *testing.T) {
		root := t.TempDir()
		writePromptProject(t, root)
		runID := createPromptRun(t, root, workflow.RunStatusRunning)
		store := openPromptStore(t, root)
		startPromptAttemptVersion(t, store, runID, "plan", "planner", "attempt-missing", 2)

		loaded, err := store.LoadContext(context.Background(), runID)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		_, err = attemptdeadline.GroupForAttempt(loaded, *loaded.Status.ActiveAttempt)
		if err == nil || !strings.Contains(err.Error(), "config snapshot version 2") {
			t.Fatalf("GroupForAttempt error = %v, want missing version error", err)
		}
	})

	for _, tt := range []struct {
		name, stepID, agentID, want string
	}{
		{"missing step", "missing", "planner", `step "missing" is not present`},
		{"agent mismatch", "plan", "tester", `uses agent "planner", not attempt agent "tester"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePromptProject(t, root)
			runID := createPromptRun(t, root, workflow.RunStatusRunning)
			store := openPromptStore(t, root)
			startPromptAttemptVersion(t, store, runID, tt.stepID, tt.agentID, "attempt-invalid", 1)

			loaded, err := store.LoadContext(context.Background(), runID)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			_, err = attemptdeadline.GroupForAttempt(loaded, *loaded.Status.ActiveAttempt)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("GroupForAttempt error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Run("missing descriptor", func(t *testing.T) {
		root := t.TempDir()
		writePromptProject(t, root)
		runID := createPromptRun(t, root, workflow.RunStatusRunning)
		store := openPromptStore(t, root)
		startPromptAttempt(t, store, runID, "plan", "planner", "attempt-no-agent")
		mutatePromptResolvedSnapshot(t, root, runID, func(project *config.Project) {
			delete(project.Agents, "planner")
		})

		loaded, err := store.LoadContext(context.Background(), runID)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		_, err = attemptdeadline.GroupForAttempt(loaded, *loaded.Status.ActiveAttempt)
		if err == nil || !strings.Contains(err.Error(), `agent "planner" is not present`) {
			t.Fatalf("GroupForAttempt error = %v, want missing descriptor error", err)
		}
	})

	t.Run("deterministic attempt", func(t *testing.T) {
		root := t.TempDir()
		writePromptProject(t, root)
		runID := createPromptRun(t, root, workflow.RunStatusRunning)
		store := openPromptStore(t, root)
		startPromptAttempt(t, store, runID, "plan", "command", "attempt-command")
		mutatePromptResolvedSnapshot(t, root, runID, func(project *config.Project) {
			workflowConfig := project.Workflows["implementation"]
			step := workflowConfig.Steps["plan"]
			step.Kind = config.StepKindCommand
			step.Agent = ""
			workflowConfig.Steps["plan"] = step
			project.Workflows["implementation"] = workflowConfig
		})

		loaded, err := store.LoadContext(context.Background(), runID)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		group, err := attemptdeadline.GroupForAttempt(loaded, *loaded.Status.ActiveAttempt)
		if err != nil || group != attemptdeadline.ActionGroupGeneric {
			t.Fatalf("GroupForAttempt = %q, %v, want generic", group, err)
		}
	})
}

func TestRenderRejectsMissingOrMismatchedAttemptIdentity(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-plan")

	tests := []struct {
		name, step, agent, attempt, want string
	}{
		{"missing", "plan", "planner", "missing", `attempt "missing" is not present`},
		{"mismatch", "test", "tester", "attempt-plan", `attempt "attempt-plan" identity is plan/planner, not test/tester`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Render(context.Background(), Options{
				Root: root, RunID: runID, StepID: tt.step, AgentID: tt.agent, AttemptID: tt.attempt,
				AllowUnselectedStep: true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRenderTestStepUsesStepSpecificAllowedResultsWhenAllowed(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "test", "tester", "attempt-test")

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

func TestRenderReviewerReportContractDefinesFindings(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	startPromptAttempt(t, store, runID, "review", "reviewer", "attempt-review")

	result, err := Render(context.Background(), Options{
		Root: root, RunID: runID, StepID: "review", AgentID: "reviewer", AttemptID: "attempt-review", AllowUnselectedStep: true,
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	assertPromptContainsAll(t, string(result.Content), []string{
		`"finding_id": "time-left-missing-error-output"`,
		`"category": "correctness"`,
		`"path": "internal/cli/time_left.go"`,
		`"location": "executeTimeLeft"`,
		`"summary": "The command returns an error without writing it."`,
		"All five fields are required, must be non-empty, and must be trimmed.",
		"`finding_id` must match `[a-z0-9][a-z0-9._-]{0,63}` and must be unique in one report.",
		"`path` must be a clean project-relative slash path.",
		"It must not be absolute, contain a backslash or empty segment, or contain `.` or `..` segments.",
		"A `done/changes_requested` report requires one or more findings.",
		"A `done/approved` report must not contain findings.",
	})
}

func TestRenderIncludesStructuredPriorReportCanonicalArtifactPath(t *testing.T) {
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
	startPromptAttempt(t, store, runID, "test", "tester", "attempt-test")

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
		"Full report: `.orc/runs/prompt-run/reports/000009-plan.md`",
		"# Worker Report",
		"## Metadata",
		"- run_id: `prompt-run`",
		"- step_id: `plan`",
		"- agent_id: `planner`",
		"- status/result: `done/ready`",
		"## Summary\n\nPlan is ready for verification.",
		"## Commands\n\n- go test ./internal/promptrender",
		"## Tests\n\n- prompt renderer package tests passed",
		"## Risks\n\n- none",
		"## Changed Paths\n\n- internal/promptrender/promptrender.go",
		"## Follow-ups\n\n- Document prompt report context\n  Details: Keep docs aligned with renderer behavior.",
	})

	if strings.Contains(prompt, "### reports/") {
		t.Fatalf("prompt renders canonical attempt report as a duplicate artifact context:\n%s", prompt)
	}
}

func TestRenderIncludesWorkflowLoopContextAfterSoftCap(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	store := openPromptStore(t, root)

	run, err := store.CreateContext(context.Background(), runstore.CreateRunRequest{
		RunID:        "prompt-run",
		Workflow:     "implementation",
		InitialState: "plan",
		Time:         fixedPromptTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	runID := run.ID
	writePromptConfigSnapshot(t, root, store, runID)
	writeTaskContextArtifact(t, store, runID, "# Task\n", fixedPromptTime())
	recordReportedLoopPromptAttempt(t, store, runID, "attempt-1", "")
	recordReportedLoopPromptAttempt(t, store, runID, "attempt-2", "attempt-1")

	if _, _, err := store.StartAttemptContext(context.Background(), runID, runstore.StartAttemptRequest{
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

	if _, _, err := store.RecordStepSkipContext(context.Background(), runID, runstore.RecordStepSkipRequest{
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

	startPromptAttempt(t, store, runID, "test", "tester", "attempt-002")

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
	if strings.Contains(prompt, "not worth another review") {
		t.Fatalf("prompt contains skipped-step history:\n%s", prompt)
	}
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
	startPromptAttempt(t, store, runID, "test", "tester", "attempt-test")

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
		"Full report: `.orc/runs/prompt-run/reports/000009-plan.md`",
		"## Summary\n\nPlan is ready for verification.",
		"## Report Detail",
		"# Detail\n\nUse the focused test surface.",
	})

	if strings.Contains(prompt, "### reports/") {
		t.Fatalf("prompt renders report artifact as a duplicate context entry:\n%s", prompt)
	}
}

func TestRenderRequiresPriorAttemptReportRef(t *testing.T) {
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
		Summary:   "Plan is ready.",
	}, nil)
	removeAttemptReportedReportRef(t, root, runID)
	startPromptAttempt(t, store, runID, "test", "tester", "attempt-test")

	_, err := Render(context.Background(), Options{
		Root:      root,
		RunID:     runID,
		StepID:    "test",
		AgentID:   "tester",
		AttemptID: "attempt-test",
		Time:      fixedPromptTime().Add(8 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Render returned error for a report outside accepted selection: %v", err)
	}
}

func TestRenderTruncatedPriorReportRequiresReadingFullReport(t *testing.T) {
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
		Summary:   strings.Repeat("long summary ", 160),
	}, nil)
	startPromptAttempt(t, store, runID, "test", "tester", "attempt-test")

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

	assertPromptContainsAll(t, string(result.Content), []string{
		"Full report: `.orc/runs/prompt-run/reports/000009-plan.md`",
		strings.TrimSpace(strings.Repeat("long summary ", 160)),
	})
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

	if !strings.Contains(err.Error(), `step "`+"test"+`" is not selected`) {
		t.Fatalf("error = %q, want non-selected step context", err)
	}

	loaded, loadErr := store.LoadContext(context.Background(), runID)
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

	loaded, loadErr := store.LoadContext(context.Background(), runID)
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
	startPromptAttempt(t, store, runID, "plan", "planner", "attempt-001")
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
			want: `step "` + "test" + `" uses agent "` + "tester" + `", not "` + "planner" + `"`,
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

			loaded, loadErr := store.LoadContext(context.Background(), runID)
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
	startPromptAttempt(t, openPromptStore(t, root), runID, "plan", "planner", "attempt-001")

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

func TestSelectReportsDoesNotInventRoutingReport(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	ref := &runstore.ArtifactRef{EventSequence: 4}
	loaded.Status.Attempts = []runstore.Attempt{{
		AttemptID: "old-test", AgentID: "tester", State: runstore.AttemptStateReported,
		Report: &runstore.Report{Status: "done", Result: "passed"}, ReportRef: ref,
	}}
	loaded.Events = nil

	selected, err := selectReports(renderContext{run: loaded, attempt: runstore.Attempt{AttemptID: "initial-plan", ConfigSnapshotVersion: 1}})
	if err != nil {
		t.Fatalf("selectReports returned error: %v", err)
	}

	if len(selected) != 0 {
		t.Fatalf("selected reports = %+v, want none", selected)
	}

	if correctionRoute(renderContext{run: loaded, attempt: runstore.Attempt{AttemptID: "initial-plan"}}) {
		t.Fatal("correctionRoute = true without an attempt.started event")
	}
}

func TestVerificationEvidenceAndCorrectionRoute(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	mutatePromptResolvedSnapshot(t, root, runID, func(project *config.Project) {
		wf := project.Workflows["implementation"]
		step := wf.Steps["test"]
		step.Kind = config.StepKindCommand
		step.Agent = ""
		step.Command.Argv = []string{"task", "check"}
		step.Verification = "full"
		wf.Steps["test"] = step
		project.Workflows["implementation"] = wf
	})

	verificationRef := &runstore.ArtifactRef{EventSequence: 8}
	changedRef := &runstore.ArtifactRef{EventSequence: 6}
	routingRef := &runstore.ArtifactRef{EventSequence: 9}
	loaded.Status.Attempts = []runstore.Attempt{
		{AttemptID: "changed", AgentID: "planner", State: runstore.AttemptStateReported, Report: &runstore.Report{Status: "done", Result: "ready", ChangedPaths: []string{"main.go"}}, ReportRef: changedRef},
		{AttemptID: "verified", StepID: "test", AgentID: "command", ConfigSnapshotVersion: 1, State: runstore.AttemptStateReported, Report: &runstore.Report{Status: "done", Result: "passed"}, ReportRef: verificationRef},
		{AttemptID: "review", AgentID: "reviewer", State: runstore.AttemptStateReported, ConsumedByEvent: 10, Report: &runstore.Report{Status: "done", Result: "changes_requested"}, ReportRef: routingRef},
	}
	loaded.Events = []runstore.Event{{Type: "attempt.started", Sequence: 10, Payload: json.RawMessage(`{"attempt":{"attempt_id":"code"}}`)}}
	ctx := renderContext{
		run: loaded, attempt: runstore.Attempt{AttemptID: "code", ConfigSnapshotVersion: 1},
		workflow: config.Workflow{Verification: config.VerificationConfig{FullCheck: config.CommandStep{Argv: []string{"task", "check"}}}},
	}

	fresh, err := freshVerification(ctx)
	if err != nil || fresh == nil || fresh.AttemptID != "verified" {
		t.Fatalf("freshVerification = %+v, %v, want verified", fresh, err)
	}

	evidence, err := renderVerificationEvidence(ctx)
	if err != nil || !strings.Contains(evidence, "Do not repeat the full check") || !strings.Contains(evidence, "`task check`") {
		t.Fatalf("renderVerificationEvidence = %q, %v", evidence, err)
	}

	if !correctionRoute(ctx) {
		t.Fatal("correctionRoute = false, want true")
	}

	loaded.Status.Attempts[0].ReportRef.EventSequence = 11

	evidence, err = renderVerificationEvidence(ctx)
	if err != nil || !strings.Contains(evidence, "Run `task check`") {
		t.Fatalf("stale renderVerificationEvidence = %q, %v", evidence, err)
	}

	ctx.workflow.Verification.FullCheck.Argv = nil

	evidence, err = renderVerificationEvidence(ctx)
	if err != nil || !strings.Contains(evidence, "Report `blocked/blocked`") {
		t.Fatalf("missing fallback evidence = %q, %v", evidence, err)
	}
}

func TestSelectReportsDoesNotGiveImplementationContextOrCorrectionBoundaryToPlanner(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	mutatePromptResolvedSnapshot(t, root, runID, func(project *config.Project) {
		project.Agents["coder"] = config.Agent{ID: "coder", Role: "coder"}
	})

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	implementationRef := &runstore.ArtifactRef{EventSequence: 4}
	routingRef := &runstore.ArtifactRef{EventSequence: 6}
	loaded.Status.Attempts = []runstore.Attempt{
		{AttemptID: "implementation", AgentID: "coder", State: runstore.AttemptStateReported, Report: &runstore.Report{Status: "done", Result: "ready"}, ReportRef: implementationRef},
		{AttemptID: "failed-check", AgentID: "tester", State: runstore.AttemptStateReported, ConsumedByEvent: 7, Report: &runstore.Report{Status: "done", Result: "failed"}, ReportRef: routingRef},
	}
	loaded.Events = []runstore.Event{{Type: "attempt.started", Sequence: 7, Payload: json.RawMessage(`{"attempt":{"attempt_id":"replan"}}`)}}
	ctx := renderContext{run: loaded, agent: config.Agent{Role: "planner"}, attempt: runstore.Attempt{AttemptID: "replan", ConfigSnapshotVersion: 1}}

	selected, err := selectReports(ctx)
	if err != nil {
		t.Fatalf("selectReports returned error: %v", err)
	}

	if len(selected) != 1 || selected[0].AttemptID != "failed-check" {
		t.Fatalf("selected reports = %+v, want failed-check only", selected)
	}

	if correctionBoundary(ctx) {
		t.Fatal("planner prompt includes correction search boundary")
	}
}

func TestRenderCorrectionPromptContainsOnlyRequiredContext(t *testing.T) {
	root := t.TempDir()
	writePromptProject(t, root)
	runID := createPromptRun(t, root, workflow.RunStatusRunning)
	store := openPromptStore(t, root)
	writeTaskContextArtifact(t, store, runID, "# Task\n\nFix the reported failure.\n", fixedPromptTime())

	mutatePromptResolvedSnapshot(t, root, runID, func(project *config.Project) {
		project.Agents["coder"] = config.Agent{ID: "coder", Role: "coder", Description: "Implements corrections."}
		wf := project.Workflows["implementation"]
		wf.Steps["code"] = config.Step{Agent: "coder"}
		step := wf.Steps["test"]
		step.Kind = config.StepKindCommand
		step.Agent = ""
		step.Command.Argv = []string{"task", "check"}
		step.Verification = "full"
		wf.Steps["test"] = step
		project.Workflows["implementation"] = wf
	})

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	reportAttempt := func(attemptID, stepID, agentID, content string, report runstore.Report) runstore.Attempt {
		t.Helper()

		ref, writeErr := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
			Kind: runstore.KindReport, Name: stepID, Content: []byte(content), Time: fixedPromptTime(),
		})
		if writeErr != nil {
			t.Fatalf("WriteArtifact report returned error: %v", writeErr)
		}

		report.RunID, report.StepID, report.AgentID, report.AttemptID = runID, stepID, agentID, attemptID
		report.ReportRef = &ref

		return runstore.Attempt{
			AttemptID: attemptID, StepID: stepID, AgentID: agentID, ConfigSnapshotVersion: 1,
			State: runstore.AttemptStateReported, Report: &report, ReportRef: &ref,
		}
	}

	planner := reportAttempt("planner", "plan", "planner", "PLANNER SCOPE", runstore.Report{Status: "done", Result: "ready"})
	implementation := reportAttempt("implementation", "code", "coder", "LATEST IMPLEMENTATION AND MODIFICATION", runstore.Report{Status: "done", Result: "ready", ChangedPaths: []string{"internal/example.go"}})
	verification := reportAttempt("verification", "test", "command", "FRESH FULL VERIFICATION", runstore.Report{Status: "done", Result: "passed"})
	origin := reportAttempt("finding-origin", "review", "reviewer", "FINDING ORIGIN FULL DETAIL", runstore.Report{Status: "done", Result: "changes_requested", Findings: []runstore.Finding{{FindingID: "focused-failure", Category: "correctness", Path: "internal/example.go", Location: "run", Summary: "The failure remains."}}})
	routing := reportAttempt("routing", "review", "reviewer", "ROUTING REPORT", runstore.Report{Status: "done", Result: "changes_requested", Findings: []runstore.Finding{{FindingID: "focused-failure", Category: "correctness", Path: "internal/example.go", Location: "run", Summary: "Fix this failure."}}})
	unrelated := reportAttempt("unrelated", "test", "tester", "UNRELATED HISTORY", runstore.Report{Status: "done", Result: "failed"})
	routing.ConsumedByEvent = unrelated.ReportRef.EventSequence + 1
	current := runstore.Attempt{AttemptID: "correction", StepID: "code", AgentID: "coder", ConfigSnapshotVersion: 1, Timeout: "30m", StartedAt: fixedPromptTime()}
	loaded.Status.Attempts = []runstore.Attempt{planner, implementation, verification, origin, routing, unrelated, current}
	loaded.Events = []runstore.Event{{Type: "attempt.started", Sequence: routing.ConsumedByEvent, Payload: json.RawMessage(`{"attempt":{"attempt_id":"correction"}}`)}}

	snapshot, err := configsnapshot.LoadVersion(loaded, 1)
	if err != nil {
		t.Fatalf("LoadVersion returned error: %v", err)
	}

	renderCtx := renderContext{
		store: store, run: loaded, workflow: snapshot.Project.Workflows["implementation"],
		step: snapshot.Project.Workflows["implementation"].Steps["code"], agent: snapshot.Project.Agents["coder"], attempt: current,
	}

	content, err := renderPrompt(context.Background(), renderCtx, Options{Root: root, RunID: runID, StepID: "code", AgentID: "coder", AttemptID: "correction", Time: fixedPromptTime().Add(time.Minute)})
	if err != nil {
		t.Fatalf("renderPrompt returned error: %v", err)
	}

	prompt := string(content)
	assertPromptContainsAll(t, prompt, []string{
		"# Task\n\nFix the reported failure.", "PLANNER SCOPE", "LATEST IMPLEMENTATION AND MODIFICATION",
		"FRESH FULL VERIFICATION", "ROUTING REPORT", "FINDING ORIGIN FULL DETAIL",
		"Start from the blocking finding, failed command, and changed lines. Inspect direct definitions, callers, and dependencies only. Do not search for additional defects or improvements. Report blocked before broader repository investigation.",
	})

	if strings.Contains(prompt, "UNRELATED HISTORY") {
		t.Fatalf("correction prompt contains unrelated history:\n%s", prompt)
	}

	if strings.Count(prompt, "LATEST IMPLEMENTATION AND MODIFICATION") != 1 {
		t.Fatalf("correction prompt did not de-duplicate implementation and modifying report:\n%s", prompt)
	}

	wantOrder := []string{"PLANNER SCOPE", "LATEST IMPLEMENTATION AND MODIFICATION", "FRESH FULL VERIFICATION", "ROUTING REPORT", "FINDING ORIGIN FULL DETAIL"}
	last := -1

	for _, marker := range wantOrder {
		index := strings.Index(prompt, marker)
		if index <= last {
			t.Fatalf("correction prompt report order is wrong at %q:\n%s", marker, prompt)
		}

		last = index
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

	run, err := store.CreateContext(context.Background(), runstore.CreateRunRequest{
		RunID:    "prompt-run",
		Workflow: "implementation",
		Time:     fixedPromptTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	writePromptConfigSnapshot(t, root, store, run.ID)

	if state != workflow.RunStatusRunning {
		if _, _, err := store.UpdateStatusContext(context.Background(), run.ID, runstore.StatusUpdate{State: state, Time: fixedPromptTime().Add(time.Minute)}); err != nil {
			t.Fatalf("UpdateStatus returned error: %v", err)
		}
	}

	return run.ID
}

func writePromptConfigSnapshot(t *testing.T, root string, store *runstore.Store, runID string) {
	t.Helper()

	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config returned error: %v", err)
	}

	snapshot, err := configsnapshot.BuildInitial(project, "implementation", fixedPromptTime())
	if err != nil {
		t.Fatalf("BuildInitial returned error: %v", err)
	}

	if err := store.WriteInitialConfigSnapshotContext(context.Background(), runID, snapshot); err != nil {
		t.Fatalf("WriteInitialConfigSnapshot returned error: %v", err)
	}
}

func mutatePromptResolvedSnapshot(t *testing.T, root, runID string, mutate func(*config.Project)) {
	t.Helper()

	path := filepath.Join(root, ".orc", "runs", runID, "config", "000001", "resolved.json")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile resolved snapshot returned error: %v", err)
	}

	var resolved struct {
		SchemaVersion int             `json:"schema_version"`
		Project       *config.Project `json:"project"`
	}
	if err := json.Unmarshal(content, &resolved); err != nil {
		t.Fatalf("Unmarshal resolved snapshot returned error: %v", err)
	}

	mutate(resolved.Project)

	content, err = json.MarshalIndent(resolved, "", "  ")
	if err != nil {
		t.Fatalf("Marshal resolved snapshot returned error: %v", err)
	}

	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile resolved snapshot returned error: %v", err)
	}
}

func openPromptStore(t *testing.T, root string) *runstore.Store {
	t.Helper()

	store, err := runstore.Open(root)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	return store
}

func startPromptAttempt(t *testing.T, store *runstore.Store, runID, stepID, agentID, attemptID string) {
	t.Helper()

	startPromptAttemptVersion(t, store, runID, stepID, agentID, attemptID, 1)
}

func startPromptAttemptVersion(t *testing.T, store *runstore.Store, runID, stepID, agentID, attemptID string, version int) {
	t.Helper()

	loaded, err := store.LoadContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load before StartAttempt returned error: %v", err)
	}

	req := runstore.StartAttemptRequest{
		StepID: stepID, AgentID: agentID, AttemptID: attemptID,
		ConfigSnapshotVersion: version, Timeout: 30 * time.Minute,
		ReportExitGrace: 30 * time.Second, Time: fixedPromptTime(),
	}
	if active := loaded.Status.ActiveAttempt; active != nil {
		req.ConsumeAttemptID = active.AttemptID
	}

	if _, _, err := store.StartAttemptContext(context.Background(), runID, req); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}
}

func writeTaskContextArtifact(t *testing.T, store *runstore.Store, runID, content string, at time.Time) {
	t.Helper()

	if _, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
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
	if _, _, err := store.StartAttemptContext(context.Background(), runID, runstore.StartAttemptRequest{
		StepID:          report.StepID,
		AgentID:         report.AgentID,
		AttemptID:       report.AttemptID,
		Timeout:         30 * time.Minute,
		ReportExitGrace: 30 * time.Second,
		Time:            fixedPromptTime().Add(time.Minute),
	}); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	promptRef, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    report.StepID,
		Content: []byte("prior prompt\n"),
		Time:    fixedPromptTime().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptPromptContext(context.Background(), runID, runstore.AttemptPromptRequest{
		AttemptID: report.AttemptID,
		PromptRef: promptRef,
		Time:      fixedPromptTime().Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}

	logRef, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    report.StepID,
		Content: []byte("prior log\n"),
		Time:    fixedPromptTime().Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptLogContext(context.Background(), runID, runstore.AttemptLogRequest{
		AttemptID: report.AttemptID,
		LogRef:    logRef,
		Time:      fixedPromptTime().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptProcessContext(context.Background(), runID, runstore.AttemptProcessRequest{
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
		req.ReportContent = reportContent
		req.ReportContentSet = true
	}

	if _, _, err := store.RecordAttemptReportContext(context.Background(), runID, req); err != nil {
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

	if _, _, err := store.StartAttemptContext(context.Background(), runID, req); err != nil {
		t.Fatalf("StartAttempt returned error: %v", err)
	}

	promptRef, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    "plan",
		Content: []byte("prompt\n"),
		Time:    fixedPromptTime().Add(80 * time.Second),
	})
	if err != nil {
		t.Fatalf("WriteArtifact prompt returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptPromptContext(context.Background(), runID, runstore.AttemptPromptRequest{
		AttemptID: attemptID,
		PromptRef: promptRef,
		Time:      fixedPromptTime().Add(85 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptPrompt returned error: %v", err)
	}

	logRef, err := store.WriteArtifactContext(context.Background(), runID, runstore.Artifact{
		Kind:    runstore.KindLog,
		Name:    "plan",
		Content: []byte("log\n"),
		Time:    fixedPromptTime().Add(88 * time.Second),
	})
	if err != nil {
		t.Fatalf("WriteArtifact log returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptLogContext(context.Background(), runID, runstore.AttemptLogRequest{
		AttemptID: attemptID,
		LogRef:    logRef,
		Time:      fixedPromptTime().Add(89 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptLog returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptProcessContext(context.Background(), runID, runstore.AttemptProcessRequest{
		AttemptID:        attemptID,
		PID:              12345,
		ProcessStartTime: "123456789",
		Time:             fixedPromptTime().Add(90 * time.Second),
	}); err != nil {
		t.Fatalf("RecordAttemptProcess returned error: %v", err)
	}

	if _, _, err := store.RecordAttemptReportContext(context.Background(), runID, runstore.RecordReportRequest{
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

func removeAttemptReportedReportRef(t *testing.T, root, runID string) {
	t.Helper()

	eventsPath := filepath.Join(root, ".orc", "runs", runID, "events.jsonl")
	content := readPromptFile(t, eventsPath)
	lines := bytes.Split(content, []byte("\n"))

	var out bytes.Buffer

	removed := false

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var event runstore.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}

		if event.Type == "attempt.reported" && !removed {
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal attempt.reported payload: %v", err)
			}

			report, ok := payload["report"].(map[string]any)
			if !ok {
				t.Fatalf("attempt.reported payload missing report object: %+v", payload)
			}

			delete(report, "report_ref")

			nextPayload, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal attempt.reported payload: %v", err)
			}

			event.Payload = nextPayload

			removed = true
		}

		nextLine, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}

		out.Write(nextLine)
		out.WriteByte('\n')
	}

	if !removed {
		t.Fatal("attempt.reported event not found")
	}

	if err := os.WriteFile(eventsPath, out.Bytes(), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}
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
