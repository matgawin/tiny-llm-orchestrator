package initupgrade

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

const (
	testPlannerPath  = ".orc/agents/planner.md"
	testRuntimePath  = ".orc/runtimes/codex.yaml"
	testWorkflowPath = ".orc/workflows/implementation.yaml"
)

func TestPlanAlreadyCurrentSetupHasNoUpgradeActions(t *testing.T) {
	root := currentScaffold(t)

	result := mustPlan(t, root)

	if result.CurrentSetupVersion != 1 {
		t.Fatalf("current setup version = %d, want 1", result.CurrentSetupVersion)
	}

	if result.TargetSetupVersion != 1 {
		t.Fatalf("target setup version = %d, want 1", result.TargetSetupVersion)
	}

	if result.ConfigSchemaVersion != 1 {
		t.Fatalf("config schema version = %d, want 1", result.ConfigSchemaVersion)
	}

	if len(result.Actions) != 0 {
		t.Fatalf("actions = %#v, want none", result.Actions)
	}

	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", result.Conflicts)
	}
}

func TestPlanOlderSetupVersionWarns(t *testing.T) {
	root := currentScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "setup_version: 1\n", "setup_version: 0\n")

	result := mustPlan(t, root)

	assertWarning(t, result, "older-setup")

	if result.CurrentSetupVersion != 0 {
		t.Fatalf("current setup version = %d, want 0", result.CurrentSetupVersion)
	}

	action := assertAction(t, result, ActionModify, ".orc/config.yaml")
	assertEdit(t, action, EditSetYAMLField, "setup_version")
}

func TestPlanMissingSetupVersionAddsSurgicalConfigEdit(t *testing.T) {
	root := legacyScaffold(t)

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionModify, ".orc/config.yaml")
	if len(action.Content) != 0 {
		t.Fatalf("config modify content length = %d, want surgical edit without whole-file content", len(action.Content))
	}

	assertEdit(t, action, EditAddYAMLField, "setup_version")

	if action.FileIdentity == nil || action.FileIdentity.SHA256 == "" {
		t.Fatalf("config action identity = %#v, want content metadata", action.FileIdentity)
	}
}

func TestPlanMissingNewScaffoldFileCreatesWhenAbsent(t *testing.T) {
	root := legacyScaffold(t)

	runtimePath := filepath.Join(root, ".orc", "runtimes", "codex.yaml")
	if err := os.Remove(runtimePath); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionCreate, testRuntimePath)
	if !strings.Contains(string(action.Content), "id: codex\n") {
		t.Fatalf("runtime create content missing scaffold runtime:\n%s", string(action.Content))
	}
}

func TestPlanCustomizedExistingScaffoldFileIsSkippedForManualRefresh(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Custom.\n---\n\nCustom content.\n")

	result := mustPlan(t, root)

	assertSkippedAction(t, result, testPlannerPath, "customized-scaffold-file")

	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none for customized scaffold", result.Conflicts)
	}
}

func TestPlanMissingManifestCreatesOwnershipManifest(t *testing.T) {
	root := currentScaffold(t)
	removeFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())))

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionCreate, initconfig.ScaffoldManifestPath())
	content := string(action.Content)

	for _, want := range []string{
		"version: 1\n",
		"setup_version: 1\n",
		"  - path: " + testPlannerPath + "\n",
		"  - path: " + testRuntimePath + "\n",
		"  - path: .orc/workflows/implementation.yaml\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("manifest create missing %q:\n%s", want, content)
		}
	}

	for _, excluded := range []string{"path: .orc/config.yaml", "path: .orc/runs", "path: .gitignore", "path: AGENTS.md"} {
		if strings.Contains(content, excluded) {
			t.Fatalf("manifest create contains excluded entry %q:\n%s", excluded, content)
		}
	}
}

func TestPlanInvalidManifestFallsBackWithoutOverwritingManifest(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())), "version: nope\n")
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	result := mustPlan(t, root)

	assertConflict(t, result, initconfig.ScaffoldManifestPath(), "invalid-scaffold-manifest")
	assertSkippedAction(t, result, testPlannerPath, "customized-scaffold-file")

	for _, action := range result.Actions {
		if action.Path == initconfig.ScaffoldManifestPath() {
			t.Fatalf("manifest action = %#v, want none for invalid manifest", action)
		}
	}
}

func TestPlanManifestHashMatchEnablesManagedRefresh(t *testing.T) {
	root := currentScaffold(t)
	plannerPath := filepath.Join(root, ".orc", "agents", "planner.md")
	oldContent := []byte("old managed planner\n")
	writeFile(t, plannerPath, string(oldContent))
	writeManifest(t, root, []initconfig.ScaffoldManifestFile{{
		Path:   testPlannerPath,
		SHA256: initconfig.SHA256Hex(oldContent),
	}})

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionModify, testPlannerPath)
	assertEdit(t, action, EditReplaceIfBaseline, "")

	if !slices.Contains(action.DependsOn, initconfig.ScaffoldManifestPath()) {
		t.Fatalf("planner action dependencies = %#v, want manifest dependency", action.DependsOn)
	}

	manifest := assertAction(t, result, ActionModify, initconfig.ScaffoldManifestPath())
	if !slices.Contains(manifest.DependsOn, testPlannerPath) {
		t.Fatalf("manifest dependencies = %#v, want planner dependency", manifest.DependsOn)
	}
}

func TestPlanManifestHashMismatchPreservesCustomizedFile(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	result := mustPlan(t, root)

	assertSkippedAction(t, result, testPlannerPath, "customized-scaffold-file")

	for _, action := range result.Actions {
		if action.Path == testPlannerPath {
			t.Fatalf("planner action = %#v, want no refresh for hash mismatch", action)
		}
	}
}

func TestPlanUnknownHistoricalFileIsSkippedForManualRefresh(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Plans implementation work.\n---\n\nPlan the work.\n")

	result := mustPlan(t, root)

	assertSkippedAction(t, result, testPlannerPath, "customized-scaffold-file")

	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none for unknown scaffold content", result.Conflicts)
	}
}

func TestPlanCommentOnlyScaffoldChangeIsSkippedForManualRefresh(t *testing.T) {
	root := legacyScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "workflows", "implementation.yaml"), "name: implementation\n", "# local note\nname: implementation\n")

	result := mustPlan(t, root)

	skipped := assertSkippedAction(t, result, ".orc/workflows/implementation.yaml", "customized-scaffold-file")
	if !strings.Contains(skipped.Guidance, "local customization was preserved") {
		t.Fatalf("guidance = %q, want preservation guidance", skipped.Guidance)
	}
}

func TestPlanDeprecatedFieldWithSafeMigrationUsesSurgicalEdits(t *testing.T) {
	root := legacyScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  max_loops: 3\n")

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionModify, ".orc/config.yaml")
	assertEdit(t, action, EditRemoveYAMLField, "defaults.max_loops")
	assertEdit(t, action, EditAddYAMLField, "defaults.loop_caps")

	if len(action.Content) != 0 {
		t.Fatalf("config modify content length = %d, want surgical edits only", len(action.Content))
	}
}

func TestPlanDeprecatedFieldRequiringConflict(t *testing.T) {
	root := legacyScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n", "defaults:\n  legacy_runtime: codex\n  loop_caps:\n")

	result := mustPlan(t, root)

	assertConflict(t, result, ".orc/config.yaml", "deprecated-field")
}

func TestPlanDoesNotReportUnownedWorkflowAsStale(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "workflows", "user-review.yaml"), "name: user-review\n")

	result := mustPlan(t, root)

	if len(result.StaleFiles) != 0 {
		t.Fatalf("stale files = %#v, want none for unowned workflow", result.StaleFiles)
	}

	for _, action := range result.Actions {
		if action.Path == ".orc/workflows/user-review.yaml" {
			t.Fatalf("unowned workflow has action %#v, want no planner action", action)
		}
	}
}

func TestPlanActiveRunsPresenceDoesNotBlockOrPlanRunsChanges(t *testing.T) {
	root := legacyScaffold(t)

	runDir := filepath.Join(root, ".orc", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	writeFile(t, filepath.Join(runDir, "snapshot.yaml"), "setup_version: 999\n")

	result := mustPlan(t, root)

	if len(result.FollowUps) == 0 {
		t.Fatalf("follow ups = %#v, want active-runs guidance", result.FollowUps)
	}

	for _, action := range result.Actions {
		if strings.HasPrefix(action.Path, ".orc/runs/") || action.Path == ".orc/runs" {
			t.Fatalf("planned runs action %#v", action)
		}
	}

	for _, affected := range result.AffectedPaths {
		if strings.HasPrefix(affected.Path, ".orc/runs/") || affected.Path == ".orc/runs" {
			t.Fatalf("affected runs path %#v", affected)
		}
	}
}

func TestPlanGitignoreBroadOrcIgnoreIsConflict(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".gitignore"), ".orc/\n")

	result := mustPlan(t, root)

	assertConflict(t, result, ".gitignore", "broad-orc-ignore")
}

func currentScaffold(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := initconfig.Run(initconfig.Options{Root: root, Yes: true}); err != nil {
		t.Fatalf("init scaffold: %v", err)
	}

	return root
}

func legacyScaffold(t *testing.T) string {
	t.Helper()

	root := currentScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "setup_version: 1\n", "")
	removeFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())))

	return root
}

func mustPlan(t *testing.T, root string) *Result {
	t.Helper()

	result, err := Plan(root)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}

	return result
}

func assertAction(t *testing.T, result *Result, kind ActionKind, path string) Action {
	t.Helper()

	for _, action := range result.Actions {
		if action.Kind == kind && action.Path == path {
			return action
		}
	}

	t.Fatalf("missing %s action for %s; actions = %#v", kind, path, result.Actions)

	return Action{}
}

func assertEdit(t *testing.T, action Action, kind EditKind, path string) {
	t.Helper()

	if slices.ContainsFunc(action.Edits, func(edit SurgicalEdit) bool {
		return edit.Kind == kind && (path == "" || edit.Path == path)
	}) {
		return
	}

	t.Fatalf("missing %s edit %q in action %#v", kind, path, action)
}

func assertWarning(t *testing.T, result *Result, code string) {
	t.Helper()

	if slices.ContainsFunc(result.Warnings, func(warning Warning) bool {
		return warning.Code == code
	}) {
		return
	}

	t.Fatalf("missing warning %q; warnings = %#v", code, result.Warnings)
}

func assertConflict(t *testing.T, result *Result, path, code string) {
	t.Helper()

	if slices.ContainsFunc(result.Conflicts, func(conflict Conflict) bool {
		return conflict.Path == path && conflict.Code == code
	}) {
		return
	}

	t.Fatalf("missing conflict %s %s; conflicts = %#v", path, code, result.Conflicts)
}

func assertSkippedAction(t *testing.T, result *Result, path, code string) SkippedAction {
	t.Helper()

	for _, skipped := range result.SkippedActions {
		if skipped.Path == path && skipped.Code == code {
			return skipped
		}
	}

	t.Fatalf("missing skipped action %s %s; skipped = %#v", path, code, result.SkippedActions)

	return SkippedAction{}
}

func replaceInFile(t *testing.T, path, old, next string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	updated := strings.Replace(string(content), old, next, 1)
	if updated == string(content) {
		t.Fatalf("replace %q in %s did not change content", old, path)
	}

	writeFile(t, path, updated)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
}

func renameFile(t *testing.T, oldPath, newPath string) {
	t.Helper()

	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename %s to %s: %v", oldPath, newPath, err)
	}
}

func writeManifest(t *testing.T, root string, files []initconfig.ScaffoldManifestFile) {
	t.Helper()

	writeFile(t, filepath.Join(root, filepath.FromSlash(initconfig.ScaffoldManifestPath())), string(initconfig.ScaffoldManifestContent(files)))
}
