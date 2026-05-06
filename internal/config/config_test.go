package config

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"tiny-llm-orchestrator/orc/internal/testutil"
)

func TestLoadValidImplementationWorkflow(t *testing.T) {
	project, err := Load(validScaffoldPath())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	for _, name := range []string{
		"implementation",
		"bugfix",
		"mechanical-change",
		"test-only",
		"review-mechanical",
		"review-readability",
		"review-redundancy",
		"review-docs",
	} {
		if _, ok := project.Workflows[name]; !ok {
			t.Fatalf("workflow %q was not loaded", name)
		}
	}
	for _, name := range []string{
		"planner",
		"coder",
		"mechanical-coder",
		"bug-reproducer",
		"tester",
		"test-designer",
		"reviewer",
		"mechanical-reviewer",
		"readability-reviewer",
		"redundancy-reviewer",
		"docs-reviewer",
	} {
		if _, ok := project.Agents[name]; !ok {
			t.Fatalf("agent %q was not loaded", name)
		}
	}

	workflow := project.Workflows["implementation"]
	if workflow.Name != "implementation" {
		t.Fatalf("workflow name = %q, want implementation", workflow.Name)
	}
	if workflow.Start != "plan" {
		t.Fatalf("workflow start = %q, want plan", workflow.Start)
	}
	if workflow.Execution.Mode != "sequential" {
		t.Fatalf("execution mode = %q, want sequential", workflow.Execution.Mode)
	}
	if !workflow.TaskContext.MarkdownFallback.Value {
		t.Fatal("markdown_fallback = false, want true")
	}
	if got := workflow.VCS.EffectiveDirtyStart(); got != VCSDirtyStartBlock {
		t.Fatalf("vcs dirty_start = %q, want %q", got, VCSDirtyStartBlock)
	}
	if got := workflow.VCS.EffectiveNoVCS(); got != VCSNoVCSAllow {
		t.Fatalf("vcs no_vcs = %q, want %q", got, VCSNoVCSAllow)
	}
	if got, want := workflow.Defaults.Timeout.Duration, 30*time.Minute; got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
	if got, want := workflow.LoopCaps, (EffectiveLoopCaps{Enabled: true, Soft: 2, Hard: 4}); got != want {
		t.Fatalf("loop caps = %+v, want %+v", got, want)
	}
	if got := project.Agents["planner"].Role; got != "planner" {
		t.Fatalf("planner role = %q, want planner", got)
	}
	if got := workflow.ReferencedAgents["planner"].Path; got != "agents/planner.md" {
		t.Fatalf("planner workflow agent path = %q, want agents/planner.md", got)
	}
}

func TestLoadWorkflowPreservesStepDeclarationOrder(t *testing.T) {
	root := t.TempDir()
	testutil.WriteProject(t, root, testutil.ProjectOptions{
		MarkdownFallback: true,
		TwoStep:          true,
	})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := project.Workflows["implementation"].StepOrder, []string{"plan", "code"}; !slices.Equal(got, want) {
		t.Fatalf("workflow step order = %v, want %v", got, want)
	}
}

func TestLoadWorkflowLoopCaps(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   EffectiveLoopCaps
	}{
		{
			name:   "legacy config uses built-in defaults",
			config: configForAgents(map[string]string{"planner": validAgentDescriptor("planner")}),
			want:   EffectiveLoopCaps{Enabled: true, Soft: 2, Hard: 4},
		},
		{
			name: "explicit defaults",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 7
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
`,
			want: EffectiveLoopCaps{Enabled: true, Soft: 3, Hard: 7},
		},
		{
			name: "workflow partial override merges with defaults",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 7
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      hard: 9
agents:
  planner: agents/planner.md
`,
			want: EffectiveLoopCaps{Enabled: true, Soft: 3, Hard: 9},
		},
		{
			name: "workflow disables caps without soft or hard",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 7
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      enabled: false
agents:
  planner: agents/planner.md
`,
			want: EffectiveLoopCaps{Enabled: false, Soft: 3, Hard: 7},
		},
		{
			name: "disabled caps ignore zero soft and hard",
			config: `version: 1
defaults:
  loop_caps:
    enabled: false
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      soft: 0
      hard: 0
agents:
  planner: agents/planner.md
`,
			want: EffectiveLoopCaps{Enabled: false, Soft: 0, Hard: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: tt.config})
			project, err := Load(root)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if got := project.Workflows["implementation"].LoopCaps; got != tt.want {
				t.Fatalf("loop caps = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidLoopCaps(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		contains []string
	}{
		{
			name: "negative default soft cap",
			config: `version: 1
defaults:
  loop_caps:
    soft: -1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
`,
			contains: []string{"defaults.loop_caps.soft must be >= 0"},
		},
		{
			name: "zero soft cap while enabled",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 0
    hard: 4
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
`,
			contains: []string{"workflows.implementation.loop_caps.soft must be > 0 when enabled"},
		},
		{
			name: "hard equal to soft while enabled",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 4
    hard: 4
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
`,
			contains: []string{"workflows.implementation.loop_caps.hard must be greater than soft when enabled"},
		},
		{
			name: "workflow override makes hard less than inherited soft",
			config: `version: 1
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 7
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      hard: 2
agents:
  planner: agents/planner.md
`,
			contains: []string{"workflows.implementation.loop_caps.hard must be greater than soft when enabled"},
		},
		{
			name: "expanded workflow missing path",
			config: `version: 1
workflows:
  implementation:
    loop_caps:
      hard: 5
agents:
  planner: agents/planner.md
`,
			contains: []string{`workflow "implementation" path is required`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: tt.config})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsGeneratedWorkflowConfig(t *testing.T) {
	tests := []invalidWorkflowCase{
		generatedWorkflowCase(t, "invalid transition target", func(workflow Workflow) Workflow {
			workflow.Steps["plan"].On["done/ready"] = "ship_it"
			return workflow
		}, `targets unknown step or terminal state "ship_it"`),
		generatedWorkflowCase(t, "invalid transition pair includes allowed values", func(workflow Workflow) Workflow {
			delete(workflow.Steps["plan"].On, "done/ready")
			workflow.Steps["plan"].On["done/unknown"] = "ready_for_human"
			return workflow
		}, `transition "done/unknown" is not declared`, `allowed pairs: done/ready`),
		generatedWorkflowCase(t, "missing transition for allowed pair", func(workflow Workflow) Workflow {
			workflow.Steps["plan"].AllowedResults["blocked"] = []string{"blocked"}
			return workflow
		}, `allowed result "blocked/blocked" has no deterministic on transition`),
		generatedWorkflowCase(t, "invalid retry key includes allowed values", func(workflow Workflow) Workflow {
			workflow.Defaults.Retries = map[string]int{"failed/timeout": 1}
			return workflow
		}, `retry key "failed/timeout" is not declared`, `allowed pairs: done/ready`),
		generatedWorkflowCase(t, "negative retry count", func(workflow Workflow) Workflow {
			workflow.Defaults.Retries = map[string]int{"done/ready": -1}
			return workflow
		}, `retry key "done/ready" has negative retry count -1`),
		generatedWorkflowCase(t, "invalid task context policy", func(workflow Workflow) Workflow {
			workflow.TaskContext.Beads = "nonsense"
			return workflow
		}, `task_context.beads "nonsense" is invalid; allowed: disabled, optional, required`),
		generatedWorkflowCase(t, "invalid dirty start policy", func(workflow Workflow) Workflow {
			workflow.VCS.DirtyStart = "prompt"
			return workflow
		}, `vcs.dirty_start "prompt" is invalid; allowed: allow, block`),
		generatedWorkflowCase(t, "invalid no vcs policy", func(workflow Workflow) Workflow {
			workflow.VCS.NoVCS = "warn"
			return workflow
		}, `vcs.no_vcs "warn" is invalid; allowed: allow, block`),
		generatedWorkflowCase(t, "missing timeout", func(workflow Workflow) Workflow {
			workflow.Defaults.Timeout = Duration{}
			return workflow
		}, "defaults.timeout is required"),
		generatedWorkflowCase(t, "zero report grace", func(workflow Workflow) Workflow {
			workflow.Defaults.ReportExitGrace = Duration{Set: true}
			return workflow
		}, "defaults.report_exit_grace must be > 0"),
		generatedWorkflowCase(t, "invalid status includes allowed values", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.AllowedResults = map[string][]string{"waiting": {"ready"}}
			step.On = map[string]string{"waiting/ready": "ready_for_human"}
			workflow.Steps["plan"] = step
			return workflow
		}, `invalid status "waiting"; allowed: blocked, done, failed`),
		generatedWorkflowCase(t, "missing start step", func(workflow Workflow) Workflow {
			workflow.Steps["code"] = workflow.Steps["plan"]
			delete(workflow.Steps, "plan")
			return workflow
		}, `start step "plan" is not declared`),
		generatedWorkflowCase(t, "unsupported execution mode", func(workflow Workflow) Workflow {
			workflow.Execution.Mode = "parallel"
			return workflow
		}, `unsupported execution mode "parallel"; allowed: sequential`),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				workflow: tt.workflow,
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsRawWorkflowConfig(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		agents   map[string]string
		contains []string
	}{
		{
			name:     "missing retries policy",
			workflow: workflowWithoutRetries(t),
			contains: []string{"defaults.retries is required"},
		},
		{
			name:     "duplicate step name",
			workflow: duplicateStepWorkflow,
			agents: map[string]string{
				"planner": validAgentDescriptor("planner"),
				"coder":   validAgentDescriptor("coder"),
			},
			contains: []string{"duplicate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				workflow: tt.workflow,
				agents:   tt.agents,
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsInvalidProjectConfig(t *testing.T) {
	tests := []struct {
		name     string
		agents   map[string]string
		config   string
		contains []string
	}{
		{
			name:     "step references missing configured agent",
			agents:   map[string]string{"coder": validAgentDescriptor("coder")},
			contains: []string{`step "plan" references missing agent "planner"`},
		},
		{
			name:     "invalid agent frontmatter",
			agents:   map[string]string{"planner": "---\nid: planner\nrole: planner\n---\n\nPlan the work.\n"},
			contains: []string{"frontmatter description is required"},
		},
		{
			name:     "escaping path",
			config:   "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: ../planner.md\n",
			contains: []string{"path must not escape .orc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				agents: tt.agents,
				config: tt.config,
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsSymlinkEscapingOrc(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{})
	orcDir := filepath.Join(root, ".orc")
	outsideAgent := filepath.Join(root, "outside-agent.md")
	writeFile(t, outsideAgent, `---
id: planner
role: planner
description: Escapes the .orc directory.
---

This descriptor is outside .orc.
`)
	if err := os.Symlink(outsideAgent, filepath.Join(orcDir, "agents", "planner.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	assertLoadErrorContains(t, root, "path must not escape .orc")
}

func validScaffoldPath() string {
	return filepath.Join("..", "initconfig", "scaffold")
}

type projectFixture struct {
	config   string
	workflow string
	agents   map[string]string
}

const duplicateStepWorkflow = `name: implementation
start: plan
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 30m
  report_exit_grace: 30s
  retries: {}
steps:
  plan:
    agent: planner
    allowed_results:
      done: [ready]
    on:
      done/ready: code
  plan:
    agent: coder
    allowed_results:
      done: [ready]
    on:
      done/ready: ready_for_human
`

func workflowYAML(t *testing.T, mutate func(Workflow) Workflow) string {
	t.Helper()
	workflow := minimalWorkflowSpec()
	if mutate != nil {
		workflow = mutate(workflow)
	}
	content, err := yaml.Marshal(workflow)
	if err != nil {
		t.Fatalf("marshal workflow: %v", err)
	}
	return string(content)
}

type invalidWorkflowCase struct {
	name     string
	workflow string
	contains []string
}

func generatedWorkflowCase(t *testing.T, name string, mutate func(Workflow) Workflow, contains ...string) invalidWorkflowCase {
	t.Helper()
	return invalidWorkflowCase{
		name:     name,
		workflow: workflowYAML(t, mutate),
		contains: contains,
	}
}

func workflowWithoutRetries(t *testing.T) string {
	return removeOnce(t, workflowYAML(t, nil), "  retries: {}\n")
}

func minimalWorkflowSpec() Workflow {
	return Workflow{
		Name:  "implementation",
		Start: "plan",
		Execution: Execution{
			Mode: "sequential",
		},
		TaskContext: TaskContext{
			Beads:            "optional",
			MarkdownFallback: RequiredBool{Value: true, Set: true},
		},
		Defaults: Defaults{
			Timeout:         Duration{Duration: 30 * time.Minute, Set: true},
			ReportExitGrace: Duration{Duration: 30 * time.Second, Set: true},
			Retries:         map[string]int{},
		},
		Steps: map[string]Step{
			"plan": {
				Agent:          "planner",
				AllowedResults: map[string][]string{"done": {"ready"}},
				On:             map[string]string{"done/ready": "ready_for_human"},
			},
		},
	}
}

func writeMinimalProject(t *testing.T, fixture projectFixture) string {
	t.Helper()

	root := t.TempDir()
	orcDir := filepath.Join(root, ".orc")
	if err := os.MkdirAll(filepath.Join(orcDir, "agents"), 0o755); err != nil {
		t.Fatalf("create agents dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orcDir, "workflows"), 0o755); err != nil {
		t.Fatalf("create workflows dir: %v", err)
	}

	agents := fixture.agents
	if agents == nil {
		agents = map[string]string{"planner": validAgentDescriptor("planner")}
	}
	config := fixture.config
	if config == "" {
		config = configForAgents(agents)
	}
	workflow := fixture.workflow
	if workflow == "" {
		workflow = workflowYAML(t, nil)
	}

	writeFile(t, filepath.Join(orcDir, "config.yaml"), config)
	writeFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), workflow)
	for id, descriptor := range agents {
		writeFile(t, filepath.Join(orcDir, "agents", id+".md"), descriptor)
	}

	return root
}

func configForAgents(agents map[string]string) string {
	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n")
	for _, id := range ids {
		b.WriteString("  ")
		b.WriteString(id)
		b.WriteString(": agents/")
		b.WriteString(id)
		b.WriteString(".md\n")
	}
	return b.String()
}

func removeOnce(t *testing.T, input, target string) string {
	t.Helper()
	if !strings.Contains(input, target) {
		t.Fatalf("workflow removal target missing: %q", target)
	}
	return strings.Replace(input, target, "", 1)
}

func validAgentDescriptor(id string) string {
	return "---\nid: " + id + "\nrole: " + id + "\ndescription: Test descriptor for " + id + ".\n---\n\nDo the work.\n"
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
}

func assertLoadErrorContains(t *testing.T, root string, wants ...string) {
	t.Helper()
	_, err := Load(root)
	if err == nil {
		t.Fatal("Load returned nil error, want validation error")
	}
	for _, want := range wants {
		assertErrorContains(t, err, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
