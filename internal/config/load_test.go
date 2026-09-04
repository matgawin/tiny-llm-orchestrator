//nolint:goconst // Test strings are clearer in place.
package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
		"review-fix",
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

func TestLoadCurrentRepositoryConfigUsesExplicitCodexRuntime(t *testing.T) {
	project, err := Load(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := project.Config.Runtimes["codex"]; got != "runtimes/codex.yaml" {
		t.Fatalf("config runtimes.codex = %q, want runtimes/codex.yaml", got)
	}

	codex, ok := project.Runtimes["codex"]
	if !ok {
		t.Fatal("runtime codex was not loaded")
	}

	wantArgs := []string{"exec", "--skip-git-repo-check", "-"}
	if !slices.Equal(codex.Command.Args, wantArgs) {
		t.Fatalf("codex command args = %#v, want %#v", codex.Command.Args, wantArgs)
	}

	for name, workflow := range project.Workflows {
		if got := workflow.Defaults.Runtime; got != "codex" {
			t.Fatalf("workflow %s defaults.runtime = %q, want codex", name, got)
		}
	}
}

func TestLoadSetupVersion(t *testing.T) {
	project, err := Load(validScaffoldPath())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := project.Config.SetupVersion; got != CurrentSetupVersion {
		t.Fatalf("setup_version = %d, want %d", got, CurrentSetupVersion)
	}

	legacyRoot := writeMinimalProject(t, projectFixture{config: configForAgents(map[string]string{"planner": validAgentDescriptor("planner")})})

	legacyProject, err := Load(legacyRoot)
	if err != nil {
		t.Fatalf("Load legacy config returned error: %v", err)
	}

	if got := legacyProject.Config.SetupVersion; got != 0 {
		t.Fatalf("legacy setup_version = %d, want 0", got)
	}
}

func TestLoadRejectsInvalidSetupVersion(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name: "negative",
			config: `version: 1
setup_version: -1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
runtimes:
  codex: runtimes/codex.yaml
`,
			want: "setup_version = -1, want >= 0",
		},
		{
			name: "newer than supported",
			config: `version: 1
setup_version: 2
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
runtimes:
  codex: runtimes/codex.yaml
`,
			want: "setup_version = 2, want <= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: tt.config})
			assertLoadErrorContains(t, root, tt.want)
		})
	}
}

func TestLoadDefaultScaffoldSkippablePolicy(t *testing.T) {
	project, err := Load(validScaffoldPath())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	tests := []struct {
		workflow string
		step     string
		target   string
	}{
		{workflow: "implementation", step: "code", target: "redundancy-review"},
		{workflow: "implementation", step: "review", target: "redundancy-review"},
		{workflow: "implementation", step: "redundancy-review", target: "readability-review"},
		{workflow: "implementation", step: "code_fixer", target: "readability-review"},
		{workflow: "implementation", step: "readability-review", target: "ready_for_human"},
		{workflow: "implementation", step: "code_cleaner", target: "ready_for_human"},
		{workflow: "bugfix", step: "code", target: "ready_for_human"},
		{workflow: "bugfix", step: "review", target: "ready_for_human"},
		{workflow: "mechanical-change", step: "mechanical-code", target: "ready_for_human"},
		{workflow: "mechanical-change", step: "mechanical-review", target: "ready_for_human"},
		{workflow: "test-only", step: "test-code", target: "ready_for_human"},
		{workflow: "test-only", step: "review", target: "ready_for_human"},
		{workflow: "review-fix", step: "review", target: "redundancy-review"},
		{workflow: "review-fix", step: "code", target: "redundancy-review"},
		{workflow: "review-fix", step: "redundancy-review", target: "readability-review"},
		{workflow: "review-fix", step: "code_fixer", target: "readability-review"},
		{workflow: "review-fix", step: "readability-review", target: "ready_for_human"},
		{workflow: "review-fix", step: "code_cleaner", target: "ready_for_human"},
		{workflow: "review-mechanical", step: "mechanical-review", target: "ready_for_human"},
		{workflow: "review-readability", step: "readability-review", target: "ready_for_human"},
		{workflow: "review-redundancy", step: "redundancy-review", target: "ready_for_human"},
		{workflow: "review-docs", step: "docs-review", target: "ready_for_human"},
	}
	for _, tt := range tests {
		t.Run(tt.workflow+"/"+tt.step, func(t *testing.T) {
			assertSkippableStep(t, project.Workflows[tt.workflow].Steps[tt.step], tt.target)
		})
	}

	nonSkippable := map[string][]string{
		"implementation":     {"plan", "lsp", "test", "lsp-redundancy", "test-redundancy", "lsp-readability", "test-readability"},
		"bugfix":             {"reproduce", "plan", "lsp", "test"},
		"mechanical-change":  {"plan", "lsp", "test"},
		"test-only":          {"plan", "test-design", "lsp", "test-run"},
		"review-fix":         {"lsp", "test", "lsp-redundancy", "test-redundancy", "lsp-readability", "test-readability"},
		"review-mechanical":  {},
		"review-readability": {},
		"review-redundancy":  {},
		"review-docs":        {},
	}
	for workflowName, stepNames := range nonSkippable {
		workflow := project.Workflows[workflowName]
		for _, stepName := range stepNames {
			t.Run(workflowName+"/"+stepName, func(t *testing.T) {
				assertNotSkippableStep(t, workflow.Steps[stepName])
			})
		}
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

func TestValidateLoopMemberRejectsConflictingSharedLimits(t *testing.T) {
	members := map[string]string{}
	if err := validateLoopMember("code", "coding", 2, 3, members); err != nil {
		t.Fatal(err)
	}

	err := validateLoopMember("fix", "coding", 1, 2, members)
	if err == nil || !strings.Contains(err.Error(), `loop key "coding" has conflicting limits: steps "code" and "fix"`) {
		t.Fatalf("validateLoopMember error = %v", err)
	}
}

func TestLoadRejectsExplicitLoopKeyWithDifferentImplicitLimits(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{workflow: workflowYAML(t, func(w Workflow) Workflow {
		plan := w.Steps["plan"]
		plan.Loop = &StepLoopConfig{Key: "code", Soft: 2, Hard: 3}
		plan.On = map[string]string{"done/ready": "code"}
		w.Steps["plan"] = plan
		w.Steps["code"] = Step{
			Agent:          "planner",
			AllowedResults: map[string][]string{"done": {"ready"}},
			On:             map[string]string{"done/ready": "ready_for_human"},
		}

		return w
	})})

	assertLoadErrorContains(t, root, `workflow "implementation": loop key "code" has conflicting limits`)
}

func TestLoadStepLoopOnAllStepKinds(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{workflow: workflowYAML(t, func(w Workflow) Workflow {
		w.Steps = map[string]Step{
			"plan": {
				Agent: "planner", Loop: &StepLoopConfig{Key: "coding", Soft: 2, Hard: 3},
				AllowedResults: map[string][]string{"done": {"ready"}}, On: map[string]string{"done/ready": "command"},
			},
			"command": {
				Kind: StepKindCommand, Command: CommandStep{Argv: []string{"true"}}, Loop: &StepLoopConfig{Key: "coding", Soft: 2, Hard: 3},
				AllowedResults: map[string][]string{"done": {"passed"}}, On: map[string]string{"done/passed": "script"},
			},
			"script": {
				Kind: StepKindScript, Script: ScriptStep{Path: "check.sh"}, Loop: &StepLoopConfig{Key: "coding", Soft: 2, Hard: 3},
				AllowedResults: map[string][]string{"done": {"passed"}}, On: map[string]string{"done/passed": "ready_for_human"},
			},
		}

		return w
	})})
	writeFile(t, filepath.Join(root, "check.sh"), "#!/bin/sh\nexit 0\n")

	project, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, stepID := range []string{"plan", "command", "script"} {
		if got := project.Workflows["implementation"].Steps[stepID].Loop; got == nil || *got != (StepLoopConfig{Key: "coding", Soft: 2, Hard: 3}) {
			t.Fatalf("step %q loop = %+v", stepID, got)
		}
	}
}

func TestValidateStepLoopRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name string
		loop StepLoopConfig
		want string
	}{
		{name: "missing key", loop: StepLoopConfig{Soft: 2, Hard: 3}, want: "loop.key"},
		{name: "invalid key", loop: StepLoopConfig{Key: "1coding", Soft: 2, Hard: 3}, want: "must match"},
		{name: "missing soft", loop: StepLoopConfig{Key: "coding", Hard: 3}, want: "loop.soft must be > 0"},
		{name: "negative soft", loop: StepLoopConfig{Key: "coding", Soft: -1, Hard: 3}, want: "loop.soft must be > 0"},
		{name: "missing hard", loop: StepLoopConfig{Key: "coding", Soft: 2}, want: "loop.hard must be greater"},
		{name: "hard equals soft", loop: StepLoopConfig{Key: "coding", Soft: 2, Hard: 2}, want: "loop.hard must be greater"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{workflow: workflowYAML(t, func(w Workflow) Workflow {
				step := w.Steps["plan"]
				step.Loop = &tt.loop
				w.Steps["plan"] = step

				return w
			})})
			assertLoadErrorContains(t, root, tt.want)
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

func assertSkippableStep(t *testing.T, step Step, target string) {
	t.Helper()

	if !step.Skippable {
		t.Fatalf("step skippable = false, want true")
	}

	if !slices.Contains(step.AllowedResults[SystemSkipStatus], SystemSkipResult) {
		t.Fatalf("step allowed %s results = %v, want %s", SystemSkipStatus, step.AllowedResults[SystemSkipStatus], SystemSkipResult)
	}

	if got := step.On[SystemSkipPair]; got != target {
		t.Fatalf("step %s transition = %q, want %q", SystemSkipPair, got, target)
	}
}

func assertNotSkippableStep(t *testing.T, step Step) {
	t.Helper()

	if step.Skippable {
		t.Fatal("step skippable = true, want false")
	}

	if slices.Contains(step.AllowedResults[SystemSkipStatus], SystemSkipResult) {
		t.Fatalf("step allowed %s results = %v, want no %s", SystemSkipStatus, step.AllowedResults[SystemSkipStatus], SystemSkipResult)
	}

	if _, ok := step.On[SystemSkipPair]; ok {
		t.Fatalf("step declares %s transition, want none", SystemSkipPair)
	}
}
