package launcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

func TestLaunchNextRunsCustomRuntimeWithStdinPromptAndModeArgs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		sandbox bool
		wantArg string
	}{
		{name: "normal", wantArg: "arg:1=normal"},
		{name: "sandbox", sandbox: true, wantArg: "arg:1=sandbox"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			recordPath := filepath.Join(root, "runtime-record.txt")

			runID := writeCustomRuntimeLauncherProject(t, root, customRuntimeProject{
				RecordPath:     recordPath,
				PromptDelivery: runtimePromptDeliveryStdin,
				IncludeSandbox: true,
				Runtime: customRuntimeDescriptor{
					ModelSupported: true,
					DirsSupported:  true,
				},
				Workflow: customRuntimeWorkflow{
					DefaultsRuntime: "recorder",
				},
			})
			if tt.sandbox {
				t.Setenv("ORC_SANDBOX", "1")
				t.Setenv("ORC_SANDBOX_ROOT", root)
			} else {
				t.Setenv("ORC_SANDBOX", "0")
				t.Setenv("ORC_SANDBOX_ROOT", root)
			}

			result, err := LaunchNext(context.Background(), Options{
				Root:  root,
				RunID: runID,
				Time:  fixedLauncherTime(),
			})
			if err != nil {
				t.Fatalf("LaunchNext returned error: %v", err)
			}

			if !result.Launched {
				t.Fatal("Launched = false, want true")
			}

			if result.Attempt.State != runstore.AttemptStateMissingReport || result.Attempt.Result != resultMissingReport {
				t.Fatalf("attempt = %+v, want missing_report after fixture exit", result.Attempt)
			}

			record := string(readLauncherFile(t, recordPath))
			assertRecordContains(t, record,
				"arg:0=--mode",
				tt.wantArg,
				"env:ORC_RUN_ID=custom-runtime-run",
				"env:ORC_STEP_ID=code",
				"env:ORC_PROGRESS_SOCKET=",
				"env:ORC_PROGRESS_TOKEN=",
				"stdin:# Tiny Orc Worker Prompt",
				"stdin:Launch a worker.",
			)
			assertRecordNotContains(t, record, "--model")
		})
	}
}

func TestLaunchNextCustomRuntimeFilePromptStepOverridesModelAndRuntime(t *testing.T) {
	root := t.TempDir()
	recordPath := filepath.Join(root, "recorder.txt")
	fallbackRecordPath := filepath.Join(root, "fallback.txt")
	runID := writeCustomRuntimeLauncherProject(t, root, customRuntimeProject{
		RecordPath:         recordPath,
		FallbackRecordPath: fallbackRecordPath,
		PromptDelivery:     runtimePromptDeliveryFile,
		Runtime: customRuntimeDescriptor{
			ModelSupported: true,
			ModelDefault:   "runtime-default",
			DirsSupported:  true,
		},
		Workflow: customRuntimeWorkflow{
			DefaultsRuntime: "fallback",
			DefaultsModel:   "workflow-model",
			DefaultsDirs:    []string{"shared"},
			StepRuntime:     "recorder",
			StepModel:       "step-model",
			StepDirs:        []string{"/tmp/external-worktree"},
		},
	})

	result, err := LaunchNext(context.Background(), Options{
		Root:  root,
		RunID: runID,
		Time:  fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("LaunchNext returned error: %v", err)
	}

	if !result.Launched {
		t.Fatal("Launched = false, want true")
	}

	if _, err := os.Stat(fallbackRecordPath); !os.IsNotExist(err) {
		t.Fatalf("fallback runtime record stat error = %v, want not launched", err)
	}

	record := string(readLauncherFile(t, recordPath))
	assertRecordContains(t, record,
		"arg:1=normal",
		"arg:4=--prompt-file",
		"arg:6=--model",
		"arg:7=step-model",
		"arg:8=--dir",
		"arg:9="+filepath.Join(root, "shared"),
		"arg:10=--dir",
		"arg:11=/tmp/external-worktree",
		"prompt_file_content:# Tiny Orc Worker Prompt",
		"prompt_file_content:Launch a worker.",
	)
	assertRecordNotContains(t, record, "workflow-model", "runtime-default", "stdin:")
}

func TestLaunchNextCustomRuntimeModelPrecedenceAndOmission(t *testing.T) {
	for _, tt := range []struct {
		name          string
		runtimeModel  string
		workflowModel string
		want          string
		notWant       []string
	}{
		{
			name:          "workflow default overrides runtime default",
			runtimeModel:  "runtime-default",
			workflowModel: "workflow-model",
			want:          "arg:7=workflow-model",
			notWant:       []string{"runtime-default"},
		},
		{
			name:    "model args omitted when no model resolves",
			notWant: []string{"--model", "arg:6="},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			recordPath := filepath.Join(root, "runtime-record.txt")
			runID := writeCustomRuntimeLauncherProject(t, root, customRuntimeProject{
				RecordPath:     recordPath,
				PromptDelivery: runtimePromptDeliveryFile,
				Runtime: customRuntimeDescriptor{
					ModelSupported: true,
					ModelDefault:   tt.runtimeModel,
					DirsSupported:  true,
				},
				Workflow: customRuntimeWorkflow{
					DefaultsRuntime: "recorder",
					DefaultsModel:   tt.workflowModel,
				},
			})

			result, err := LaunchNext(context.Background(), Options{
				Root:  root,
				RunID: runID,
				Time:  fixedLauncherTime(),
			})
			if err != nil {
				t.Fatalf("LaunchNext returned error: %v", err)
			}

			if !result.Launched {
				t.Fatal("Launched = false, want true")
			}

			record := string(readLauncherFile(t, recordPath))
			if tt.want != "" {
				assertRecordContains(t, record, tt.want)
			}

			assertRecordNotContains(t, record, tt.notWant...)
		})
	}
}

func TestLaunchNextIgnoresLiveRuntimeCapabilityEditsAfterSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name       string
		liveUpdate func(t *testing.T, root, recordPath string)
		wantRecord string
	}{
		{
			name: "live runtime removes model capability",
			liveUpdate: func(t *testing.T, root, recordPath string) {
				t.Helper()
				writeLauncherFile(t, filepath.Join(root, ".orc", "runtimes", "recorder.yaml"), customRuntimeYAML("recorder", runtimeRecorderFixturePath(t), recordPath, runtimePromptDeliveryFile, customRuntimeDescriptor{
					ModelSupported: false,
					DirsSupported:  true,
				}))
			},
			wantRecord: "arg:7=snapshot-model",
		},
		{
			name: "live runtime removes directory capability",
			liveUpdate: func(t *testing.T, root, recordPath string) {
				t.Helper()
				writeLauncherFile(t, filepath.Join(root, ".orc", "runtimes", "recorder.yaml"), customRuntimeYAML("recorder", runtimeRecorderFixturePath(t), recordPath, runtimePromptDeliveryFile, customRuntimeDescriptor{
					ModelSupported: true,
					DirsSupported:  false,
				}))
			},
			wantRecord: "arg:9=",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			recordPath := filepath.Join(root, "runtime-record.txt")
			runID := writeCustomRuntimeLauncherProject(t, root, customRuntimeProject{
				RecordPath:     recordPath,
				PromptDelivery: runtimePromptDeliveryFile,
				Runtime: customRuntimeDescriptor{
					ModelSupported: true,
					DirsSupported:  true,
				},
				Workflow: customRuntimeWorkflow{
					DefaultsRuntime: "recorder",
					DefaultsModel:   "snapshot-model",
					DefaultsDirs:    []string{"shared"},
				},
			})
			tt.liveUpdate(t, root, recordPath)

			result, err := LaunchNext(context.Background(), Options{
				Root:  root,
				RunID: runID,
				Time:  fixedLauncherTime(),
			})
			if err != nil {
				t.Fatalf("LaunchNext returned error after live config mutation: %v", err)
			}

			if !result.Launched {
				t.Fatal("Launched = false, want snapshot-backed launch")
			}

			record := string(readLauncherFile(t, recordPath))
			assertRecordContains(t, record, tt.wantRecord)

			if !strings.Contains(record, "prompt_file_content:# Tiny Orc Worker Prompt") {
				t.Fatalf("record = %q, want prompt from snapshot-backed launch", record)
			}
		})
	}
}

type customRuntimeProject struct {
	RecordPath         string
	FallbackRecordPath string
	PromptDelivery     string
	IncludeSandbox     bool
	Runtime            customRuntimeDescriptor
	Workflow           customRuntimeWorkflow
}

type customRuntimeDescriptor struct {
	ModelSupported bool
	ModelDefault   string
	DirsSupported  bool
}

type customRuntimeWorkflow struct {
	DefaultsRuntime string
	DefaultsModel   string
	DefaultsDirs    []string
	StepRuntime     string
	StepModel       string
	StepDirs        []string
}

func writeCustomRuntimeLauncherProject(t *testing.T, root string, project customRuntimeProject) string {
	t.Helper()

	orcDir := filepath.Join(root, ".orc")
	for _, dir := range []string{"agents", "runtimes", "workflows"} {
		if err := os.MkdirAll(filepath.Join(orcDir, dir), 0o750); err != nil {
			t.Fatalf("create %s dir: %v", dir, err)
		}
	}

	configYAML := `version: 1
workflows:
  implementation: workflows/implementation.yaml
agents:
  coder: agents/coder.md
runtimes:
  recorder: runtimes/recorder.yaml
`
	if project.FallbackRecordPath != "" {
		configYAML += "  fallback: runtimes/fallback.yaml\n"
	}

	if project.IncludeSandbox {
		configYAML += `sandbox:
  command:
    argv: ["sh", "-c", "true"]
`
	}

	writeLauncherFile(t, filepath.Join(orcDir, "config.yaml"), configYAML)
	writeLauncherFile(t, filepath.Join(orcDir, "agents", "coder.md"), "---\nid: coder\nrole: coder\ndescription: Test coder.\n---\n\nCode.\n")
	fixturePath := runtimeRecorderFixturePath(t)
	writeLauncherFile(t, filepath.Join(orcDir, "runtimes", "recorder.yaml"), customRuntimeYAML("recorder", fixturePath, project.RecordPath, project.PromptDelivery, project.Runtime))

	if project.FallbackRecordPath != "" {
		writeLauncherFile(t, filepath.Join(orcDir, "runtimes", "fallback.yaml"), customRuntimeYAML("fallback", fixturePath, project.FallbackRecordPath, project.PromptDelivery, project.Runtime))
	}

	writeLauncherFile(t, filepath.Join(orcDir, "workflows", "implementation.yaml"), customRuntimeWorkflowYAML(project.Workflow))

	store := openLauncherStore(t, root)

	run, err := store.Create(runstore.CreateRunRequest{
		RunID:        "custom-runtime-run",
		Workflow:     "implementation",
		InitialState: "code",
		Time:         fixedLauncherTime(),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	writeLauncherConfigSnapshot(t, root, store, run.ID)

	if _, err := store.WriteArtifact(run.ID, runstore.Artifact{
		Kind:    runstore.KindTaskContext,
		Name:    "task",
		Content: []byte("# Task\n\nLaunch a worker.\n"),
		Time:    fixedLauncherTime(),
	}); err != nil {
		t.Fatalf("WriteArtifact task returned error: %v", err)
	}

	return run.ID
}

func customRuntimeYAML(id, executable, recordPath, delivery string, descriptor customRuntimeDescriptor) string {
	var out strings.Builder
	out.WriteString("id: " + id + "\n")
	out.WriteString("command:\n")
	out.WriteString("  executable: " + strconv.Quote(executable) + "\n")
	out.WriteString("  normal_args: [--mode, normal]\n")
	out.WriteString("  sandbox_args: [--mode, sandbox]\n")
	out.WriteString("  args: [--record, " + strconv.Quote(filepath.ToSlash(recordPath)))

	if delivery == runtimePromptDeliveryFile {
		out.WriteString(", --prompt-file, \"{prompt_file}\"")
	}

	out.WriteString("]\n")
	out.WriteString("prompt:\n  delivery: " + delivery + "\n")
	out.WriteString("model:\n")
	out.WriteString("  supported: " + strconv.FormatBool(descriptor.ModelSupported) + "\n")
	out.WriteString("  required: false\n")

	if descriptor.ModelSupported {
		if descriptor.ModelDefault != "" {
			out.WriteString("  default: " + strconv.Quote(descriptor.ModelDefault) + "\n")
		}

		out.WriteString("  allowed: []\n")
		out.WriteString("  args: [--model, \"{model}\"]\n")
	}

	out.WriteString("directories:\n")
	out.WriteString("  supported: " + strconv.FormatBool(descriptor.DirsSupported) + "\n")

	if descriptor.DirsSupported {
		out.WriteString("  args: [--dir, \"{dir}\"]\n")
	}

	out.WriteString("sandbox:\n  supported: true\n  required: false\n")

	return out.String()
}

func runtimeRecorderFixturePath(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime recorder fixture: runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(file), "testdata", "runtime-recorder.sh")

	return filepath.ToSlash(path)
}

func customRuntimeWorkflowYAML(workflow customRuntimeWorkflow) string {
	var out strings.Builder
	out.WriteString(`name: implementation
start: code
execution:
  mode: sequential
task_context:
  beads: optional
  markdown_fallback: true
defaults:
  timeout: 500ms
  report_exit_grace: 30ms
  retries: {}
`)

	if workflow.DefaultsRuntime != "" {
		out.WriteString("  runtime: " + workflow.DefaultsRuntime + "\n")
	}

	if workflow.DefaultsModel != "" {
		out.WriteString("  model: " + workflow.DefaultsModel + "\n")
	}

	writeRuntimeDirsYAML(&out, "  ", "runtime_dirs", workflow.DefaultsDirs)
	out.WriteString("steps:\n  code:\n    agent: coder\n")

	if workflow.StepRuntime != "" {
		out.WriteString("    runtime: " + workflow.StepRuntime + "\n")
	}

	if workflow.StepModel != "" {
		out.WriteString("    model: " + workflow.StepModel + "\n")
	}

	writeRuntimeDirsYAML(&out, "    ", "runtime_dirs", workflow.StepDirs)
	out.WriteString(`    allowed_results:
      done: [ready]
      failed: [missing_report, process_error, timeout]
    on:
      done/ready: ready_for_human
      failed/missing_report: blocked_for_human
      failed/process_error: blocked_for_human
      failed/timeout: blocked_for_human
`)

	return out.String()
}

func writeRuntimeDirsYAML(out *strings.Builder, indent, key string, dirs []string) {
	if len(dirs) == 0 {
		return
	}

	out.WriteString(indent + key + ":\n")

	for _, dir := range dirs {
		out.WriteString(indent + "  - " + strconv.Quote(dir) + "\n")
	}
}

func assertRecordContains(t *testing.T, record string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(record, want) {
			t.Fatalf("record = %q, want %q", record, want)
		}
	}
}

func assertRecordNotContains(t *testing.T, record string, notWants ...string) {
	t.Helper()

	for _, notWant := range notWants {
		if strings.Contains(record, notWant) {
			t.Fatalf("record = %q, did not want %q", record, notWant)
		}
	}
}
