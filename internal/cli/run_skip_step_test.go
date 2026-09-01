package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestExecuteRunSkipStepSkipsSelectedReviewStep(t *testing.T) {
	root := withTempCwd(t)
	writeCLISkipStepProject(t, root, true)
	result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)

	output := executeCLICommand(t, []string{commandRun, cliCommandSkipStep, result.runID, cliFlagStep, cliStepReview, cliFlagReason, " not worth another review "})
	assertCLIOutputContainsAll(t, output, []string{
		"skipped step review for run " + result.runID,
		"reason: not worth another review",
		"next selected step: code",
	})

	loaded := loadCLIRun(t, root, result.runID)
	if got := len(loaded.Status.Attempts); got != 0 {
		t.Fatalf("attempts = %d, want unchanged empty attempts", got)
	}

	if len(loaded.Status.SkippedSteps) != 1 || loaded.Status.SkippedSteps[0].StepID != cliStepReview || loaded.Status.SkippedSteps[0].Reason != "not worth another review" {
		t.Fatalf("skipped steps = %+v, want review skip with trimmed reason", loaded.Status.SkippedSteps)
	}
}

func TestExecuteRunSkipStepSkipsSelectedRemediationStep(t *testing.T) {
	root := withTempCwd(t)
	writeCLISkipStepProject(t, root, true)
	result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)
	recordCLIReportedAttempt(t, root, result.runID, "review-attempt", cliStepReview, "reviewer", cliStatusDone, "changes_requested")

	output := executeCLICommand(t, []string{commandRun, cliCommandSkipStep, result.runID, "--step=code", "--reason=human reviewed requested changes"})
	assertCLIOutputContainsAll(t, output, []string{
		"skipped step code for run " + result.runID,
		"reason: human reviewed requested changes",
		"run status: " + cliStateReadyForHuman,
	})

	loaded := loadCLIRun(t, root, result.runID)
	if len(loaded.Status.SkippedSteps) != 1 || loaded.Status.SkippedSteps[0].StepID != cliStepCode {
		t.Fatalf("skipped steps = %+v, want code remediation skip", loaded.Status.SkippedSteps)
	}

	if loaded.Status.State != cliStateReadyForHuman {
		t.Fatalf("state = %q, want %s", loaded.Status.State, cliStateReadyForHuman)
	}
}

func TestExecuteRunSkipStepFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "missing step", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagReason, cliSkipReason}, want: []string{"--step is required"}},
		{name: "missing reason", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview}, want: []string{"--reason is required"}},
		{name: "whitespace reason", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview, cliFlagReason, " \t "}, want: []string{"non-empty after trimming"}},
		{name: "duplicate step", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview, "--step=code", cliFlagReason, cliSkipReason}, want: []string{"repeated --step"}},
		{name: "duplicate reason", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview, cliFlagReason, "one", "--reason=two"}, want: []string{"repeated --reason"}},
		{name: "unknown json", args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview, cliFlagReason, cliSkipReason, cliFlagJSON}, want: []string{`unknown flag: --json`}},
		{name: cliUnknownFlag, args: []string{commandRun, cliCommandSkipStep, cliRunIDOne, cliFlagStep, cliStepReview, cliFlagReason, cliSkipReason, "--force"}, want: []string{`unknown flag: --force`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Execute(tc.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want validation failure")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			assertCLIOutputContainsAll(t, stderr.String(), tc.want)
		})
	}
}

func TestExecuteRunSkipStepRejectsIneligibleRunState(t *testing.T) {
	tests := []struct {
		name      string
		skippable bool
		setup     func(t *testing.T, root, runID string)
		step      string
		want      []string
	}{
		{
			name:      "wrong step",
			skippable: true,
			step:      cliStepCode,
			want:      []string{`step "code" is not selected`, `selected step is "review"`},
		},
		{
			name:      "active attempt",
			skippable: true,
			setup: func(t *testing.T, root, runID string) {
				t.Helper()
				startCLIActiveAttemptForStep(t, root, runID, "review-active", cliStepReview, "reviewer")
			},
			step: cliStepReview,
			want: []string{"active attempt"},
		},
		{
			name:      "retry decision",
			skippable: true,
			setup: func(t *testing.T, root, runID string) {
				t.Helper()
				recordCLIReportedAttempt(t, root, runID, "review-retry", cliStepReview, "reviewer", cliStatusFailed, cliResultError)
			},
			step: cliStepReview,
			want: []string{"decision is retry_step", "only a selected step can be skipped"},
		},
		{
			name:      "terminal run",
			skippable: true,
			setup: func(t *testing.T, root, runID string) {
				t.Helper()

				if _, _, err := openCLIStore(t, root).UpdateStatusContext(context.Background(), runID, runstore.StatusUpdate{State: cliStateReadyForHuman}); err != nil {
					t.Fatalf("UpdateStatus returned error: %v", err)
				}
			},
			step: cliStepReview,
			want: []string{"terminal"},
		},
		{
			name:      "non skippable step",
			skippable: false,
			step:      cliStepReview,
			want:      []string{`step "review" is not skippable`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLISkipStepProject(t, root, tc.skippable)

			result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)
			if tc.setup != nil {
				tc.setup(t, root, result.runID)
			}

			before := loadCLIRun(t, root, result.runID)

			var stdout, stderr bytes.Buffer

			err := Execute([]string{commandRun, cliCommandSkipStep, result.runID, cliFlagStep, tc.step, cliFlagReason, "skip it"}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want skip rejection")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			assertCLIOutputContainsAll(t, stderr.String(), tc.want)

			after := loadCLIRun(t, root, result.runID)
			if after.Status.LastSequence != before.Status.LastSequence || len(after.Status.SkippedSteps) != len(before.Status.SkippedSteps) || after.Status.State != before.Status.State {
				t.Fatalf("after status = %+v, want no mutation from %+v", after.Status, before.Status)
			}
		})
	}
}

func TestExecuteRunHelpListsMigratedSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, helpFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		cliCommandAddFollowup,
		cliCommandAdvance,
		cliCommandConfig,
		cliCommandContinue,
		cliCommandNext,
		cliCommandRecordSummary,
		cliCommandRefreshConfig,
		cliCommandShow,
		cliCommandSkipStep,
		cliCommandStart,
		cliCommandStatus,
		cliCommandSummaryContext,
		"Skip the currently selected skippable workflow step",
	})
}

func TestExecuteRunUnknownSubcommandFailsThroughCobra(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, cliCommandUnknown}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown command failure")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, `unknown command "unknown" for "orc run"`) {
		t.Fatalf("stderr = %q, want Cobra unknown command diagnostic", got)
	}
}

func TestExecuteRunSkipStepHelpDocumentsContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, cliCommandSkipStep, helpFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"orc run skip-step <run-id> --step <step-id> --reason <text>",
		"--step <step-id>",
		"--reason <text>",
		"active worker attempts",
		"retry, wait, terminal decisions",
		"done/skipped",
		"JSON output",
	})
}
