package launcher

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/promptrender"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstore"
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
					Sandbox: &config.SandboxConfig{},
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
