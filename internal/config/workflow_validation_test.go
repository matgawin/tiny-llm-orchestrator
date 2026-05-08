package config

import (
	"slices"
	"strings"
	"testing"
)

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

func TestLoadAcceptsSkippableStepContract(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{
		workflow: workflowYAML(t, func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.Skippable = true
			step.AllowedResults["done"] = append(step.AllowedResults["done"], "skipped")
			step.On["done/skipped"] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}),
	})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	step := project.Workflows["implementation"].Steps["plan"]
	if !step.Skippable {
		t.Fatal("plan skippable = false, want true")
	}
	if !slices.Contains(step.AllowedResults["done"], "skipped") {
		t.Fatalf("plan allowed done results = %v, want skipped", step.AllowedResults["done"])
	}
	if got := step.On["done/skipped"]; got != testTerminalReadyForHuman {
		t.Fatalf("plan done/skipped transition = %q, want %s", got, testTerminalReadyForHuman)
	}
}

func TestLoadRejectsInvalidSkippableStepContract(t *testing.T) {
	tests := []invalidWorkflowCase{
		generatedWorkflowCase(t, "skippable missing allowed result", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.Skippable = true
			step.On["done/skipped"] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" is skippable`, `allowed_results.done including skipped`),
		generatedWorkflowCase(t, "skippable missing transition", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.Skippable = true
			step.AllowedResults["done"] = append(step.AllowedResults["done"], "skipped")
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" is skippable`, `on transition for done/skipped`),
		generatedWorkflowCase(t, "non skippable allowed result", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.AllowedResults["done"] = append(step.AllowedResults["done"], "skipped")
			step.On["done/skipped"] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" declares reserved system outcome done/skipped but is not skippable`),
		generatedWorkflowCase(t, "non skippable transition", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.On["done/skipped"] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" declares reserved system transition done/skipped but is not skippable`),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{workflow: tt.workflow})
			assertLoadErrorContains(t, root, tt.contains...)
		})
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
