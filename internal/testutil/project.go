package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ProjectOptions describes the minimal implementation workflow test project.
type ProjectOptions struct {
	Beads            string
	MarkdownFallback bool
	Timeout          string
	ReportExitGrace  string
	FailedResults    []string
	BlockedResults   []string
}

// WriteProject writes a minimal .orc project with one planner-backed plan step.
func WriteProject(t *testing.T, root string, opts ProjectOptions) {
	t.Helper()
	if opts.Beads == "" {
		opts.Beads = "optional"
	}
	if opts.Timeout == "" {
		opts.Timeout = "30m"
	}
	if opts.ReportExitGrace == "" {
		opts.ReportExitGrace = "30s"
	}
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o750); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o750); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	writeFile(t, filepath.Join(orcDir, "config.yaml"), "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n")
	writeFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Test planner.\n---\n\nPlan.\n")
	writeFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), workflowYAML(opts))
}

func workflowYAML(opts ProjectOptions) string {
	allowed := "      done: [ready]\n"
	var on strings.Builder
	on.WriteString("      done/ready: ready_for_human\n")
	if len(opts.FailedResults) > 0 {
		allowed += "      failed: [" + joinYAMLList(opts.FailedResults) + "]\n"
		for _, result := range opts.FailedResults {
			on.WriteString("      failed/" + result + ": blocked_for_human\n")
		}
	}
	if len(opts.BlockedResults) > 0 {
		allowed += "      blocked: [" + joinYAMLList(opts.BlockedResults) + "]\n"
		for _, result := range opts.BlockedResults {
			on.WriteString("      blocked/" + result + ": blocked_for_human\n")
		}
	}
	return fmt.Sprintf(`name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: %s
  markdown_fallback: %t
defaults:
  timeout: %s
  report_exit_grace: %s
  retries: {}
steps:
  plan:
    agent: planner
    allowed_results:
%s    on:
%s`, opts.Beads, opts.MarkdownFallback, opts.Timeout, opts.ReportExitGrace, allowed, on.String())
}

func joinYAMLList(values []string) string {
	var out strings.Builder
	for i, value := range values {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(value)
	}
	return out.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
