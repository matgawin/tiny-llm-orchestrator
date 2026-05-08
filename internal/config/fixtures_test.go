package config

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

type projectFixture struct {
	config   string
	workflow string
	agents   map[string]string
	runtimes map[string]string
}

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

func readConfigTestdata(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve config testdata path")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(content)
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
			Runtime:         "codex",
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
	if err := os.MkdirAll(filepath.Join(orcDir, "runtimes"), 0o755); err != nil {
		t.Fatalf("create runtimes dir: %v", err)
	}

	agents := fixture.agents
	if agents == nil {
		agents = map[string]string{"planner": validAgentDescriptor("planner")}
	}
	config := fixture.config
	if config == "" {
		config = configForAgents(agents)
	}
	runtimes := fixture.runtimes
	if runtimes == nil {
		runtimes = map[string]string{"codex": validCodexRuntimeDescriptor()}
		if !strings.Contains(config, "\nruntimes:") {
			config += "runtimes:\n  codex: runtimes/codex.yaml\n"
		}
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
	for id, descriptor := range runtimes {
		writeFile(t, filepath.Join(orcDir, "runtimes", id+".yaml"), descriptor)
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
	b.WriteString("runtimes:\n  codex: runtimes/codex.yaml\n")
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

func validScaffoldPath() string {
	return filepath.Join("..", "initconfig", "scaffold")
}
