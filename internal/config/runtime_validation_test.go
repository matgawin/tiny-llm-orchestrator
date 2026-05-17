package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/testutil"
)

func TestLoadValidRuntimeDescriptors(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{
		config: configWithRuntimes(map[string]string{
			"codex":  "runtimes/codex.yaml",
			"fileai": "runtimes/fileai.yaml",
		}),
		runtimes: map[string]string{
			"codex":  validCodexRuntimeDescriptor(),
			"fileai": validFilePromptRuntimeDescriptor(),
		},
	})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	codex := project.Runtimes["codex"]
	if codex.ID != "codex" {
		t.Fatalf("codex runtime id = %q, want codex", codex.ID)
	}

	if got, want := codex.Command.Executable, "codex"; got != want {
		t.Fatalf("codex executable = %q, want %q", got, want)
	}

	if !codex.Model.Supported {
		t.Fatal("codex model.supported = false, want true")
	}

	if len(codex.Model.Allowed) != 0 {
		t.Fatalf("codex model.allowed = %v, want empty pass-through list", codex.Model.Allowed)
	}

	if !codex.Reasoning.Supported {
		t.Fatal("codex reasoning.supported = false, want true")
	}

	if got, want := codex.Reasoning.Default, "medium"; got != want {
		t.Fatalf("codex reasoning.default = %q, want %q", got, want)
	}

	if got, want := project.Runtimes["fileai"].Prompt.Delivery, "file"; got != want {
		t.Fatalf("fileai prompt.delivery = %q, want %q", got, want)
	}

	if got, want := project.Runtimes["fileai"].Sandbox.Requirements.Env.Set["ORC_RUNTIME"], "fileai"; got != want {
		t.Fatalf("fileai sandbox env = %q, want %q", got, want)
	}
}

func TestLoadRuntimeSelectionAllowlistValidation(t *testing.T) {
	tests := []struct {
		name       string
		descriptor string
		contains   []string
	}{
		{
			name: "model missing allowed means pass through",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "model",
				defaultName: "gpt-9",
			}),
		},
		{
			name: "model empty allowed means pass through",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "model",
				defaultName: "gpt-9",
				allowed:     []string{},
			}),
		},
		{
			name: "model default constrained by allowlist",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "model",
				defaultName: "gpt-9",
				allowed:     []string{"gpt-5"},
			}),
			contains: []string{`runtime "codex" file "runtimes/codex.yaml"`, `model.default "gpt-9" is not allowed by model.allowed`},
		},
		{
			name: "model empty allowlist entry",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection: "model",
				allowed:   []string{`""`},
			}),
			contains: []string{`model.allowed[0] is empty`},
		},
		{
			name: "reasoning missing allowed means pass through",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "reasoning",
				defaultName: "effort-9",
			}),
		},
		{
			name: "reasoning empty allowed means pass through",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "reasoning",
				defaultName: "effort-9",
				allowed:     []string{},
			}),
		},
		{
			name: "reasoning default constrained by allowlist",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection:   "reasoning",
				defaultName: "effort-9",
				allowed:     []string{"medium"},
			}),
			contains: []string{`runtime "codex" file "runtimes/codex.yaml"`, `reasoning.default "effort-9" is not allowed by reasoning.allowed`},
		},
		{
			name: "reasoning empty allowlist entry",
			descriptor: runtimeSelectionDescriptor(runtimeSelectionDescriptorOptions{
				selection: "reasoning",
				allowed:   []string{`""`},
			}),
			contains: []string{`reasoning.allowed[0] is empty`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				config:   configWithRuntimes(map[string]string{"codex": "runtimes/codex.yaml"}),
				runtimes: map[string]string{"codex": tt.descriptor},
			})
			if len(tt.contains) == 0 {
				if _, err := Load(root); err != nil {
					t.Fatalf("Load returned error: %v", err)
				}

				return
			}

			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

type runtimeSelectionDescriptorOptions struct {
	selection   string
	defaultName string
	allowed     []string
}

func runtimeSelectionDescriptor(options runtimeSelectionDescriptorOptions) string {
	var builder strings.Builder
	builder.WriteString(`id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
`)

	if options.selection == "model" {
		writeRuntimeSelectionDescriptorFields(&builder, options)
	} else {
		builder.WriteString("  supported: false\n")
	}

	if options.selection == "reasoning" {
		builder.WriteString("reasoning:\n")
		writeRuntimeSelectionDescriptorFields(&builder, options)
		builder.WriteString(`  args: [--reasoning, "{reasoning}"]
`)
	}

	builder.WriteString(`directories:
  supported: false
sandbox:
  supported: true
`)

	return builder.String()
}

func writeRuntimeSelectionDescriptorFields(builder *strings.Builder, options runtimeSelectionDescriptorOptions) {
	builder.WriteString("  supported: true\n")

	if options.defaultName != "" {
		builder.WriteString("  default: " + options.defaultName + "\n")
	}

	if options.allowed != nil {
		builder.WriteString("  allowed: [" + strings.Join(options.allowed, ", ") + "]\n")
	}
}

func TestLoadRuntimeSandboxRequirementsExtendedSchema(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{
		config: configWithRuntimes(map[string]string{"custom": "runtimes/custom.yaml"}),
		workflow: workflowYAML(t, func(workflow Workflow) Workflow {
			workflow.Defaults.Runtime = "custom"
			return workflow
		}),
		runtimes: map[string]string{"custom": `id: custom
command:
  executable: recorder
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
  requirements:
    env:
      pass: [CUSTOM_TOKEN]
      set:
        ORC_RUNTIME: custom
      set_from_mount:
        CUSTOM_HOME:
          mount: config_home
          value: target
    mounts:
      - id: config_home
        source:
          env: CUSTOM_HOME
          fallback:
            host_home: .custom
          create: true
        target:
          env_same_as_source: true
          fallback:
            sandbox_home: .custom
        mode: rw
      - host: .orc/cache/custom
        target: /workspace/.orc/cache/custom
        mode: rw
        optional: true
`},
	})

	project, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	requirements := project.Runtimes["custom"].Sandbox.Requirements
	if got := requirements.Mounts[0].ID; got != "config_home" {
		t.Fatalf("extended mount id = %q, want config_home", got)
	}

	if got := requirements.Env.SetFromMount["CUSTOM_HOME"].Mount; got != "config_home" {
		t.Fatalf("set_from_mount mount = %q, want config_home", got)
	}

	if got := requirements.Mounts[1].Target.Path; got != "/workspace/.orc/cache/custom" {
		t.Fatalf("simple mount target = %q, want legacy scalar target", got)
	}
}

func TestLoadRejectsInvalidRuntimeSandboxRequirementExtendedSchema(t *testing.T) {
	tests := []struct {
		name     string
		mount    string
		env      string
		contains []string
	}{
		{
			name: "invalid mount id",
			mount: `      - id: bad.id
        host: .orc/cache/custom
        target: /workspace/.orc/cache/custom
        mode: rw
`,
			contains: []string{`sandbox.requirements.mounts[0].id "bad.id" is invalid`},
		},
		{
			name: "duplicate mount id",
			mount: `      - id: cache
        host: .orc/cache/custom
        target: /workspace/.orc/cache/custom
        mode: rw
      - id: cache
        host: .orc/cache/other
        target: /workspace/.orc/cache/other
        mode: rw
`,
			contains: []string{`sandbox.requirements.mounts[1].id "cache" duplicates another sandbox.requirements.mounts id`},
		},
		{
			name: "invalid set from mount env name",
			env: `      set_from_mount:
        BAD-NAME:
          mount: cache
          value: target
`,
			mount: `      - id: cache
        host: .orc/cache/custom
        target: /workspace/.orc/cache/custom
        mode: rw
`,
			contains: []string{`sandbox.requirements.env: set_from_mount["BAD-NAME"]: environment variable name "BAD-NAME" is invalid`},
		},
		{
			name: "invalid source combination",
			mount: `      - host: .orc/cache/custom
        source:
          env: CUSTOM_HOME
        target:
          env_same_as_source: true
        mode: rw
`,
			contains: []string{`sandbox.requirements.mounts[0] must not combine simple host with extended source`},
		},
		{
			name: "invalid target combination",
			mount: `      - host: .orc/cache/custom
        target:
          env_same_as_source: true
        mode: rw
`,
			contains: []string{`sandbox.requirements.mounts[0].target is required`},
		},
		{
			name: "unsupported expansion syntax",
			mount: `      - source:
          env: CUSTOM_HOME
          fallback:
            host_home: $CUSTOM_HOME
        target:
          env_same_as_source: true
          fallback:
            sandbox_home: .custom
        mode: rw
`,
			contains: []string{`sandbox.requirements.mounts[0].source.fallback.host_home "$CUSTOM_HOME" must not use shell, environment, or tilde expansion`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.env
			if env == "" {
				env = "      set: {}\n"
			}

			root := writeMinimalProject(t, projectFixture{
				config: configWithRuntimes(map[string]string{"custom": "runtimes/custom.yaml"}),
				workflow: workflowYAML(t, func(workflow Workflow) Workflow {
					workflow.Defaults.Runtime = "custom"
					return workflow
				}),
				runtimes: map[string]string{"custom": `id: custom
command:
  executable: recorder
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
  requirements:
    env:
` + env + `    mounts:
` + tt.mount},
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsInvalidRuntimeDescriptors(t *testing.T) {
	tests := []struct {
		name       string
		descriptor string
		contains   []string
	}{
		{
			name: "id mismatch",
			descriptor: `id: other
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`runtime "codex" file "runtimes/codex.yaml"`, `id "other" does not match runtime map key`},
		},
		{
			name: "empty executable",
			descriptor: `id: codex
command:
  executable: ""
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.executable: is required`},
		},
		{
			name: "executable placeholder",
			descriptor: `id: codex
command:
  executable: codex-{model}
prompt:
  delivery: stdin
model:
  supported: true
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.executable: must not contain placeholders`},
		},
		{
			name: "empty command arg",
			descriptor: `id: codex
command:
  executable: codex
  normal_args: [""]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.normal_args[0] is empty`},
		},
		{
			name: "invalid prompt delivery",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: pipe
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`prompt.delivery "pipe" is invalid; allowed: stdin, file`},
		},
		{
			name: "unknown placeholder",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--task={task}"]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.args[0] contains unknown placeholder {task}`},
		},
		{
			name: "unknown model arg placeholder",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: true
  args: ["--model", "{provider_model}"]
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`model.args[1] contains unknown placeholder {provider_model}`},
		},
		{
			name: "unknown reasoning arg placeholder",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
reasoning:
  supported: true
  args: ["--reasoning", "{provider_reasoning}"]
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`reasoning.args[1] contains unknown placeholder {provider_reasoning}`},
		},
		{
			name: "malformed placeholder",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--model={model"]
prompt:
  delivery: stdin
model:
  supported: true
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.args[0] contains malformed placeholder syntax`},
		},
		{
			name: "unknown directory arg placeholder",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: true
  args: ["--dir", "{dir}", "--label", "{label}"]
sandbox:
  supported: true
`,
			contains: []string{`directories.args[3] contains unknown placeholder {label}`},
		},
		{
			name: "prompt file placeholder requires file delivery",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--prompt", "{prompt_file}"]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.args[1] placeholder {prompt_file} requires prompt.delivery=file`},
		},
		{
			name: "model placeholder requires model support",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--model", "{model}"]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.args[1] placeholder {model} requires model.supported=true`},
		},
		{
			name: "dir placeholder only in directory args",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--add-dir", "{dir}"]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: true
  args: ["--add-dir", "{dir}"]
sandbox:
  supported: true
`,
			contains: []string{`command.args[1] placeholder {dir} is valid only in directories.args`},
		},
		{
			name: "reasoning placeholder only in reasoning args",
			descriptor: `id: codex
command:
  executable: codex
  args: ["--reasoning", "{reasoning}"]
prompt:
  delivery: stdin
model:
  supported: false
reasoning:
  supported: true
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`command.args[1] placeholder {reasoning} is valid only in reasoning.args`},
		},
		{
			name: "model placeholder invalid in reasoning args",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: true
reasoning:
  supported: true
  args: ["--reasoning", "{model}"]
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`reasoning.args[1] placeholder {model} is not valid in reasoning.args`},
		},
		{
			name: "unsupported model rejects model fields",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
  default: gpt-5
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`model.default requires model.supported=true`},
		},
		{
			name: "unsupported reasoning rejects reasoning fields",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
reasoning:
  supported: false
  default: medium
directories:
  supported: false
sandbox:
  supported: true
`,
			contains: []string{`reasoning.default requires reasoning.supported=true`},
		},
		{
			name: "unsupported directories rejects args",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
  args: ["--add-dir", "{dir}"]
sandbox:
  supported: true
`,
			contains: []string{`directories.args requires directories.supported=true`},
		},
		{
			name: "supported directories require args",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: true
sandbox:
  supported: true
`,
			contains: []string{`directories.args must include {dir} when directories.supported=true`},
		},
		{
			name: "supported directories require dir placeholder",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: true
  args: ["--add-dir"]
sandbox:
  supported: true
`,
			contains: []string{`directories.args must include {dir} when directories.supported=true`},
		},
		{
			name: "sandbox required needs support",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: false
  required: true
`,
			contains: []string{`sandbox.required requires sandbox.supported=true`},
		},
		{
			name: "invalid sandbox requirement env",
			descriptor: `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
  requirements:
    env:
      pass: [BAD-NAME]
`,
			contains: []string{`sandbox.requirements.env: sandbox.env.pass[0]: environment variable name "BAD-NAME" is invalid`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeMinimalProject(t, projectFixture{
				config:   configWithRuntimes(map[string]string{"codex": "runtimes/codex.yaml"}),
				runtimes: map[string]string{"codex": tt.descriptor},
			})
			assertLoadErrorContains(t, root, tt.contains...)
		})
	}
}

func TestLoadRejectsSelectedRuntimeSandboxRequirementStaticConflict(t *testing.T) {
	root := writeMinimalProject(t, projectFixture{
		config: configWithRuntimes(map[string]string{"codex": "runtimes/codex.yaml"}) + `
sandbox:
  command:
    argv: [sh]
  cwd: .
  env:
    set:
      ORC_RUNTIME: project
`,
		runtimes: map[string]string{"codex": `id: codex
command:
  executable: codex
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: false
sandbox:
  supported: true
  requirements:
    env:
      set:
        ORC_RUNTIME: codex
`},
	})

	assertLoadErrorContains(t, root, `runtime "codex" sandbox.requirements.env.set.ORC_RUNTIME conflicts with another fixed sandbox environment value for ORC_RUNTIME`)
}

func TestLoadRejectsUnsafeRuntimeReferences(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config: configWithRuntimes(map[string]string{"codex": "runtimes/missing.yaml"}),
		})
		assertLoadErrorContains(t, root, `runtime "codex" path "runtimes/missing.yaml"`)
	})

	t.Run("absolute path", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config: configWithRuntimes(map[string]string{"codex": filepath.Join(t.TempDir(), "codex.yaml")}),
		})
		assertLoadErrorContains(t, root, `runtime "codex" path`, `path must be relative to .orc`)
	})

	t.Run("traversal path", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config: configWithRuntimes(map[string]string{"codex": "../codex.yaml"}),
		})
		assertLoadErrorContains(t, root, `runtime "codex" path "../codex.yaml": path must not escape .orc`)
	})

	t.Run("outside runtimes directory", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config: configWithRuntimes(map[string]string{"codex": "agents/planner.md"}),
		})
		assertLoadErrorContains(t, root, `runtime "codex" path "agents/planner.md" must be under runtimes/`)
	})

	t.Run("symlink escape", func(t *testing.T) {
		root := writeMinimalProject(t, projectFixture{
			config:   configWithRuntimes(map[string]string{"codex": "runtimes/codex.yaml"}),
			runtimes: map[string]string{"codex": validCodexRuntimeDescriptor()},
		})
		outside := filepath.Join(t.TempDir(), "codex.yaml")
		writeFile(t, outside, validCodexRuntimeDescriptor())

		runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
		if err := os.Remove(runtimePath); err != nil {
			t.Fatalf("remove runtime fixture: %v", err)
		}

		if err := os.Symlink(outside, runtimePath); err != nil {
			t.Fatalf("create symlink: %v", err)
		}

		assertLoadErrorContains(t, root, `runtime "codex" path "runtimes/codex.yaml": path must not escape .orc`)
	})
}

func configWithRuntimes(runtimes map[string]string) string {
	var config strings.Builder
	config.WriteString(`version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  planner: agents/planner.md
runtimes:
`)

	for id, path := range runtimes {
		config.WriteString("  " + id + ": " + path + "\n")
	}

	return config.String()
}

func validCodexRuntimeDescriptor() string {
	return testutil.CodexRuntimeYAML()
}

func validFilePromptRuntimeDescriptor() string {
	return `id: fileai
command:
  executable: fileai
  args: [run, --prompt-file, "{prompt_file}", --agent, "{agent_id}", --attempt, "{attempt_id}"]
prompt:
  delivery: file
model:
  supported: true
  required: true
  default: model-a
  allowed: [model-a, model-b]
  args: [--model, "{model}"]
reasoning:
  supported: true
  required: false
  default: medium
  allowed: [low, medium, high]
  args: [--reasoning, "{reasoning}"]
directories:
  supported: true
  args: [--dir, "{dir}"]
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: [OPENAI_API_KEY]
      set:
        ORC_RUNTIME: fileai
    mounts:
      - host: .orc/cache/fileai
        target: /workspace/.orc/cache/fileai
        mode: rw
        optional: true
`
}

func validNoModelRuntimeDescriptor(id string) string {
	return `id: ` + id + `
command:
  executable: ` + id + `
  args: [run]
prompt:
  delivery: stdin
model:
  supported: false
directories:
  supported: true
  args: [--dir, "{dir}"]
sandbox:
  supported: true
  required: false
`
}

func validNoDirsRuntimeDescriptor(id string) string {
	return `id: ` + id + `
command:
  executable: ` + id + `
  args: [run]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  args: [--model, "{model}"]
directories:
  supported: false
sandbox:
  supported: true
  required: false
`
}

func validNoReasoningRuntimeDescriptor(id string) string {
	return `id: ` + id + `
command:
  executable: ` + id + `
  args: [run]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  args: [--model, "{model}"]
reasoning:
  supported: false
directories:
  supported: true
  args: [--dir, "{dir}"]
sandbox:
  supported: true
  required: false
`
}

func validRequiredModelRuntimeDescriptor(id string) string {
	return `id: ` + id + `
command:
  executable: ` + id + `
  args: [run]
prompt:
  delivery: stdin
model:
  supported: true
  required: true
  args: [--model, "{model}"]
directories:
  supported: true
  args: [--dir, "{dir}"]
sandbox:
  supported: true
  required: false
`
}

func validRequiredReasoningRuntimeDescriptor(id string) string {
	return `id: ` + id + `
command:
  executable: ` + id + `
  args: [run]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  args: [--model, "{model}"]
reasoning:
  supported: true
  required: true
  args: [--reasoning, "{reasoning}"]
directories:
  supported: true
  args: [--dir, "{dir}"]
sandbox:
  supported: true
  required: false
`
}
