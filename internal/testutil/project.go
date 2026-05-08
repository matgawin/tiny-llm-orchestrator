package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	TwoStep          bool
	Retries          map[string]int
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
	if err := os.MkdirAll(filepath.Join(orcDir, "runtimes"), 0o750); err != nil {
		t.Fatalf("create runtimes dir: %v", err)
	}
	configYAML := "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n"
	if opts.TwoStep {
		configYAML += "  coder: agents/coder.md\n"
	}
	configYAML += "runtimes:\n  codex: runtimes/codex.yaml\n"
	writeFile(t, filepath.Join(orcDir, "config.yaml"), configYAML)
	writeFile(t, filepath.Join(orcDir, "runtimes", "codex.yaml"), CodexRuntimeYAML())
	writeFile(t, filepath.Join(orcDir, "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Test planner.\n---\n\nPlan.\n")
	if opts.TwoStep {
		writeFile(t, filepath.Join(orcDir, "agents", "coder.md"), "---\nid: coder\nrole: coder\ndescription: Test coder.\n---\n\nCode.\n")
	}
	writeFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), workflowYAML(opts))
}

func workflowYAML(opts ProjectOptions) string {
	allowed := allowedResultsYAML(opts)
	planReadyTarget := "ready_for_human"
	if opts.TwoStep {
		planReadyTarget = "code"
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
  runtime: codex
  retries:
%s
steps:
  plan:
    agent: planner
    allowed_results:
%s    on:
%s
  code:
    agent: coder
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`, opts.Beads, opts.MarkdownFallback, opts.Timeout, opts.ReportExitGrace, retriesYAML(opts.Retries), allowed, outcomeTransitionsYAML(opts, planReadyTarget))
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
  runtime: codex
  retries:
%s
steps:
  plan:
    agent: planner
    allowed_results:
%s    on:
%s`, opts.Beads, opts.MarkdownFallback, opts.Timeout, opts.ReportExitGrace, retriesYAML(opts.Retries), allowed, outcomeTransitionsYAML(opts, planReadyTarget))
}

func CodexRuntimeYAML() string {
	return `id: codex
command:
  executable: codex
  normal_args: [--ask-for-approval, never]
  sandbox_args: [--dangerously-bypass-approvals-and-sandbox]
  args: [exec, --skip-git-repo-check, "-"]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  allowed: []
  args: [--model, "{model}"]
directories:
  supported: true
  args: [--add-dir, "{dir}"]
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: [CODEX_HOME, OPENAI_API_KEY]
      set: {}
    mounts: []
`
}

func allowedResultsYAML(opts ProjectOptions) string {
	var allowed strings.Builder
	allowed.WriteString("      done: [ready]\n")
	if len(opts.FailedResults) > 0 {
		allowed.WriteString("      failed: [" + joinYAMLList(opts.FailedResults) + "]\n")
	}
	if len(opts.BlockedResults) > 0 {
		allowed.WriteString("      blocked: [" + joinYAMLList(opts.BlockedResults) + "]\n")
	}
	return allowed.String()
}

func retriesYAML(retries map[string]int) string {
	if len(retries) == 0 {
		return "    {}\n"
	}
	var keys []string
	for key := range retries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "    %s: %d\n", key, retries[key])
	}
	return out.String()
}

func outcomeTransitionsYAML(opts ProjectOptions, readyTarget string) string {
	var on strings.Builder
	on.WriteString("      done/ready: " + readyTarget + "\n")
	for _, result := range opts.FailedResults {
		on.WriteString("      failed/" + result + ": blocked_for_human\n")
	}
	for _, result := range opts.BlockedResults {
		on.WriteString("      blocked/" + result + ": blocked_for_human\n")
	}
	return on.String()
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
