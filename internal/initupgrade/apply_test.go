package initupgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
)

func TestApplyRefusesPlanConflictsBeforeWriting(t *testing.T) {
	root := legacyScaffold(t)
	configPath := filepath.Join(root, ".orc", "config.yaml")
	before := readFile(t, configPath)

	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")
	result := mustPlan(t, root)

	_, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want conflict refusal")
	}

	if !strings.Contains(err.Error(), "customized-scaffold-file") {
		t.Fatalf("Apply error = %v, want customized-scaffold-file", err)
	}

	if got := readFile(t, configPath); got != before {
		t.Fatalf("config changed after conflict refusal:\n%s", got)
	}
}

func TestApplyRejectsChangedDuringApplyBeforeAnyWrite(t *testing.T) {
	root := legacyScaffold(t)

	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	result := mustPlan(t, root)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: 1\n", "version: 1\n# concurrent edit\n")

	_, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want changed-during-apply rejection")
	}

	if !strings.Contains(err.Error(), "changed during init upgrade apply") {
		t.Fatalf("Apply error = %v, want changed-during-apply message", err)
	}

	if _, err := os.Stat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime stat error = %v, want still missing", err)
	}
}

func TestApplyCreatesMissingScaffoldFileFromPlan(t *testing.T) {
	root := legacyScaffold(t)

	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.Contains(applied.CreatedPaths, ".orc/runtimes/codex.yaml") {
		t.Fatalf("created paths = %#v, want runtime", applied.CreatedPaths)
	}

	content := readFile(t, runtimePath)
	if !strings.Contains(content, "id: codex\n") {
		t.Fatalf("runtime content missing scaffold runtime:\n%s", content)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplyRefusesCreateThroughSymlinkedParent(t *testing.T) {
	root := legacyScaffold(t)

	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	runtimesDir := filepath.Join(root, ".orc", "runtimes")
	if err := os.Remove(runtimesDir); err != nil {
		t.Fatalf("remove runtime dir: %v", err)
	}

	runTargetDir := filepath.Join(root, ".orc", "runs", "redirect")
	if err := os.MkdirAll(runTargetDir, 0o750); err != nil {
		t.Fatalf("create run target dir: %v", err)
	}

	if err := os.Symlink(runTargetDir, runtimesDir); err != nil {
		t.Fatalf("symlink runtime dir: %v", err)
	}

	result := mustPlan(t, root)

	_, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want symlink parent refusal")
	}

	if !strings.Contains(err.Error(), "unsafe symlink parent .orc/runtimes") {
		t.Fatalf("Apply error = %v, want unsafe symlink parent", err)
	}

	if _, err := os.Stat(filepath.Join(runTargetDir, "codex.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run target stat error = %v, want no created file under runs", err)
	}

	if strings.Contains(readFile(t, filepath.Join(root, ".orc", "config.yaml")), "setup_version") {
		t.Fatalf("config was modified despite unsafe create parent")
	}
}

func TestApplyReplacesKnownBaselineWithCurrentScaffold(t *testing.T) {
	original := knownReplacementBaselines
	knownReplacementBaselines = map[string][][]byte{
		".orc/agents/planner.md": {[]byte("known v0 planner\n")},
	}

	t.Cleanup(func() { knownReplacementBaselines = original })

	root := legacyScaffold(t)
	target := filepath.Join(root, ".orc", "agents", "planner.md")
	writeFile(t, target, "known v0 planner\n")

	result := mustPlan(t, root)
	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	want := string(scaffoldByPath()[".orc/agents/planner.md"])
	if got := readFile(t, target); got != want {
		t.Fatalf("planner content = %q, want current scaffold %q", got, want)
	}
}

func TestApplyMigratesConfigSurgically(t *testing.T) {
	root := legacyScaffold(t)
	configPath := filepath.Join(root, ".orc", "config.yaml")
	replaceInFile(t, configPath, "version: 1\n", "# keep top comment\nversion: 1\n")
	replaceInFile(t, configPath, "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  # keep default comment\n  max_loops: 3\n")

	result := mustPlan(t, root)
	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, configPath)
	for _, want := range []string{
		"# keep top comment\nversion: 1\nsetup_version: 1\n",
		"defaults:\n  # keep default comment\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("config content missing %q:\n%s", want, content)
		}
	}

	if strings.Contains(content, "max_loops") {
		t.Fatalf("config still contains max_loops:\n%s", content)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplyPreservesInlineCommentWhenSettingYAMLField(t *testing.T) {
	root := currentScaffold(t)
	configPath := filepath.Join(root, ".orc", "config.yaml")
	replaceInFile(t, configPath, "setup_version: 1\n", "setup_version: 0 # keep setup note\n")

	result := mustPlan(t, root)
	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, configPath)
	if !strings.Contains(content, "setup_version: 1 # keep setup note\n") {
		t.Fatalf("config did not preserve setup_version inline comment:\n%s", content)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplyReportsStaleFilesWithoutDeleting(t *testing.T) {
	original := removedManagedScaffoldFiles
	removedManagedScaffoldFiles = []removedManagedScaffoldFile{{
		Path:     ".orc/workflows/old-managed.yaml",
		Reason:   "removed from scaffold",
		Guidance: "leave or remove manually",
	}}

	t.Cleanup(func() { removedManagedScaffoldFiles = original })

	root := legacyScaffold(t)
	stalePath := filepath.Join(root, ".orc", "workflows", "old-managed.yaml")
	writeFile(t, stalePath, "name: old\n")

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(applied.StaleFiles) != 1 {
		t.Fatalf("stale files = %#v, want one", applied.StaleFiles)
	}

	if got := readFile(t, stalePath); got != "name: old\n" {
		t.Fatalf("stale file changed to %q", got)
	}
}

func TestApplyActiveRunsDoNotBlockAndAreUntouched(t *testing.T) {
	root := legacyScaffold(t)
	runFile := filepath.Join(root, ".orc", "runs", "run-1", "snapshot.yaml")
	writeFile(t, runFile, "setup_version: 999\n")

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got := readFile(t, runFile); got != "setup_version: 999\n" {
		t.Fatalf("run file changed to %q", got)
	}

	if !slices.ContainsFunc(applied.FollowUps, func(follow FollowUp) bool {
		return follow.Code == "active-runs"
	}) {
		t.Fatalf("follow ups = %#v, want active-runs guidance", applied.FollowUps)
	}

	for _, path := range append(applied.CreatedPaths, applied.ModifiedPaths...) {
		if isRunsPath(path) {
			t.Fatalf("apply reported runs write %s", path)
		}
	}
}

func TestApplyWarnsAndProceedsWithoutVCS(t *testing.T) {
	root := legacyScaffold(t)
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
printf 'No jj repo found\n' >&2
exit 1
`,
		"git": `#!/bin/sh
printf 'fatal: not a git repository\n' >&2
exit 1
`,
	})

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{Env: []string{"PATH=" + path}})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.ContainsFunc(applied.Warnings, func(warning Warning) bool {
		return warning.Code == "no-vcs-dirty-check"
	}) {
		t.Fatalf("warnings = %#v, want no-vcs-dirty-check", applied.Warnings)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplyRefusesDirtyAffectedPath(t *testing.T) {
	root := legacyScaffold(t)
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
case "$1" in
  root) printf '%s\n' "$PWD";;
  status) printf 'Working copy changes:\nM .orc/config.yaml\nM README.md\n';;
  *) exit 2;;
esac
`,
	})

	result := mustPlan(t, root)

	_, err := Apply(context.Background(), result, ApplyOptions{Env: []string{"PATH=" + path}})
	if err == nil {
		t.Fatal("Apply returned nil error, want dirty affected path refusal")
	}

	if !strings.Contains(err.Error(), ".orc/config.yaml dirty-affected-path") {
		t.Fatalf("Apply error = %v, want dirty affected path conflict", err)
	}

	if strings.Contains(readFile(t, filepath.Join(root, ".orc", "config.yaml")), "setup_version") {
		t.Fatalf("config was modified despite dirty affected path")
	}
}

func TestApplyRefusesDirtyAffectedPathInNestedVCSRoot(t *testing.T) {
	root := legacyScaffold(t)
	path := fakeVCSPath(t, map[string]string{
		"jj": `#!/bin/sh
case "$1" in
  root) cd .. && pwd -P;;
  status) printf 'Working copy changes:\nM %s/.orc/config.yaml\nM README.md\n' "${PWD##*/}";;
  *) exit 2;;
esac
`,
	})

	result := mustPlan(t, root)

	_, err := Apply(context.Background(), result, ApplyOptions{Env: []string{"PATH=" + path}})
	if err == nil {
		t.Fatal("Apply returned nil error, want dirty affected path refusal")
	}

	if !strings.Contains(err.Error(), ".orc/config.yaml dirty-affected-path") {
		t.Fatalf("Apply error = %v, want project-relative dirty affected path conflict", err)
	}

	if strings.Contains(readFile(t, filepath.Join(root, ".orc", "config.yaml")), "setup_version") {
		t.Fatalf("config was modified despite dirty affected path")
	}
}

func assertCurrentSetupConfig(t *testing.T, root string) {
	t.Helper()

	project, err := config.Load(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if project.Config.SetupVersion != config.CurrentSetupVersion {
		t.Fatalf("setup_version = %d, want %d", project.Config.SetupVersion, config.CurrentSetupVersion)
	}

	if project.Config.Version != 1 {
		t.Fatalf("config schema version = %d, want 1", project.Config.Version)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(content)
}

func fakeVCSPath(t *testing.T, scripts map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	return dir
}
