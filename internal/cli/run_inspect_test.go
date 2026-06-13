package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestExecuteRunInspectCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(runID string) []string
		want string
	}{
		{
			name: cliCommandStatus,
			args: func(runID string) []string { return []string{commandRun, cliCommandStatus, runID} },
			want: "state: running",
		},
		{
			name: cliCommandShow,
			args: func(runID string) []string { return []string{commandRun, cliCommandShow, runID} },
			want: "workflow_loop:",
		},
		{
			name: cliCommandNext,
			args: func(runID string) []string { return []string{commandRun, cliCommandNext, runID} },
			want: "decision: select_step",
		},
		{
			name: cliCommandSummaryContext,
			args: func(runID string) []string { return []string{commandRun, cliCommandSummaryContext, runID} },
			want: "# Summary Context",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)

			output := executeCLICommand(t, tc.args(result.runID))
			if !strings.Contains(output, tc.want) {
				t.Fatalf("%s output missing %q:\n%s", tc.name, tc.want, output)
			}
		})
	}
}

func TestExecuteRunInspectUnknownRunFailsClearly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: cliCommandStatus, args: []string{commandRun, cliCommandStatus, cliMissingRunID}, want: `orc run status: run "missing-run" not found`},
		{name: cliCommandShow, args: []string{commandRun, cliCommandShow, cliMissingRunID}, want: `orc run show: run "missing-run" not found`},
		{name: cliCommandNext, args: []string{commandRun, cliCommandNext, cliMissingRunID}, want: `orc run next: run "missing-run" not found`},
		{name: cliCommandSummaryContext, args: []string{commandRun, cliCommandSummaryContext, cliMissingRunID}, want: `orc run summary-context: run "missing-run" not found`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)

			var stdout, stderr bytes.Buffer
			if err := Execute(tc.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want missing run failure")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecuteRunShowDisplaysWorkflowLoopCapStatus(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)
	blockCLIWorkflowLoopHardCap(t, root, result.runID, cliStepPlan, 1, 2)

	output := executeCLICommand(t, []string{commandRun, cliCommandShow, result.runID})
	assertCLIOutputContainsAll(t, output, []string{
		"workflow_loop:\n",
		"    plan:\n",
		"      current_count: 1\n",
		"      soft_threshold:",
		"      hard_threshold:",
		"      soft_reached:",
		"      hard_blocking: true\n",
		"      blocked_target_state: plan\n",
		"      blocked_prospective_count: 2\n",
	})
}
