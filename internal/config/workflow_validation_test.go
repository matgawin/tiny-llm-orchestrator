package config

import (
	"slices"
	"strings"
	"testing"
)

const (
	testRuntimeCodex  = "codex"
	testRuntimeFileAI = "fileai"
	testModelGPT5     = "gpt-5"
	testReasoningHigh = "high"
	testReasoningMed  = "medium"
)

func TestLoadAcceptsCommandAndScriptSteps(t *testing.T) {
	workflow := readConfigTestdata(t, "command_script_workflow.yaml")
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
			step.AllowedResults[SystemSkipStatus] = append(step.AllowedResults[SystemSkipStatus], SystemSkipResult)
			step.On[SystemSkipPair] = testTerminalReadyForHuman
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
	if !slices.Contains(step.AllowedResults[SystemSkipStatus], SystemSkipResult) {
		t.Fatalf("plan allowed %s results = %v, want %s", SystemSkipStatus, step.AllowedResults[SystemSkipStatus], SystemSkipResult)
	}
	if got := step.On[SystemSkipPair]; got != testTerminalReadyForHuman {
		t.Fatalf("plan %s transition = %q, want %s", SystemSkipPair, got, testTerminalReadyForHuman)
	}
}

func TestLoadAcceptsAgentRuntimeSelection(t *testing.T) {
	t.Run("step override wins over workflow defaults", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config: configWithRuntimes(map[string]string{
				testRuntimeCodex:  "runtimes/codex.yaml",
				testRuntimeFileAI: "runtimes/fileai.yaml",
			}),
			runtimes: map[string]string{
				testRuntimeCodex:  validCodexRuntimeDescriptor(),
				testRuntimeFileAI: validFilePromptRuntimeDescriptor(),
			},
			workflow: workflowYAML(t, func(workflow Workflow) Workflow {
				workflow.Defaults.Runtime = testRuntimeCodex
				workflow.Defaults.Reasoning = testReasoningMed
				workflow.Defaults.RuntimeDirs = []string{"shared"}
				step := workflow.Steps["plan"]
				step.Runtime = testRuntimeFileAI
				step.Model = "model-b"
				step.Reasoning = testReasoningHigh
				step.RuntimeDirs = []string{"/tmp/external-worktree"}
				workflow.Steps["plan"] = step
				return workflow
			}),
		})

		project, err := Load(root)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		workflow := project.Workflows["implementation"]
		step := workflow.Steps["plan"]
		if got := workflow.EffectiveRuntime(step); got != testRuntimeFileAI {
			t.Fatalf("effective runtime = %q, want fileai", got)
		}
		if got := workflow.EffectiveModel(step, project.Runtimes[testRuntimeFileAI]); got != "model-b" {
			t.Fatalf("effective model = %q, want model-b", got)
		}
		if got := workflow.EffectiveReasoning(step, project.Runtimes[testRuntimeFileAI]); got != testReasoningHigh {
			t.Fatalf("effective reasoning = %q, want high", got)
		}
		if got, want := workflow.EffectiveRuntimeDirs(step), []string{"shared", "/tmp/external-worktree"}; !slices.Equal(got, want) {
			t.Fatalf("effective runtime dirs = %v, want %v", got, want)
		}
		if got := step.EffectiveAgentID(); got != "planner" {
			t.Fatalf("effective agent id = %q, want planner", got)
		}
	})

	t.Run("workflow defaults supply agent-only step", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			workflow: workflowYAML(t, func(workflow Workflow) Workflow {
				workflow.Defaults.Runtime = testRuntimeCodex
				workflow.Defaults.Model = testModelGPT5
				workflow.Defaults.Reasoning = testReasoningHigh
				workflow.Defaults.RuntimeDirs = []string{"src"}
				return workflow
			}),
		})

		project, err := Load(root)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		workflow := project.Workflows["implementation"]
		step := workflow.Steps["plan"]
		if got := workflow.EffectiveRuntime(step); got != testRuntimeCodex {
			t.Fatalf("effective runtime = %q, want codex", got)
		}
		if got := workflow.EffectiveModel(step, project.Runtimes[testRuntimeCodex]); got != testModelGPT5 {
			t.Fatalf("effective model = %q, want gpt-5", got)
		}
		if got := workflow.EffectiveReasoning(step, project.Runtimes[testRuntimeCodex]); got != testReasoningHigh {
			t.Fatalf("effective reasoning = %q, want high", got)
		}
	})

	t.Run("runtime default supplies reasoning when workflow omits it", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			workflow: workflowYAML(t, func(workflow Workflow) Workflow {
				workflow.Defaults.Runtime = testRuntimeCodex
				return workflow
			}),
		})

		project, err := Load(root)
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		workflow := project.Workflows["implementation"]
		step := workflow.Steps["plan"]
		if got := workflow.EffectiveReasoning(step, project.Runtimes[testRuntimeCodex]); got != testReasoningMed {
			t.Fatalf("effective reasoning = %q, want medium", got)
		}
	})
}

func TestLoadRejectsInvalidSkippableStepContract(t *testing.T) {
	tests := []invalidWorkflowCase{
		generatedWorkflowCase(t, "skippable missing allowed result", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.Skippable = true
			step.On[SystemSkipPair] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" is skippable`, `allowed_results.done including skipped`),
		generatedWorkflowCase(t, "skippable missing transition", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.Skippable = true
			step.AllowedResults[SystemSkipStatus] = append(step.AllowedResults[SystemSkipStatus], SystemSkipResult)
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" is skippable`, `on transition for done/skipped`),
		generatedWorkflowCase(t, "non skippable allowed result", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.AllowedResults[SystemSkipStatus] = append(step.AllowedResults[SystemSkipStatus], SystemSkipResult)
			step.On[SystemSkipPair] = testTerminalReadyForHuman
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" declares reserved system outcome done/skipped but is not skippable`),
		generatedWorkflowCase(t, "non skippable transition", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.On[SystemSkipPair] = testTerminalReadyForHuman
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

func TestLoadRejectsInvalidAgentRuntimeSelection(t *testing.T) {
	tests := []invalidWorkflowCase{
		generatedWorkflowCase(t, "missing effective runtime", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = ""
			return workflow
		}, `step "plan" runtime is required for agent steps`),
		generatedWorkflowCase(t, "missing runtime reference", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "missing"
			return workflow
		}, `step "plan" references missing runtime "missing"`),
		generatedWorkflowCase(t, "missing required model", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "requiredmodel"
			return workflow
		}, `step "plan" runtime "requiredmodel" requires a model`),
		generatedWorkflowCase(t, "step model unsupported", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "nomodel"
			step := workflow.Steps["plan"]
			step.Model = testModelGPT5
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" model requires runtime "nomodel" model.supported=true`),
		generatedWorkflowCase(t, "default model unsupported", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "nomodel"
			workflow.Defaults.Model = testModelGPT5
			return workflow
		}, `step "plan" defaults.model requires runtime "nomodel" model.supported=true`),
		generatedWorkflowCase(t, "allowlist rejects step model", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = testRuntimeFileAI
			step := workflow.Steps["plan"]
			step.Model = "model-z"
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" model "model-z" is not allowed by runtime "fileai" model.allowed`),
		generatedWorkflowCase(t, "allowlist rejects default model", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = testRuntimeFileAI
			workflow.Defaults.Model = "model-z"
			return workflow
		}, `step "plan" defaults.model "model-z" is not allowed by runtime "fileai" model.allowed`),
		generatedWorkflowCase(t, "runtime dirs unsupported", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "nodirs"
			step := workflow.Steps["plan"]
			step.RuntimeDirs = []string{"extra"}
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" runtime_dirs require runtime "nodirs" directories.supported=true`),
		generatedWorkflowCase(t, "missing required reasoning", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "requiredreasoning"
			return workflow
		}, `step "plan" runtime "requiredreasoning" requires reasoning`),
		generatedWorkflowCase(t, "step reasoning unsupported", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "noreasoning"
			step := workflow.Steps["plan"]
			step.Reasoning = testReasoningMed
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" reasoning requires runtime "noreasoning" reasoning.supported=true`),
		generatedWorkflowCase(t, "default reasoning unsupported", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "noreasoning"
			workflow.Defaults.Reasoning = testReasoningMed
			return workflow
		}, `step "plan" defaults.reasoning requires runtime "noreasoning" reasoning.supported=true`),
		generatedWorkflowCase(t, "allowlist rejects step reasoning", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = testRuntimeFileAI
			step := workflow.Steps["plan"]
			step.Reasoning = "extreme"
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" reasoning "extreme" is not allowed by runtime "fileai" reasoning.allowed`),
		generatedWorkflowCase(t, "allowlist rejects default reasoning", func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = testRuntimeFileAI
			workflow.Defaults.Reasoning = "extreme"
			return workflow
		}, `step "plan" defaults.reasoning "extreme" is not allowed by runtime "fileai" reasoning.allowed`),
		generatedWorkflowCase(t, "empty default runtime dir", func(workflow Workflow) Workflow {
			workflow.Defaults.RuntimeDirs = []string{""}
			return workflow
		}, `defaults.runtime_dirs[0] is empty`),
		generatedWorkflowCase(t, "unclean default runtime dir", func(workflow Workflow) Workflow {
			workflow.Defaults.RuntimeDirs = []string{"work/../work"}
			return workflow
		}, `defaults.runtime_dirs[0] "work/../work" must be clean`),
		generatedWorkflowCase(t, "env default runtime dir", func(workflow Workflow) Workflow {
			workflow.Defaults.RuntimeDirs = []string{"$WORKTREE"}
			return workflow
		}, `defaults.runtime_dirs[0] "$WORKTREE" must not use shell, environment, or tilde expansion`),
		generatedWorkflowCase(t, "traversal step runtime dir", func(workflow Workflow) Workflow {
			step := workflow.Steps["plan"]
			step.RuntimeDirs = []string{"../outside"}
			workflow.Steps["plan"] = step
			return workflow
		}, `step "plan" runtime_dirs[0] "../outside" must be repo-relative or absolute and stay under repository root`),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				config: configWithRuntimes(map[string]string{
					testRuntimeCodex:    "runtimes/codex.yaml",
					testRuntimeFileAI:   "runtimes/fileai.yaml",
					"nomodel":           "runtimes/nomodel.yaml",
					"noreasoning":       "runtimes/noreasoning.yaml",
					"nodirs":            "runtimes/nodirs.yaml",
					"requiredmodel":     "runtimes/requiredmodel.yaml",
					"requiredreasoning": "runtimes/requiredreasoning.yaml",
				}),
				workflow: tt.workflow,
				runtimes: map[string]string{
					testRuntimeCodex:    validCodexRuntimeDescriptor(),
					testRuntimeFileAI:   validFilePromptRuntimeDescriptor(),
					"nomodel":           validNoModelRuntimeDescriptor("nomodel"),
					"noreasoning":       validNoReasoningRuntimeDescriptor("noreasoning"),
					"nodirs":            validNoDirsRuntimeDescriptor("nodirs"),
					"requiredmodel":     validRequiredModelRuntimeDescriptor("requiredmodel"),
					"requiredreasoning": validRequiredReasoningRuntimeDescriptor("requiredreasoning"),
				},
			})
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
			name: "command with runtime",
			stepYAML: `kind: command
runtime: codex
command:
  argv: ["task", "check"]`,
			contains: []string{`kind command must not set runtime`},
		},
		{
			name: "command with model",
			stepYAML: `kind: command
model: gpt-5
command:
  argv: ["task", "check"]`,
			contains: []string{`kind command must not set model`},
		},
		{
			name: "command with reasoning",
			stepYAML: `kind: command
reasoning: medium
command:
  argv: ["task", "check"]`,
			contains: []string{`kind command must not set reasoning`},
		},
		{
			name: "command with runtime dirs",
			stepYAML: `kind: command
runtime_dirs: ["src"]
command:
  argv: ["task", "check"]`,
			contains: []string{`kind command must not set runtime_dirs`},
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
			name: "script with runtime",
			stepYAML: `kind: script
runtime: codex
script:
  path: scripts/verify.sh`,
			contains: []string{`kind script must not set runtime`},
		},
		{
			name: "script with model",
			stepYAML: `kind: script
model: gpt-5
script:
  path: scripts/verify.sh`,
			contains: []string{`kind script must not set model`},
		},
		{
			name: "script with reasoning",
			stepYAML: `kind: script
reasoning: medium
script:
  path: scripts/verify.sh`,
			contains: []string{`kind script must not set reasoning`},
		},
		{
			name: "script with runtime dirs",
			stepYAML: `kind: script
runtime_dirs: ["src"]
script:
  path: scripts/verify.sh`,
			contains: []string{`kind script must not set runtime_dirs`},
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
			workflow: duplicateStepWorkflow(t),
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
	t.Helper()
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

func duplicateStepWorkflow(t *testing.T) string {
	t.Helper()
	return readConfigTestdata(t, "duplicate_step_workflow.yaml")
}
