package initupgrade

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tiny-llm-orchestrator/orc/internal/initconfig"
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

	action := assertAction(t, result, ActionCreate, ".orc/runtimes/codex.yaml")
	if !strings.Contains(string(action.Content), "id: codex\n") {
		t.Fatalf("runtime create content missing scaffold runtime:\n%s", string(action.Content))
	}
}

func TestPlanCustomizedExistingScaffoldFileConflicts(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Custom.\n---\n\nCustom content.\n")

	result := mustPlan(t, root)

	assertConflict(t, result, ".orc/agents/planner.md", "customized-scaffold-file")
}

func TestPlanUnknownHistoricalFileConflicts(t *testing.T) {
	root := legacyScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "---\nid: planner\nrole: planner\ndescription: Plans implementation work.\n---\n\nPlan the work.\n")

	result := mustPlan(t, root)

	assertConflict(t, result, ".orc/agents/planner.md", "customized-scaffold-file")
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
