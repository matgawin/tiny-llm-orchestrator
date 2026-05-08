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
			name: "status",
			args: func(runID string) []string { return []string{"run", "status", runID} },
			want: "state: running",
		},
		{
			name: "show",
			args: func(runID string) []string { return []string{"run", "show", runID} },
			want: "workflow_loop:",
		},
		{
			name: "next",
			args: func(runID string) []string { return []string{"run", "next", runID} },
			want: "decision: select_step",
		},
		{
			name: "summary-context",
			args: func(runID string) []string { return []string{"run", "summary-context", runID} },
			want: "# Summary Context",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)

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
		{name: "status", args: []string{"run", "status", "missing-run"}, want: `orc run status: run "missing-run" not found`},
		{name: "show", args: []string{"run", "show", "missing-run"}, want: `orc run show: run "missing-run" not found`},
		{name: "next", args: []string{"run", "next", "missing-run"}, want: `orc run next: run "missing-run" not found`},
		{name: "summary-context", args: []string{"run", "summary-context", "missing-run"}, want: `orc run summary-context: run "missing-run" not found`},
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
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	blockCLIWorkflowLoopHardCap(t, root, result.runID, "plan", 1, 2)

	output := executeCLICommand(t, []string{"run", "show", result.runID})
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
