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

func TestLoadAcceptsCommandAndScriptSteps(t *testing.T) {
	workflow := `name: implementation
start: check
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
  check:
    kind: command
    command:
      argv: ["task", "check"]
    cwd: tools
    env:
      ORC_MODE: deterministic
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: verify
      done/failed: blocked_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
  verify:
    kind: script
    script:
      path: scripts/verify.sh
      args: ["--strict"]
    allowed_results:
      done: [passed, failed]
      failed: [timeout, process_error]
    on:
      done/passed: ready_for_human
      done/failed: blocked_for_human
      failed/timeout: blocked_for_human
      failed/process_error: blocked_for_human
`
	root := writeMinimalProject(t, projectFixture{workflow: workflow})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	check := project.Workflows["implementation"].Steps["check"]
	if check.EffectiveKind() != StepKindCommand || !slices.Equal(check.Command.Argv, []string{"task", "check"}) {
		t.Fatalf("check step = %+v, want command argv", check)
	}
	verify := project.Workflows["implementation"].Steps["verify"]
	if verify.EffectiveKind() != StepKindScript || verify.Script.Path != "scripts/verify.sh" {
		t.Fatalf("verify step = %+v, want script path", verify)
	}
	if len(project.Workflows["implementation"].ReferencedAgents) != 0 {
		t.Fatalf("referenced agents = %+v, want none for deterministic-only workflow", project.Workflows["implementation"].ReferencedAgents)
	}
}

func TestLoadRejectsInvalidStepKinds(t *testing.T) {
	tests := []struct {
		name     string
		stepYAML string
		contains []string
	}{
		{
			name: "command with agent",
			stepYAML: `kind: command
agent: planner
command:
  argv: ["task", "check"]`,
			contains: []string{`kind command must not set agent`},
		},
		{
			name: "command missing argv",
			stepYAML: `kind: command
command: {}`,
			contains: []string{`command.argv must declare at least one argument`},
		},
		{
			name: "script with traversal path",
			stepYAML: `kind: script
script:
  path: ../verify.sh`,
			contains: []string{`script.path "../verify.sh" must be clean and stay under repository root`},
		},
		{
			name: "agent with command",
			stepYAML: `agent: planner
command:
  argv: ["task", "check"]`,
			contains: []string{`kind agent must not set command`},
		},
		{
			name: "agent with script body",
			stepYAML: `agent: planner
script:
  body: echo unsupported`,
			contains: []string{`script.body is not supported in v1`},
		},
		{
			name: "command with script body",
			stepYAML: `kind: command
command:
  argv: ["task", "check"]
script:
  body: echo unsupported`,
			contains: []string{`script.body is not supported in v1`},
		},
		{
			name: "unsupported kind",
			stepYAML: `kind: shell
command:
  argv: ["task", "check"]`,
			contains: []string{`unsupported kind "shell"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{workflow: workflowWithRawStep(tt.stepYAML)})
			assertLoadErrorContains(t, root, tt.contains...)
		})
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

func TestLoadAcceptsMinimalSandboxConfig(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
sandbox:
  command:
    argv: ["codex"]
`})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if project.Config.Sandbox == nil {
		t.Fatal("sandbox config was nil")
	}
	if got, want := project.Config.Sandbox.Command.Argv, []string{"codex"}; !slices.Equal(got, want) {
		t.Fatalf("sandbox command argv = %v, want %v", got, want)
	}
	if got := project.Config.Sandbox.CWD; got != "." {
		t.Fatalf("sandbox cwd = %q, want .", got)
	}
	if got := project.Config.Sandbox.Bubblewrap.Network; !got.Set || !got.Value {
		t.Fatalf("sandbox bubblewrap network = %+v, want default true", got)
	}
}

func TestLoadAcceptsFullSandboxConfig(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  cwd: tools
  bubblewrap:
    enabled: true
    network: false
    mounts:
      repo: rw
      beads: auto
      codex_home: rw
      tmp: rw
  env:
    pass: ["TERM"]
    set:
      ORC_SANDBOX: "1"
  mounts:
    - host: data
      target: /workspace/data
      mode: ro
    - host: missing-cache
      target: /workspace/cache
      mode: rw
      optional: true
`})
	if err := os.Mkdir(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatalf("create tools dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	sandbox := project.Config.Sandbox
	if sandbox == nil {
		t.Fatal("sandbox config was nil")
	}
	if got := sandbox.CWD; got != "tools" {
		t.Fatalf("sandbox cwd = %q, want tools", got)
	}
	if !sandbox.Bubblewrap.Enabled {
		t.Fatal("sandbox bubblewrap enabled = false, want true")
	}
	if got := sandbox.Bubblewrap.Network; !got.Set || got.Value {
		t.Fatalf("sandbox bubblewrap network = %+v, want explicit false", got)
	}
	if got := sandbox.Bubblewrap.Mounts.Beads; got != "auto" {
		t.Fatalf("sandbox beads mount = %q, want auto", got)
	}
	if got := sandbox.Env.Pass; !slices.Equal(got, []string{"TERM"}) {
		t.Fatalf("sandbox env pass = %v, want TERM", got)
	}
	if got := sandbox.Env.Set["ORC_SANDBOX"]; got != "1" {
		t.Fatalf("sandbox env set ORC_SANDBOX = %q, want 1", got)
	}
	if got := len(sandbox.Mounts); got != 2 {
		t.Fatalf("sandbox mounts length = %d, want 2", got)
	}
}

func TestLoadRejectsInvalidSandboxConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		prepare  func(t *testing.T, root string)
		contains []string
	}{
		{
			name: "missing command argv",
			config: `sandbox:
  command: {}`,
			contains: []string{"sandbox.command.argv must declare at least one argument"},
		},
		{
			name: "empty argv element",
			config: `sandbox:
  command:
    argv: ["codex", ""]`,
			contains: []string{"sandbox.command.argv[1] is empty"},
		},
		{
			name: "shell string command",
			config: `sandbox:
  command: "codex --dangerously-bypass-approvals-and-sandbox"`,
			contains: []string{"sandbox.command must use argv", "shell-string commands are not supported"},
		},
		{
			name: "absolute cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: /tmp`,
			contains: []string{`sandbox.cwd "/tmp" must be repo-relative`},
		},
		{
			name: "traversing cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: ../outside`,
			contains: []string{`sandbox.cwd "../outside" must be clean and stay under repository root`},
		},
		{
			name: "symlink escaping cwd",
			config: `sandbox:
  command:
    argv: ["codex"]
  cwd: linked-outside`,
			prepare: func(t *testing.T, root string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(root, "linked-outside")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			contains: []string{`sandbox.cwd "linked-outside"`, "path must not escape repository root"},
		},
		{
			name: "invalid extra mount mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: .
      target: /workspace
      mode: write`,
			contains: []string{`sandbox.mounts[0].mode "write" is invalid; allowed: ro, rw`},
		},
		{
			name: "missing required mount",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: missing
      target: /workspace/missing
      mode: ro`,
			contains: []string{`sandbox.mounts[0].host "missing" does not exist`},
		},
		{
			name: "invalid preset mount mode",
			config: `sandbox:
  command:
    argv: ["codex"]
  bubblewrap:
    mounts:
      repo: auto`,
			contains: []string{`sandbox.bubblewrap.mounts.repo "auto" is invalid; allowed: ro, rw`},
		},
		{
			name: "unsafe writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../outside
      target: /outside
      mode: rw`,
			prepare: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "..", "outside"), 0o755); err != nil {
					t.Fatalf("create outside dir: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "../outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe optional writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../outside
      target: /outside
      mode: rw
      optional: true`,
			prepare: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "..", "outside"), 0o755); err != nil {
					t.Fatalf("create outside dir: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "../outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe missing optional writable mount traversal",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: ../missing-outside
      target: /outside
      mode: rw
      optional: true`,
			contains: []string{`sandbox.mounts[0].host "../missing-outside" must not traverse outside repository root for writable mounts`},
		},
		{
			name: "unsafe writable mount symlink",
			config: `sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: linked-outside
      target: /outside
      mode: rw`,
			prepare: func(t *testing.T, root string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(root, "linked-outside")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			contains: []string{`sandbox.mounts[0].host "linked-outside" must not escape repository root for writable mounts`},
		},
		{
			name: "empty env pass entry",
			config: `sandbox:
  command:
    argv: ["codex"]
  env:
    pass: ["TERM", ""]`,
			contains: []string{`sandbox.env.pass[1] is empty`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{config: configWithSandbox(tt.config)})
			if tt.prepare != nil {
				tt.prepare(t, root)
			}
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadSkipsMissingOptionalSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(`sandbox:
  command:
    argv: ["codex"]
  mounts:
    - host: missing
      target: /workspace/missing
      mode: rw
      optional: true`)})

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestLoadAcceptsDocumentedOptionalSandboxMount(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{config: configWithSandbox(documentedSandboxConfig(t))})

	if _, err := Load(root); err != nil {
		t.Fatalf("Load returned error: %v", err)
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

func workflowWithRawStep(stepYAML string) string {
	var b strings.Builder
	b.WriteString(`name: implementation
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
`)
	for line := range strings.SplitSeq(strings.TrimRight(stepYAML, "\n"), "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(`    allowed_results:
      done: [passed]
    on:
      done/passed: ready_for_human
`)
	return b.String()
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

func configWithSandbox(sandbox string) string {
	return "version: 1\nworkflows:\n  implementation: workflows/implementation.yaml\nagents:\n  planner: agents/planner.md\n" + sandbox + "\n"
}

func documentedSandboxConfig(t *testing.T) string {
	t.Helper()

	docPath := filepath.Join("..", "..", "docs", "reference", "configuration.md")
	content, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read configuration reference: %v", err)
	}

	const intro = "Project config may also declare an Orc-managed sandbox command contract:"
	afterIntro, ok := cutAfter(string(content), intro)
	if !ok {
		t.Fatalf("configuration reference missing sandbox sample intro %q", intro)
	}

	const fence = "```yaml\n"
	afterFence, ok := cutAfter(afterIntro, fence)
	if !ok {
		t.Fatal("configuration reference sandbox sample is missing opening YAML fence")
	}
	sample, _, ok := strings.Cut(afterFence, "\n```")
	if !ok {
		t.Fatal("configuration reference sandbox sample is missing closing YAML fence")
	}
	return sample
}

func cutAfter(s, sep string) (string, bool) {
	_, after, ok := strings.Cut(s, sep)
	if !ok {
		return "", false
	}
	return after, true
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
