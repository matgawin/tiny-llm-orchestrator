package initupgrade

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/initconfig"
)

func TestApplyWritesIndependentActionsDespiteCustomizedScaffoldSkip(t *testing.T) {
	root := legacyScaffold(t)
	configPath := filepath.Join(root, ".orc", "config.yaml")

	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	writeFile(t, plannerPath, "custom planner\n")
	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if applied == nil || !slices.Contains(applied.ModifiedPaths, ".orc/config.yaml") {
		t.Fatalf("applied = %#v, want config modified despite unrelated scaffold skip", applied)
	}

	if !slices.ContainsFunc(applied.SkippedActions, func(skipped SkippedAction) bool {
		return skipped.Path == testPlannerPath && skipped.Code == "customized-scaffold-file"
	}) {
		t.Fatalf("skipped actions = %#v, want customized planner", applied.SkippedActions)
	}

	if got := readFile(t, plannerPath); got != "custom planner\n" {
		t.Fatalf("planner changed to %q, want preserved customization", got)
	}

	if !strings.Contains(readFile(t, configPath), "setup_version: 1\n") {
		t.Fatalf("config did not gain setup_version")
	}
}

func TestApplyRejectsChangedDuringApplyButWritesIndependentActions(t *testing.T) {
	root := legacyScaffold(t)

	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	result := mustPlan(t, root)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: 1\n", "version: 1\n# concurrent edit\n")

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want changed-during-apply rejection")
	}

	if !strings.Contains(err.Error(), "changed during init upgrade apply") {
		t.Fatalf("Apply error = %v, want changed-during-apply message", err)
	}

	if applied == nil || !slices.Contains(applied.CreatedPaths, testRuntimePath) {
		t.Fatalf("applied = %#v, want independent runtime create", applied)
	}

	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime stat error = %v, want created", err)
	}
}

func TestApplyReportsEditFailureAsConflictAndWritesIndependentActions(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		edit    SurgicalEdit
	}{
		{
			name:    "invalid YAML",
			path:    ".orc/workflows/bad.yaml",
			content: "legacy: [\n",
			edit:    SurgicalEdit{Kind: EditAddYAMLField, Path: mustYAMLPath("modern"), Value: yamlScalarTrue},
		},
		{
			name:    "invalid Markdown frontmatter",
			path:    ".orc/agents/bad.md",
			content: "---\nlegacy: [\n---\n\nBody stays local.\n",
			edit:    SurgicalEdit{Kind: EditAddYAMLField, Path: mustYAMLPath("modern"), Value: yamlScalarTrue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := legacyScaffold(t)
			target := filepath.Join(root, filepath.FromSlash(tt.path))
			writeFile(t, target, tt.content)

			createPath := ".orc/workflows/independent-" + strings.ReplaceAll(tt.name, " ", "-") + ".yaml"
			targetIdentity := identity([]byte(tt.content))
			result := &Result{
				ProjectRoot:         root,
				ConfigSchemaVersion: 1,
				CurrentSetupVersion: 0,
				TargetSetupVersion:  1,
				Actions: []Action{
					{
						Kind:         ActionModify,
						Path:         tt.path,
						Reason:       "test invalid YAML edit failure",
						FileIdentity: &targetIdentity,
						Edits:        []SurgicalEdit{tt.edit},
					},
					{
						Kind:    ActionCreate,
						Path:    createPath,
						Reason:  "test independent create",
						Content: []byte("name: independent\n"),
					},
				},
			}

			applied, err := Apply(context.Background(), result, ApplyOptions{})
			if err == nil {
				t.Fatal("Apply returned nil error, want path-scoped edit conflict")
			}

			if applied == nil {
				t.Fatal("Apply result is nil, want partial result")
			}

			if !slices.Contains(applied.CreatedPaths, createPath) {
				t.Fatalf("created paths = %#v, want independent create", applied.CreatedPaths)
			}

			if !slices.ContainsFunc(applied.Conflicts, func(conflict Conflict) bool {
				return conflict.Path == tt.path && conflict.Code == "edit-failed" && strings.Contains(conflict.Message, "edit "+tt.path+":")
			}) {
				t.Fatalf("conflicts = %#v, want edit-failed for %s", applied.Conflicts, tt.path)
			}
		})
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

	if !slices.Contains(applied.CreatedPaths, testRuntimePath) {
		t.Fatalf("created paths = %#v, want runtime", applied.CreatedPaths)
	}

	content := readFile(t, runtimePath)
	if !strings.Contains(content, "id: codex\n") {
		t.Fatalf("runtime content missing scaffold runtime:\n%s", content)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplyCreatesManifestForExistingProject(t *testing.T) {
	root := legacyScaffold(t)

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.Contains(applied.CreatedPaths, initconfig.ScaffoldManifestPath()) {
		t.Fatalf("created paths = %#v, want manifest", applied.CreatedPaths)
	}

	content := readFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())))
	if !strings.Contains(content, "  - path: "+testPlannerPath+"\n") || strings.Contains(content, "path: .orc/config.yaml") {
		t.Fatalf("manifest content did not record managed scaffold scope:\n%s", content)
	}
}

func TestOrderPreparedWritesPlacesManifestAfterSameApplyScaffoldDependency(t *testing.T) {
	writes := []preparedWrite{
		{relPath: initconfig.ScaffoldManifestPath()},
		{relPath: testWorkflowPath},
	}
	actions := []Action{
		{Path: initconfig.ScaffoldManifestPath(), DependsOn: []string{testWorkflowPath}},
		{Path: testWorkflowPath},
	}

	ordered := orderPreparedWritesForApply(writes, actions)

	if got := orderedPaths(ordered); !slices.Equal(got, []string{testWorkflowPath, initconfig.ScaffoldManifestPath()}) {
		t.Fatalf("ordered paths = %#v, want scaffold dependency before manifest", got)
	}
}

func TestOrderPreparedWritesPlacesManifestBeforeReciprocalRefreshGroup(t *testing.T) {
	writes := []preparedWrite{
		{relPath: testPlannerPath},
		{relPath: initconfig.ScaffoldManifestPath()},
	}
	actions := []Action{
		{Path: testPlannerPath, DependsOn: []string{initconfig.ScaffoldManifestPath()}},
		{Path: initconfig.ScaffoldManifestPath(), DependsOn: []string{testPlannerPath}},
	}

	ordered := orderPreparedWritesForApply(writes, actions)

	if got := orderedPaths(ordered); !slices.Equal(got, []string{initconfig.ScaffoldManifestPath(), testPlannerPath}) {
		t.Fatalf("ordered paths = %#v, want manifest before reciprocal refresh", got)
	}
}

func TestApplyRefreshesManifestManagedFileAndManifest(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	oldContent := []byte("old managed planner\n")
	writeFile(t, plannerPath, string(oldContent))
	writeManifest(t, root, []initconfig.ScaffoldManifestFile{{
		Path:   testPlannerPath,
		SHA256: initconfig.SHA256Hex(oldContent),
	}})

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	for _, path := range []string{testPlannerPath, initconfig.ScaffoldManifestPath()} {
		if !slices.Contains(applied.ModifiedPaths, path) {
			t.Fatalf("modified paths = %#v, want %s", applied.ModifiedPaths, path)
		}
	}

	wantPlanner := string(scaffoldByPath()[testPlannerPath])
	if got := readFile(t, plannerPath); got != wantPlanner {
		t.Fatalf("planner content = %q, want current scaffold", got)
	}

	manifest := readFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())))
	if !strings.Contains(manifest, initconfig.SHA256Hex([]byte(wantPlanner))) {
		t.Fatalf("manifest did not record refreshed planner hash:\n%s", manifest)
	}
}

func TestApplyDoesNotRefreshManifestManagedFileWhenManifestWriteFails(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	oldContent := []byte("old managed planner\n")
	writeFile(t, plannerPath, string(oldContent))
	writeManifest(t, root, []initconfig.ScaffoldManifestFile{{
		Path:   testPlannerPath,
		SHA256: initconfig.SHA256Hex(oldContent),
	}})

	result := mustPlan(t, root)

	orcDir := filepath.Join(root, ".orc")
	if err := os.Chmod(orcDir, 0o500); err != nil {
		t.Fatalf("chmod .orc: %v", err)
	}

	t.Cleanup(func() {
		if err := os.Chmod(orcDir, 0o750); err != nil {
			t.Fatalf("restore .orc permissions: %v", err)
		}
	})

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want manifest write failure")
	}

	if !strings.Contains(err.Error(), initconfig.ScaffoldManifestPath()) {
		t.Fatalf("Apply error = %v, want manifest write failure", err)
	}

	if got := readFile(t, plannerPath); got != string(oldContent) {
		t.Fatalf("planner content = %q, want preserved old managed content", got)
	}

	if applied != nil && slices.Contains(applied.ModifiedPaths, testPlannerPath) {
		t.Fatalf("modified paths = %#v, want planner omitted after manifest failure", applied.ModifiedPaths)
	}
}

func TestApplyRestoresManifestWhenManifestManagedFileWriteFails(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	oldContent := []byte("old managed planner\n")
	writeFile(t, plannerPath, string(oldContent))
	writeManifest(t, root, []initconfig.ScaffoldManifestFile{{
		Path:   testPlannerPath,
		SHA256: initconfig.SHA256Hex(oldContent),
	}})

	manifestFile := filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath()))
	oldManifest := readFile(t, manifestFile)
	result := mustPlan(t, root)

	agentsDir := filepath.Join(root, ".orc", "agents")
	if err := os.Chmod(agentsDir, 0o500); err != nil {
		t.Fatalf("chmod .orc/agents: %v", err)
	}

	t.Cleanup(func() {
		if err := os.Chmod(agentsDir, 0o750); err != nil {
			t.Fatalf("restore .orc/agents permissions: %v", err)
		}
	})

	_, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want scaffold file write failure")
	}

	if got := readFile(t, plannerPath); got != string(oldContent) {
		t.Fatalf("planner content = %q, want preserved old managed content", got)
	}

	if got := readFile(t, manifestFile); got != oldManifest {
		t.Fatalf("manifest content changed to:\n%s\nwant restored:\n%s", got, oldManifest)
	}
}

func TestApplyBlocksRemainingManifestRefreshGroupAfterFileWriteFails(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	oldPlanner := []byte("old managed planner\n")
	oldWorkflow := []byte("old managed workflow\n")

	writeFile(t, plannerPath, string(oldPlanner))
	writeFile(t, workflowPath, string(oldWorkflow))
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "setup_version: 1\n", "setup_version: 0\n")
	writeManifest(t, root, []initconfig.ScaffoldManifestFile{
		{Path: testPlannerPath, SHA256: initconfig.SHA256Hex(oldPlanner)},
		{Path: testWorkflowPath, SHA256: initconfig.SHA256Hex(oldWorkflow)},
	})

	manifestFile := filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath()))
	oldManifest := readFile(t, manifestFile)
	result := mustPlan(t, root)

	agentsDir := filepath.Join(root, ".orc", "agents")
	if err := os.Chmod(agentsDir, 0o500); err != nil {
		t.Fatalf("chmod .orc/agents: %v", err)
	}

	t.Cleanup(func() {
		if err := os.Chmod(agentsDir, 0o750); err != nil {
			t.Fatalf("restore .orc/agents permissions: %v", err)
		}
	})

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want scaffold file write failure")
	}

	if got := readFile(t, plannerPath); got != string(oldPlanner) {
		t.Fatalf("planner content = %q, want preserved old managed content", got)
	}

	if got := readFile(t, manifestFile); got != oldManifest {
		t.Fatalf("manifest content changed to:\n%s\nwant restored:\n%s", got, oldManifest)
	}

	if got := readFile(t, workflowPath); got != string(oldWorkflow) {
		t.Fatalf("workflow content = %q, want preserved old managed content after group failure", got)
	}

	if !strings.Contains(readFile(t, filepath.Join(root, ".orc", "config.yaml")), "setup_version: 1\n") {
		t.Fatalf("config was not modified despite independent manifest group failure")
	}

	if !slices.ContainsFunc(applied.Conflicts, func(conflict Conflict) bool {
		return conflict.Path == testPlannerPath && conflict.Code == "write-permission-denied"
	}) {
		t.Fatalf("conflicts = %#v, want planner write-permission-denied", applied.Conflicts)
	}

	if !slices.ContainsFunc(applied.SkippedActions, func(skipped SkippedAction) bool {
		return skipped.Path == testWorkflowPath && skipped.Code == dependencySkippedCode
	}) {
		t.Fatalf("skipped actions = %#v, want workflow dependency skip after group failure", applied.SkippedActions)
	}
}

func TestApplyPreservesCustomizedFileWhenManifestHashDiffers(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	writeFile(t, plannerPath, "custom planner\n")

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if got := readFile(t, plannerPath); got != "custom planner\n" {
		t.Fatalf("planner changed to %q, want preserved customization", got)
	}

	if !slices.ContainsFunc(applied.SkippedActions, func(skipped SkippedAction) bool {
		return skipped.Path == testPlannerPath && skipped.Code == "customized-scaffold-file"
	}) {
		t.Fatalf("skipped actions = %#v, want customized planner", applied.SkippedActions)
	}
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

	if !strings.Contains(readFile(t, filepath.Join(root, ".orc", "config.yaml")), "setup_version") {
		t.Fatalf("config was not modified despite independent unsafe create parent")
	}
}

func TestApplyReplacesKnownBaselineWithCurrentScaffold(t *testing.T) {
	original := knownReplacementBaselines
	knownReplacementBaselines = map[string][][]byte{
		testPlannerPath: {[]byte("known v0 planner\n")},
	}

	t.Cleanup(func() { knownReplacementBaselines = original })

	root := legacyScaffold(t)
	target := filepath.Join(root, ".orc", "agents", "planner.md")
	writeFile(t, target, "known v0 planner\n")

	result := mustPlan(t, root)
	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	want := string(scaffoldByPath()[testPlannerPath])
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

func TestApplyIgnoresUnrelatedDirtyPath(t *testing.T) {
	root := legacyScaffold(t)
	initCleanJJProjectForApply(t, root)
	writeFile(t, filepath.Join(root, "README.md"), "dirty but unrelated\n")

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{Env: os.Environ()})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.Contains(applied.ModifiedPaths, ".orc/config.yaml") {
		t.Fatalf("modified paths = %#v, want config despite unrelated dirty path", applied.ModifiedPaths)
	}

	assertCurrentSetupConfig(t, root)
}

func TestApplySkipsScaffoldCreateWhenConfigDependencyConflicts(t *testing.T) {
	root := legacyScaffold(t)
	configPath := filepath.Join(root, ".orc", "config.yaml")
	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")

	replaceInFile(t, configPath, "runtimes:\n  codex: runtimes/codex.yaml\n", "runtimes:\n  codex: runtimes/custom.yaml\n")

	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	result := mustPlan(t, root)

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply returned nil error, want unresolved conflicts")
	}

	if applied == nil {
		t.Fatal("Apply result is nil, want partial result")
		return
	}

	if slices.Contains(applied.CreatedPaths, testRuntimePath) {
		t.Fatalf("created paths = %#v, want dependent runtime skipped", applied.CreatedPaths)
	}

	if _, err := os.Stat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime stat error = %v, want missing", err)
	}

	if !slices.ContainsFunc(applied.SkippedActions, func(skipped SkippedAction) bool {
		return skipped.Path == testRuntimePath &&
			skipped.Code == dependencySkippedCode &&
			skipped.ActionKind == ActionCreate &&
			slices.Contains(skipped.DependsOn, ".orc/config.yaml")
	}) {
		t.Fatalf("skipped actions = %#v, want runtime dependency-skipped", applied.SkippedActions)
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

func orderedPaths(writes []preparedWrite) []string {
	paths := make([]string, 0, len(writes))
	for _, write := range writes {
		paths = append(paths, write.relPath)
	}

	return paths
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

func initCleanJJProjectForApply(t *testing.T, root string) {
	t.Helper()

	if _, err := exec.LookPath("jj"); err != nil {
		t.Skipf("jj not available: %v", err)
	}

	runApplyTestCommand(t, root, "jj", "git", "init", "--colocate", ".")
	runApplyTestCommand(t, root, "jj", "describe", "-m", "test baseline")
	runApplyTestCommand(t, root, "jj", "new")
}

func runApplyTestCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(output))
	}
}
