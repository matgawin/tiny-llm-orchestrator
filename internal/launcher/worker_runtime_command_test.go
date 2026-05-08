package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/sandbox"
)

const launcherPlanStep = "plan"

func TestRuntimeCommandBuildsCodexNormalAndSandboxArgv(t *testing.T) {
	root := t.TempDir()
	codex := config.Runtime{
		ID: "codex",
		Command: config.RuntimeCommand{
			Executable:  "codex",
			NormalArgs:  []string{"--ask-for-approval", "never"},
			SandboxArgs: []string{"--dangerously-bypass-approvals-and-sandbox"},
			Args:        []string{"exec", "--skip-git-repo-check", "-"},
		},
		Prompt: config.RuntimePrompt{Delivery: runtimePromptDeliveryStdin},
		Model:  config.RuntimeModel{Supported: true, Args: []string{"--model", "{model}"}},
		Sandbox: config.RuntimeSandbox{
			Supported: true,
		},
	}
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "codex"},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}

	t.Run("normal", func(t *testing.T) {
		t.Setenv("ORC_SANDBOX", "0")
		t.Setenv("ORC_SANDBOX_ROOT", root)
		command, promptMode, err := runtimeCommandForTest(root, workflow, codex)
		if err != nil {
			t.Fatalf("runtimeCommand returned error: %v", err)
		}
		want := []string{"codex", "--ask-for-approval", "never", "exec", "--skip-git-repo-check", "-"}
		if !slices.Equal(command, want) {
			t.Fatalf("command = %#v, want %#v", command, want)
		}
		if promptMode != runtimePromptDeliveryStdin {
			t.Fatalf("promptMode = %q, want stdin", promptMode)
		}
	})

	t.Run("sandbox", func(t *testing.T) {
		t.Setenv("ORC_SANDBOX", "1")
		t.Setenv("ORC_SANDBOX_ROOT", root)
		command, promptMode, err := runtimeCommandForTest(root, workflow, codex)
		if err != nil {
			t.Fatalf("runtimeCommand returned error: %v", err)
		}
		want := []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "exec", "--skip-git-repo-check", "-"}
		if !slices.Equal(command, want) {
			t.Fatalf("command = %#v, want %#v", command, want)
		}
		if promptMode != runtimePromptDeliveryStdin {
			t.Fatalf("promptMode = %q, want stdin", promptMode)
		}
	})
}

func TestRuntimeCommandSubstitutesModelPromptMetadataAndDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORC_SANDBOX", "0")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	fileAI := config.Runtime{
		ID: "fileai",
		Command: config.RuntimeCommand{
			Executable: "fileai",
			NormalArgs: []string{
				"--attempt", "{attempt_id}",
			},
			Args: []string{
				"--prompt", "{prompt_file}",
				"--run-step", "{run_id}:{step_id}",
				"--agent", "{agent_id}",
			},
		},
		Prompt:      config.RuntimePrompt{Delivery: runtimePromptDeliveryFile},
		Model:       config.RuntimeModel{Supported: true, Args: []string{"--model", "{model}"}},
		Directories: config.RuntimeDirectories{Supported: true, Args: []string{"--add-dir", "{dir}"}},
		Sandbox:     config.RuntimeSandbox{Supported: true},
	}
	workflow := config.Workflow{
		Defaults: config.Defaults{
			Runtime:     "fileai",
			Model:       "workflow-model",
			RuntimeDirs: []string{"shared"},
		},
		Steps: map[string]config.Step{
			"code": {
				Agent:       "coder",
				Model:       "step-model",
				RuntimeDirs: []string{"/tmp/external"},
			},
		},
	}

	command, promptMode, err := runtimeCommandForTest(root, workflow, fileAI)
	if err != nil {
		t.Fatalf("runtimeCommand returned error: %v", err)
	}
	promptPath := filepath.ToSlash(filepath.Join(root, ".orc", "runs", "run-1", "prompts", "code.md"))
	want := []string{
		"fileai",
		"--attempt", "attempt-1",
		"--prompt", promptPath,
		"--run-step", "run-1:code",
		"--agent", "coder",
		"--model", "step-model",
		"--add-dir", filepath.Join(root, "shared"),
		"--add-dir", "/tmp/external",
	}
	if !slices.Equal(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
	if promptMode != runtimePromptDeliveryFile {
		t.Fatalf("promptMode = %q, want file", promptMode)
	}
}

func TestRuntimeCommandSandboxRuntimeDirsAllowRepositoryRelativeDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "shared"), 0o750); err != nil {
		t.Fatalf("create shared runtime dir: %v", err)
	}
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	setRuntimeDirCoverageEnv(t, root)
	runtime := runtimeWithDirs("recorder")
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{"shared", "shared"}},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}

	command, _, err := runtimeCommandForTest(root, workflow, runtime)
	if err != nil {
		t.Fatalf("runtimeCommand returned error: %v", err)
	}
	want := []string{"recorder", "--dir", filepath.Join(root, "shared"), "--dir", filepath.Join(root, "shared")}
	if !slices.Equal(command, want) {
		t.Fatalf("command = %#v, want duplicate runtime_dirs preserved as %#v", command, want)
	}
}

func TestRuntimeCommandSandboxRuntimeDirsAllowProjectSandboxMount(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external-worktree")
	if err := os.Mkdir(external, 0o750); err != nil {
		t.Fatalf("create external runtime dir: %v", err)
	}
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	setRuntimeDirCoverageEnv(t, root, external)
	runtime := runtimeWithDirs("recorder")
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{external}},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}
	sandboxConfig := config.SandboxConfig{
		Mounts: []config.SandboxMount{{Host: external, Target: external, Mode: "rw"}},
	}

	command, _, err := runtimeCommandForTestWithSandbox(root, workflow, runtime, sandboxConfig)
	if err != nil {
		t.Fatalf("runtimeCommand returned error: %v", err)
	}
	want := []string{"recorder", "--dir", external}
	if !slices.Equal(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestRuntimeCommandSandboxRuntimeDirsAllowRuntimeSandboxRequirement(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "runtime-required")
	if err := os.Mkdir(external, 0o750); err != nil {
		t.Fatalf("create runtime-required dir: %v", err)
	}
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	setRuntimeDirCoverageEnv(t, root, external)
	runtime := runtimeWithDirs("recorder")
	runtime.Sandbox.Requirements.Mounts = []config.RuntimeSandboxMount{
		{Host: external, Target: config.RuntimeSandboxMountTarget{Path: external}, Mode: "rw"},
	}
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{external}},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}

	command, _, err := runtimeCommandForTest(root, workflow, runtime)
	if err != nil {
		t.Fatalf("runtimeCommand returned error: %v", err)
	}
	want := []string{"recorder", "--dir", external}
	if !slices.Equal(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestRuntimeCommandSandboxRuntimeDirsUseActiveSandboxCoverage(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "runtime-required")
	if err := os.Mkdir(external, 0o750); err != nil {
		t.Fatalf("create runtime-required dir: %v", err)
	}
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	setRuntimeDirCoverageEnv(t, root, external)
	runtime := runtimeWithDirs("recorder")
	runtime.Sandbox.Requirements.Mounts = []config.RuntimeSandboxMount{
		{
			ID: "config_home",
			Source: config.RuntimeSandboxMountSource{
				Env: "RECORDER_HOME",
			},
			Target: config.RuntimeSandboxMountTarget{
				EnvSameAsSource: true,
			},
			Mode: "rw",
		},
	}
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{external}},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}

	command, _, err := runtimeCommandForTest(root, workflow, runtime)
	if err != nil {
		t.Fatalf("runtimeCommand returned error: %v", err)
	}
	want := []string{"recorder", "--dir", external}
	if !slices.Equal(command, want) {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestRuntimeCommandSandboxRuntimeDirsRejectMissingOrNonDirectoryBeforeArgv(t *testing.T) {
	for _, tt := range []struct {
		name    string
		setup   func(t *testing.T, root string) string
		wantErr string
	}{
		{
			name: "missing",
			setup: func(t *testing.T, root string) string {
				return "missing"
			},
			wantErr: "not visible inside the active sandbox",
		},
		{
			name: "file",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "runtime-file")
				if err := os.WriteFile(path, []byte("not a dir"), 0o640); err != nil {
					t.Fatalf("create runtime file: %v", err)
				}
				return "runtime-file"
			},
			wantErr: "visible path is not a directory",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := tt.setup(t, root)
			t.Setenv("ORC_SANDBOX", "1")
			t.Setenv("ORC_SANDBOX_ROOT", root)
			setRuntimeDirCoverageEnv(t, root)
			runtime := runtimeWithDirs("recorder")
			workflow := config.Workflow{
				Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{dir}},
				Steps: map[string]config.Step{
					launcherPlanStep: {Agent: "planner"},
				},
			}

			_, _, err := runtimeCommandForTest(root, workflow, runtime)
			resolved := effectiveRuntimeDir(root, dir)
			for _, want := range []string{
				`step "plan"`,
				`runtime "recorder"`,
				`runtime_dirs value "` + dir + `"`,
				`resolved to "` + resolved + `"`,
				tt.wantErr,
			} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("runtimeCommand error = %v, want %q", err, want)
				}
			}
		})
	}
}

func TestRuntimeCommandSandboxRuntimeDirsRejectUncoveredAbsoluteDir(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "unmounted")
	if err := os.Mkdir(external, 0o750); err != nil {
		t.Fatalf("create unmounted dir: %v", err)
	}
	t.Setenv("ORC_SANDBOX", "1")
	t.Setenv("ORC_SANDBOX_ROOT", root)
	setRuntimeDirCoverageEnv(t, root)
	runtime := runtimeWithDirs("recorder")
	workflow := config.Workflow{
		Defaults: config.Defaults{Runtime: "recorder", RuntimeDirs: []string{external}},
		Steps: map[string]config.Step{
			launcherPlanStep: {Agent: "planner"},
		},
	}

	_, _, err := runtimeCommandForTest(root, workflow, runtime)
	for _, want := range []string{
		`step "plan"`,
		`runtime "recorder"`,
		`runtime_dirs value "` + external + `"`,
		`resolved to "` + external + `"`,
		"not covered by the repository mount",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("runtimeCommand error = %v, want %q", err, want)
		}
	}
}

func TestRuntimeCommandRejectsPlaceholderWithoutValue(t *testing.T) {
	_, err := substituteRuntimePlaceholders([]string{"--model", "{model}"}, runtimePlaceholderValues{})
	if err == nil {
		t.Fatal("substituteRuntimePlaceholders returned nil error, want missing value")
	}
	if got, want := err.Error(), "placeholder {model} has no value"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestRuntimeCommandRejectsUnknownPlaceholder(t *testing.T) {
	_, err := substituteRuntimePlaceholders([]string{"--bad", "{unknown}"}, runtimePlaceholderValues{})
	if err == nil {
		t.Fatal("substituteRuntimePlaceholders returned nil error, want unknown placeholder")
	}
	if got, want := err.Error(), "unknown placeholder {unknown}"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func runtimeCommandForTest(root string, workflow config.Workflow, runtime config.Runtime) ([]string, string, error) {
	return runtimeCommandForTestWithSandbox(root, workflow, runtime, config.SandboxConfig{})
}

func runtimeCommandForTestWithSandbox(root string, workflow config.Workflow, runtime config.Runtime, sandboxConfig config.SandboxConfig) ([]string, string, error) {
	stepID := launcherPlanStep
	agentID := "planner"
	if _, ok := workflow.Steps[launcherCodeStep]; ok {
		stepID = launcherCodeStep
		agentID = "coder"
	}
	runner := workerRunner{
		loaded: runcontext.Context{
			Project: &config.Project{
				Root: root,
				Config: config.ProjectConfig{
					Sandbox: &sandboxConfig,
				},
				Runtimes: map[string]config.Runtime{runtime.ID: runtime},
			},
			Workflow: workflow,
			Run:      &runstore.Run{ID: "run-1"},
		},
		attempt: runstore.Attempt{
			StepID:    stepID,
			AgentID:   agentID,
			AttemptID: "attempt-1",
		},
		prompt: promptrender.Result{
			Path: filepath.ToSlash(filepath.Join(root, ".orc", "runs", "run-1", "prompts", stepID+".md")),
		},
	}
	return runner.runtimeCommand()
}

func runtimeWithDirs(id string) config.Runtime {
	return config.Runtime{
		ID: id,
		Command: config.RuntimeCommand{
			Executable: id,
		},
		Prompt:      config.RuntimePrompt{Delivery: runtimePromptDeliveryStdin},
		Directories: config.RuntimeDirectories{Supported: true, Args: []string{"--dir", "{dir}"}},
		Sandbox:     config.RuntimeSandbox{Supported: true},
	}
}

func setRuntimeDirCoverageEnv(t *testing.T, targets ...string) {
	t.Helper()
	value, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("marshal runtime dir coverage: %v", err)
	}
	t.Setenv(sandbox.RuntimeDirCoverageEnv, string(value))
}
