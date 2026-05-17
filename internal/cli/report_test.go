package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/testutil"
)

func TestExecuteReportFlagsPersistsCurrentAttemptReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "detail.md")
	writeCLIFile(t, reportPath, "## Detail\n")

	output := executeCLICommand(t, []string{
		"report",
		"--run=" + result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "attempt-001",
		"--status", "done",
		"--result", "ready",
		"--summary", "Plan is ready.",
		"--changed-path=README.md",
		"--changed-path", "internal/cli/report.go",
		"--command", "go test ./internal/cli",
		"--test", "go test ./internal/cli",
		"--risk", "none",
		"--follow-up", "Document report summaries",
		"--report-file", reportPath,
	})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})
	store := openCLIStore(t, root)

	loaded, err := store.Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateReported || attempt.Report == nil {
		t.Fatalf("attempt = %+v, want reported attempt with report", attempt)
	}

	if attempt.Report.ChangedPaths[0] != "README.md" || attempt.Report.Commands[0] != "go test ./internal/cli" {
		t.Fatalf("report = %+v, want preserved optional fields", attempt.Report)
	}

	if attempt.Report.ChangedPaths[1] != "internal/cli/report.go" {
		t.Fatalf("changed paths = %+v, want repeated flag order preserved", attempt.Report.ChangedPaths)
	}

	if attempt.Report.Tests[0] != "go test ./internal/cli" || attempt.Report.Risks[0] != "none" || attempt.Report.Followups[0].Title != "Document report summaries" {
		t.Fatalf("report = %+v, want preserved tests, risks, and followups", attempt.Report)
	}

	if attempt.ReportRef == nil || attempt.ReportRef.Kind != runstore.KindReport {
		t.Fatalf("report ref = %+v, want report artifact ref", attempt.ReportRef)
	}

	if attempt.Report.ReportRef == nil || *attempt.Report.ReportRef != *attempt.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", attempt.Report.ReportRef, attempt.ReportRef)
	}

	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, filepath.FromSlash(attempt.ReportRef.Path)))); got != "## Detail\n" {
		t.Fatalf("report detail = %q, want copied markdown", got)
	}

	followups := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, followups, []string{
		"## Document report summaries",
		"Source: report",
		"Step: plan",
		"Agent: planner",
		"Attempt: attempt-001",
	})
}

func TestExecuteReportHelp(t *testing.T) {
	output := executeCLICommand(t, []string{"report", "--help"})
	for _, want := range []string{"Usage:", "--json-file", "--changed-path", "--follow-up", "--report-file"} {
		if !strings.Contains(output, want) {
			t.Fatalf("report help output missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteReportFlagParsingErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown", args: []string{"report", "--bogus"}, want: "unknown flag: --bogus"},
		{name: "missing value", args: []string{"report", "--run"}, want: "flag needs an argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Execute(tt.args, &stdout, &stderr); err == nil {
				t.Fatal("Execute returned nil error, want flag parsing error")
			}

			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			if got := stderr.String(); !strings.Contains(got, tt.want) || !strings.Contains(got, "Usage:") {
				t.Fatalf("stderr = %q, want %q and usage", got, tt.want)
			}
		})
	}
}

func TestExecuteReportBadReportFileTerminalizesInvalidReport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		makePath  func(t *testing.T, root string) string
		wantError string
	}{
		{
			name: "missing",
			makePath: func(t *testing.T, root string) string {
				t.Helper()
				return filepath.Join(root, "missing.md")
			},
			wantError: "report_file",
		},
		{
			name: "directory",
			makePath: func(t *testing.T, root string) string {
				t.Helper()

				path := filepath.Join(root, "report-dir")
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatalf("Mkdir returned error: %v", err)
				}

				return path
			},
			wantError: "not a regular file",
		},
		{
			name: "unreadable",
			makePath: func(t *testing.T, root string) string {
				t.Helper()

				path := filepath.Join(root, "unreadable.md")
				writeCLIFile(t, path, "## Detail\n")

				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("Chmod returned error: %v", err)
				}

				t.Cleanup(func() {
					_ = os.Chmod(path, 0o600)
				})

				file, err := os.Open(path)
				if err == nil {
					_ = file.Close()

					t.Skip("current user can read mode 000 files")
				}

				return path
			},
			wantError: "report_file",
		},
		{
			name: "symlink",
			makePath: func(t *testing.T, root string) string {
				t.Helper()

				target := filepath.Join(root, "target.md")
				link := filepath.Join(root, "link.md")

				writeCLIFile(t, target, "## Detail\n")

				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("Symlink returned error: %v", err)
				}

				return link
			},
			wantError: "report_file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")
			reportPath := tc.makePath(t, root)

			var stdout, stderr bytes.Buffer

			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "done",
				"--result", "ready",
				"--summary", "Plan is ready.",
				"--report-file", reportPath,
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want report_file error")
			}

			if !strings.Contains(stderr.String(), tc.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantError)
			}

			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport || attempt.Report == nil {
				t.Fatalf("attempt = %+v, want invalid_report with report", attempt)
			}

			if attempt.ReportRef != nil || attempt.Report.ReportRef != nil {
				t.Fatalf("report refs = %+v/%+v, want none for invalid report_file", attempt.ReportRef, attempt.Report.ReportRef)
			}
		})
	}
}

func TestExecuteReportRejectsReservedSystemOutcomes(t *testing.T) {
	for _, reserved := range []string{"invalid_report", "missing_report", "timeout", "process_error", "error"} {
		t.Run(reserved, func(t *testing.T) {
			root := withTempCwd(t)
			testutil.WriteProject(t, root, testutil.ProjectOptions{
				Beads:            "optional",
				MarkdownFallback: true,
				FailedResults:    []string{"invalid_report", "missing_report", "timeout", "process_error", "error"},
			})
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer

			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "failed",
				"--result", reserved,
				"--summary", "Trying to claim a system outcome.",
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want reserved outcome rejection")
			}

			if !strings.Contains(stderr.String(), "reserved system outcome failed/"+reserved) {
				t.Fatalf("stderr = %q, want reserved system outcome error", stderr.String())
			}

			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport || attempt.Status != "failed" || attempt.Result != runstore.AttemptResultInvalidReport {
				t.Fatalf("attempt = %+v, want failed/invalid_report", attempt)
			}
		})
	}
}

func TestExecuteReportInvalidCurrentAttemptTerminalizesInvalidReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "attempt-001",
		"--status", "done",
		"--result", "not-allowed",
		"--summary", "Bad result.",
		"--follow-up", "Should not append",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want invalid report error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if !strings.Contains(stderr.String(), "does not allow done/not-allowed") {
		t.Fatalf("stderr = %q, want disallowed result", stderr.String())
	}

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Status != "failed" || attempt.Result != runstore.AttemptResultInvalidReport {
		t.Fatalf("attempt = %+v, want failed/invalid_report", attempt)
	}

	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestExecuteReportWrongAttemptRecordsIgnoredBeforeConfigLoad(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "old-attempt",
		"--status", "done",
		"--result", "ready",
		"--summary", "Stale report.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want wrong attempt error")
	}

	if strings.Contains(stderr.String(), "load project config") {
		t.Fatalf("stderr = %q, want ignored report before config load", stderr.String())
	}

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}
}

func TestExecuteReportShapeInvalidCurrentAttemptDoesNotLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing-status",
			args: []string{
				"report",
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--result", "ready",
				"--summary", "Missing status.",
			},
			want: "status is required",
		},
		{
			name: "reserved-outcome",
			args: []string{
				"report",
				"--step", "plan",
				"--agent", "planner",
				"--attempt", "attempt-001",
				"--status", "failed",
				"--result", "timeout",
				"--summary", "Reserved outcome.",
			},
			want: "reserved system outcome failed/timeout",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")
			writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

			args := append([]string{"report", "--run", result.runID}, tc.args[1:]...)

			var stdout, stderr bytes.Buffer

			err := Execute(args, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want shape validation error")
			}

			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}

			if strings.Contains(stderr.String(), "load project config") {
				t.Fatalf("stderr = %q, want no config load failure", stderr.String())
			}

			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport {
				t.Fatalf("attempt state = %q, want invalid_report", attempt.State)
			}
		})
	}
}

func TestExecuteReportMissingRequiredFieldTerminalizesInvalidReport(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(runID string) []string
		want string
	}{
		{
			name: "missing-status",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--result", "ready",
					"--summary", "Missing status.",
				}
			},
			want: "status is required",
		},
		{
			name: "missing-result",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--status", "done",
					"--summary", "Missing result.",
				}
			},
			want: "result is required",
		},
		{
			name: "blank-summary",
			args: func(runID string) []string {
				return []string{
					"report",
					"--run", runID,
					"--step", "plan",
					"--agent", "planner",
					"--attempt", "attempt-001",
					"--status", "done",
					"--result", "ready",
					"--summary", " \t",
				}
			},
			want: "summary is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			startCLIActiveAttempt(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer

			err := Execute(tc.args(result.runID), &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want missing field error")
			}

			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}

			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
			if attempt.State != runstore.AttemptStateInvalidReport {
				t.Fatalf("attempt state = %q, want invalid_report", attempt.State)
			}
		})
	}
}

func TestExecuteReportJSONTrailingObjectTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, "")
	writeCLIFile(t, jsonPath, currentAttemptJSONReport(runID, "")+"\n"+`{"extra": true}`)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want trailing JSON schema error")
	}

	if !strings.Contains(stderr.String(), "multiple JSON values are not allowed") {
		t.Fatalf("stderr = %q, want multiple JSON values error", stderr.String())
	}

	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONSchemaInvalidCurrentAttemptDoesNotLoadConfig(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"unexpected": true`)
	writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want schema validation error")
	}

	if !strings.Contains(stderr.String(), `unknown field "unexpected"`) {
		t.Fatalf("stderr = %q, want unknown field", stderr.String())
	}

	if strings.Contains(stderr.String(), "load project config") {
		t.Fatalf("stderr = %q, want no config load failure", stderr.String())
	}

	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONFilePersistsReport(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	jsonPath := filepath.Join(root, "report.json")
	writeCLIFile(t, jsonPath, fmt.Sprintf(`{
  "run_id": %q,
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "attempt-001",
  "status": "done",
  "result": "ready",
  "summary": "Plan is ready.",
  "changed_paths": ["README.md"],
  "commands": ["go test ./..."],
  "tests": ["task tests"],
  "risks": ["none"],
  "followups": [
    {"title": "Later", "details": "Capture summary context."}
  ]
}`, result.runID))

	output := executeCLICommand(t, []string{"report", "--json-file=" + jsonPath})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})

	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	report := loaded.Status.Attempts[len(loaded.Status.Attempts)-1].Report
	if report == nil || report.Commands[0] != "go test ./..." {
		t.Fatalf("report = %+v, want JSON command", report)
	}

	if report.ChangedPaths[0] != "README.md" || report.Tests[0] != "task tests" || report.Risks[0] != "none" {
		t.Fatalf("report = %+v, want JSON optional slices", report)
	}

	if report.Followups[0].Title != "Later" || report.Followups[0].Details != "Capture summary context." {
		t.Fatalf("followups = %+v, want JSON followup details", report.Followups)
	}

	followups := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md")))
	assertCLIOutputContainsAll(t, followups, []string{
		"## Later",
		"Source: report",
		"Step: plan",
		"Capture summary context.",
	})
}

func TestExecuteReportJSONFileCopiesMarkdownDetail(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "detail.md")
	writeCLIFile(t, reportPath, "")

	jsonPath := filepath.Join(root, "report.json")
	writeCLIFile(t, jsonPath, fmt.Sprintf(`{
  "run_id": %q,
  "step_id": "plan",
  "agent_id": "planner",
  "attempt_id": "attempt-001",
  "status": "done",
  "result": "ready",
  "summary": "Plan is ready.",
  "report_file": %q
}`, result.runID, reportPath))

	output := executeCLICommand(t, []string{"report", "--json-file", jsonPath})
	assertCLIOutputContainsAll(t, output, []string{"recorded report for run " + result.runID, "attempt-001"})

	loaded, err := openCLIStore(t, root).Load(result.runID)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	attempt := loaded.Status.Attempts[len(loaded.Status.Attempts)-1]
	if attempt.ReportRef == nil {
		t.Fatal("report ref = nil, want report artifact for empty report_file")
	}

	if attempt.Report == nil || attempt.Report.ReportRef == nil || *attempt.Report.ReportRef != *attempt.ReportRef {
		t.Fatalf("embedded report ref = %+v, want %+v", attempt.Report, attempt.ReportRef)
	}

	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, filepath.FromSlash(attempt.ReportRef.Path)))); got != "" {
		t.Fatalf("report detail = %q, want empty copied file", got)
	}
}

func TestExecuteReportRejectsJSONMixedWithFlags(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, "")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath, "--summary", "mixed"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want mixed input rejection")
	}

	if !strings.Contains(stderr.String(), "--json-file cannot be combined") {
		t.Fatalf("stderr = %q, want JSON mixed rejection", stderr.String())
	}

	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONUnknownTopLevelFieldTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"surprise": true`)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want unknown field error")
	}

	if !strings.Contains(stderr.String(), `unknown field "surprise"`) {
		t.Fatalf("stderr = %q, want unknown field", stderr.String())
	}

	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportJSONReportRefTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"report_ref": {
    "kind": "report",
    "path": "reports/000001-plan.md",
    "event_sequence": 1
  }`)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want report_ref schema error")
	}

	if !strings.Contains(stderr.String(), `unknown field "report_ref"`) {
		t.Fatalf("stderr = %q, want report_ref unknown field", stderr.String())
	}

	attempt := assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Report == nil {
		t.Fatalf("attempt = %+v, want invalid_report with preserved identity", attempt)
	}

	if attempt.Report.ReportRef != nil {
		t.Fatalf("report_ref = %+v, want caller-supplied ref cleared", attempt.Report.ReportRef)
	}
}

func TestExecuteReportJSONUnknownNestedFieldTerminalizesInvalidReport(t *testing.T) {
	root, runID, jsonPath := writeCurrentAttemptJSONReport(t, `"followups": [
    {"title": "Later", "unexpected": true}
  ]`)

	var stdout, stderr bytes.Buffer

	err := Execute([]string{"report", "--json-file", jsonPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want nested unknown field error")
	}

	if !strings.Contains(stderr.String(), `unknown field "unexpected"`) {
		t.Fatalf("stderr = %q, want nested unknown field", stderr.String())
	}

	assertCLILatestAttemptState(t, root, runID, runstore.AttemptStateInvalidReport)
}

func TestExecuteReportWrongAttemptRecordsIgnoredEvent(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	startCLIActiveAttempt(t, root, result.runID, "attempt-001")
	reportPath := filepath.Join(root, "ignored-detail.md")
	writeCLIFile(t, reportPath, "## Ignored\n")

	var stdout, stderr bytes.Buffer

	err := Execute([]string{
		"report",
		"--run", result.runID,
		"--step", "plan",
		"--agent", "planner",
		"--attempt", "old-attempt",
		"--status", "done",
		"--result", "ready",
		"--summary", "Stale report.",
		"--report-file", reportPath,
		"--follow-up", "Should not append",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Execute returned nil error, want wrong attempt error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	loaded, loadErr := openCLIStore(t, root).Load(result.runID)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}

	if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.AttemptID != "attempt-001" {
		t.Fatalf("active attempt = %+v, want unchanged attempt-001", loaded.Status.ActiveAttempt)
	}

	if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
		t.Fatalf("last event type = %q, want report.ignored", got)
	}

	for _, artifact := range loaded.Status.Artifacts {
		if artifact.Kind == runstore.KindReport {
			t.Fatalf("artifact = %+v, want no report artifact for ignored report", artifact)
		}
	}

	if active := loaded.Status.ActiveAttempt; active == nil || active.ReportRef != nil || active.Report != nil {
		t.Fatalf("active attempt = %+v, want unchanged active attempt without report refs", active)
	}

	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
		t.Fatalf("followups.md = %q, want unchanged empty file", got)
	}
}

func TestExecuteReportWrongStepAgentAndStartingAttemptRecordIgnoredEvent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		step       string
		agent      string
		start      func(t *testing.T, root, runID, attemptID string)
		wantActive string
	}{
		{name: "wrong-step", step: "future", agent: "planner", start: startCLIActiveAttempt, wantActive: runstore.AttemptStateActive},
		{name: "wrong-agent", step: "plan", agent: "other", start: startCLIActiveAttempt, wantActive: runstore.AttemptStateActive},
		{name: "starting", step: "plan", agent: "planner", start: startCLIStartingAttempt, wantActive: runstore.AttemptStateStarting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withTempCwd(t)
			writeCLIProject(t, root, "optional", true)
			result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
			tc.start(t, root, result.runID, "attempt-001")

			var stdout, stderr bytes.Buffer

			err := Execute([]string{
				"report",
				"--run", result.runID,
				"--step", tc.step,
				"--agent", tc.agent,
				"--attempt", "attempt-001",
				"--status", "done",
				"--result", "ready",
				"--summary", "Ignored.",
				"--follow-up", "Should not append",
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("Execute returned nil error, want ignored report error")
			}

			loaded, loadErr := openCLIStore(t, root).Load(result.runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}

			if loaded.Status.ActiveAttempt == nil || loaded.Status.ActiveAttempt.State != tc.wantActive {
				t.Fatalf("active attempt = %+v, want unchanged %s", loaded.Status.ActiveAttempt, tc.wantActive)
			}

			if got := loaded.Events[len(loaded.Events)-1].Type; got != reportIgnoredEvent {
				t.Fatalf("last event type = %q, want report.ignored", got)
			}

			if got := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "followups.md"))); got != "" {
				t.Fatalf("followups.md = %q, want unchanged empty file", got)
			}
		})
	}
}
