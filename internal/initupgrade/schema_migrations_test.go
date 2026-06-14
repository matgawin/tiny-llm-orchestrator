package initupgrade

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

const (
	testLoopCapsDisabledValue = "loop_caps:\n  enabled: false"
	testLoopCapsValue         = "enabled: true\nsoft: 5\nhard: 7"
	testRuntimesCodexYAMLPath = "runtimes.codex"
)

func TestProductionConfigMaxLoopsMigrationPlansAndApplies(t *testing.T) {
	root := currentScaffold(t)
	configFilePath := filepath.Join(root, ".orc", "config.yaml")
	replaceInFile(t, configFilePath, "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  # keep default comment\n  max_loops: 3\n")

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionModify, configPath)
	if !strings.Contains(action.Reason, "schema migration "+configDefaultsMaxLoopsToLoopCapsMigrationID+": migrate defaults.max_loops to defaults.loop_caps") {
		t.Fatalf("reason = %q, want production schema migration id", action.Reason)
	}

	assertEdit(t, action, EditRemoveYAMLField, "defaults.max_loops")
	assertEdit(t, action, EditAddYAMLField, configDefaultsLoopCapsYAMLPath)

	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, configFilePath)
	if !strings.Contains(content, "defaults:\n  # keep default comment\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\n") {
		t.Fatalf("config content did not preserve defaults order/comment:\n%s", content)
	}

	if strings.Contains(content, "max_loops") {
		t.Fatalf("config still contains max_loops:\n%s", content)
	}

	second := mustPlan(t, root)
	if hasActionForPath(second, configPath) {
		t.Fatalf("second plan actions = %#v, want config migration idempotent", second.Actions)
	}
}

func TestProductionConfigMaxLoopsMigrationNoOpsForNewOrNeitherShape(t *testing.T) {
	for name, replacement := range map[string]string{
		"new-only": "defaults:\n  loop_caps:\n    enabled: true\n    soft: 5\n    hard: 6\n",
		"neither":  "defaults:\n  retry_limit: 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := currentScaffold(t)
			replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", replacement)

			result := mustPlan(t, root)
			if hasSchemaMigrationAction(result, configDefaultsMaxLoopsToLoopCapsMigrationID) {
				t.Fatalf("actions = %#v, want production migration no-op", result.Actions)
			}
		})
	}
}

func TestProductionConfigMaxLoopsMigrationConflictsForAmbiguousAndInvalidValues(t *testing.T) {
	for name, replacement := range map[string]string{
		"both-fields": "defaults:\n  max_loops: 3\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\n",
		"non-integer": "defaults:\n  max_loops: sometimes\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := currentScaffold(t)
			replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", replacement)

			result := mustPlan(t, root)

			assertConflict(t, result, configPath, schemaMigrationConflictCode)

			conflict := schemaMigrationConflictForPath(t, result, configPath)
			if !strings.Contains(conflict.Message, configDefaultsMaxLoopsToLoopCapsMigrationID) {
				t.Fatalf("conflict message = %q, want migration id", conflict.Message)
			}
		})
	}
}

func TestProductionConfigMaxLoopsMigrationConflictLeavesConfigUneditedButPlansUnrelatedSafeActions(t *testing.T) {
	root := currentScaffold(t)
	replaceInFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  max_loops: 3\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\n")
	removeFile(t, filepath.Join(root, filepath.FromSlash(testRuntimePath)))

	result := mustPlan(t, root)

	assertConflict(t, result, configPath, schemaMigrationConflictCode)

	for _, action := range result.Actions {
		if action.Path == configPath {
			t.Fatalf("config action = %#v, want no config edit while schema migration conflicts", action)
		}
	}

	assertAction(t, result, ActionCreate, testRuntimePath)
}

func TestProductionConfigMaxLoopsMigrationPreservesLegacyBlankBehavior(t *testing.T) {
	root := currentScaffold(t)
	configFilePath := filepath.Join(root, ".orc", "config.yaml")
	replaceInFile(t, configFilePath, "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  max_loops: \"\"\n")

	result := mustPlan(t, root)
	action := assertAction(t, result, ActionModify, configPath)
	assertEdit(t, action, EditAddYAMLField, configDefaultsLoopCapsYAMLPath)

	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, configFilePath)
	if !strings.Contains(content, "loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n") {
		t.Fatalf("config content = %s, want legacy blank max_loops default conversion", content)
	}
}

func TestProductionConfigMaxLoopsMigrationPlansBeforeTypedConfigValidation(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: 1\nsetup_version: 1\nworkflows: invalid\ndefaults:\n  max_loops: 3\n")

	result := mustPlan(t, root)

	action := assertAction(t, result, ActionModify, configPath)
	if !strings.Contains(action.Reason, configDefaultsMaxLoopsToLoopCapsMigrationID) {
		t.Fatalf("reason = %q, want production schema migration", action.Reason)
	}

	if len(result.Actions) != 1 {
		t.Fatalf("actions = %#v, want only schema action while typed config is invalid", result.Actions)
	}
}

func TestProductionConfigMaxLoopsMigrationExcludesRunsTree(t *testing.T) {
	root := currentScaffold(t)
	runConfig := filepath.Join(root, ".orc", "runs", "run-1", "config.yaml")
	writeFile(t, runConfig, "defaults:\n  max_loops: 3\n")

	result := mustPlan(t, root)
	if hasActionForPath(result, ".orc/runs/run-1/config.yaml") {
		t.Fatalf("actions = %#v, want runs config ignored", result.Actions)
	}

	if got := readFile(t, runConfig); got != "defaults:\n  max_loops: 3\n" {
		t.Fatalf("run config changed to %q", got)
	}
}

func TestSchemaMigrationPlansOrphanEligibleWorkflow(t *testing.T) {
	root := currentScaffold(t)
	path := ".orc/workflows/orphan.yaml"
	writeFile(t, filepath.Join(root, filepath.FromSlash(path)), "name: orphan\nlegacy_field: keep\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-workflow", "workflow legacy field", func(candidate string) bool {
		return strings.HasPrefix(candidate, ".orc/workflows/") && strings.HasSuffix(candidate, ".yaml")
	}, "legacy_field", "modern_field"))

	action := assertAction(t, result, ActionModify, path)
	if !strings.Contains(action.Reason, "schema migration test-workflow: workflow legacy field") {
		t.Fatalf("reason = %q, want schema migration id and summary", action.Reason)
	}

	assertEdit(t, action, EditRemoveYAMLField, "legacy_field")
	assertEdit(t, action, EditAddYAMLField, "modern_field")
}

func TestSchemaMigrationPlansAndAppliesEligibleSurfaces(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: 1\nsetup_version: 1\nconfig_old: true\n")
	writeFile(t, filepath.Join(root, ".orc", "workflows", "implementation.yaml"), "name: implementation\nworkflow_old: true\n")
	writeFile(t, filepath.Join(root, ".orc", "runtimes", "codex.yaml"), "id: codex\nruntime_old: true\n")
	writeFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "---\nid: planner\nagent_old: true\n---\n\nKeep this body exactly.\n")

	result := mustPlanWithSchemaMigrations(
		t, root,
		testRenameMigration("test-config", "config field", exactTarget(".orc/config.yaml"), "config_old", "config_new"),
		testRenameMigration("test-workflow", "workflow field", exactTarget(".orc/workflows/implementation.yaml"), "workflow_old", "workflow_new"),
		testRenameMigration("test-runtime", "runtime field", exactTarget(testRuntimePath), "runtime_old", "runtime_new"),
		testRenameMigration("test-agent", "agent frontmatter field", exactTarget(".orc/agents/planner.md"), "agent_old", "agent_new"),
	)

	for _, path := range []string{".orc/config.yaml", ".orc/workflows/implementation.yaml", testRuntimePath, ".orc/agents/planner.md"} {
		if !hasActionForPath(result, path) {
			t.Fatalf("missing action for %s; actions = %#v conflicts = %#v skipped = %#v", path, result.Actions, result.Conflicts, result.SkippedActions)
		}
	}

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	for _, path := range []string{".orc/config.yaml", ".orc/workflows/implementation.yaml", testRuntimePath, ".orc/agents/planner.md"} {
		if !slices.Contains(applied.ModifiedPaths, path) {
			t.Fatalf("modified paths = %#v, want %s", applied.ModifiedPaths, path)
		}
	}

	agent := readFile(t, filepath.Join(root, ".orc", "agents", "planner.md"))
	if !strings.Contains(agent, "agent_new: true\n---\n\nKeep this body exactly.\n") || strings.Contains(agent, "agent_old") {
		t.Fatalf("agent frontmatter/body not migrated surgically:\n%s", agent)
	}
}

func TestSchemaMigrationConflictsOnOverlappingYAMLEditPaths(t *testing.T) {
	tests := map[string]struct {
		first  SurgicalEdit
		second SurgicalEdit
	}{
		"equal path": {
			first:  SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: "enabled: false"},
			second: SurgicalEdit{Kind: EditRemoveYAMLField, Path: configDefaultsLoopCapsYAMLPath},
		},
		"parent then child": {
			first:  SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsYAMLPath, Value: testLoopCapsDisabledValue},
			second: SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
		},
		"child then parent": {
			first:  SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
			second: SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsYAMLPath, Value: testLoopCapsDisabledValue},
		},
		"map entry child": {
			first:  SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
			second: SurgicalEdit{Kind: EditAddYAMLMapEntry, Path: configDefaultsYAMLPath, Key: "loop_caps", Value: "enabled: false"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			root := currentScaffold(t)

			result := mustPlanWithSchemaMigrations(
				t,
				root,
				testEditMigration("test-first", "first edit", configPath, []SurgicalEdit{tt.first}),
				testEditMigration("test-second", "second edit", configPath, []SurgicalEdit{tt.second}),
			)

			assertConflict(t, result, configPath, "duplicate-upgrade-action")
			assertNoActionForPath(t, result, configPath)
		})
	}
}

func TestSchemaMigrationConflictBlocksLaterSamePathActions(t *testing.T) {
	root := currentScaffold(t)

	result := mustPlanWithSchemaMigrations(
		t,
		root,
		testEditMigration("test-parent", "parent edit", configPath, []SurgicalEdit{
			{Kind: EditSetYAMLField, Path: configDefaultsYAMLPath, Value: testLoopCapsDisabledValue},
		}),
		testEditMigration("test-child", "child edit", configPath, []SurgicalEdit{
			{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
		}),
		testEditMigration("test-later", "later compatible edit", configPath, []SurgicalEdit{
			{Kind: EditSetYAMLField, Path: testRuntimesCodexYAMLPath, Value: "enabled: true"},
		}),
	)

	assertConflict(t, result, configPath, "duplicate-upgrade-action")
	assertNoActionForPath(t, result, configPath)
}

func TestSchemaMigrationComposesNonOverlappingYAMLEditPaths(t *testing.T) {
	tests := map[string]struct {
		first      SurgicalEdit
		second     SurgicalEdit
		firstPath  string
		secondPath string
	}{
		"raw prefix siblings": {
			first:      SurgicalEdit{Kind: EditSetYAMLField, Path: "defaults.loop", Value: "5"},
			second:     SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
			firstPath:  "defaults.loop",
			secondPath: configDefaultsLoopCapsYAMLPath,
		},
		"different branches": {
			first:      SurgicalEdit{Kind: EditSetYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: testLoopCapsValue},
			second:     SurgicalEdit{Kind: EditSetYAMLField, Path: testRuntimesCodexYAMLPath, Value: "enabled: true"},
			firstPath:  configDefaultsLoopCapsYAMLPath,
			secondPath: testRuntimesCodexYAMLPath,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			root := currentScaffold(t)

			result := mustPlanWithSchemaMigrations(
				t,
				root,
				testEditMigration("test-first", "first edit", configPath, []SurgicalEdit{tt.first}),
				testEditMigration("test-second", "second edit", configPath, []SurgicalEdit{tt.second}),
			)

			action := assertAction(t, result, ActionModify, configPath)
			assertEdit(t, action, tt.first.Kind, tt.firstPath)
			assertEdit(t, action, tt.second.Kind, tt.secondPath)

			if len(action.Edits) != 2 {
				t.Fatalf("edits = %#v, want exactly two composed edits", action.Edits)
			}
		})
	}
}

func TestSchemaMigrationExcludesRunsTreeFromPlanningReadsAndWrites(t *testing.T) {
	root := currentScaffold(t)
	runFile := filepath.Join(root, ".orc", "runs", "run-1", "workflow.yaml")
	writeFile(t, runFile, "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-runs", "runs excluded", func(path string) bool {
		return strings.Contains(path, "workflow.yaml")
	}, "legacy_field", "modern_field"))

	for _, action := range result.Actions {
		if isRunsPath(action.Path) {
			t.Fatalf("planned runs action %#v", action)
		}
	}

	if got := readFile(t, runFile); got != "legacy_field: true\n" {
		t.Fatalf("run file changed to %q", got)
	}
}

func TestSchemaMigrationReportsExplicitSymlinkAndNonRegularTargets(t *testing.T) {
	root := currentScaffold(t)
	target := filepath.Join(root, ".orc", "workflows", "real.yaml")
	writeFile(t, target, "legacy_field: true\n")

	if err := os.Symlink(target, filepath.Join(root, ".orc", "workflows", "link.yaml")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := os.Mkdir(filepath.Join(root, ".orc", "workflows", "dir.yaml"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-explicit", "explicit target", func(path string) bool {
		return path == ".orc/workflows/link.yaml" || path == ".orc/workflows/dir.yaml"
	}, "legacy_field", "modern_field"))

	assertConflict(t, result, ".orc/workflows/link.yaml", schemaMigrationConflictCode)
	assertConflict(t, result, ".orc/workflows/dir.yaml", schemaMigrationConflictCode)
}

func TestSchemaMigrationConfigSymlinkDoesNotBlockValidFile(t *testing.T) {
	root := currentScaffold(t)
	configFile := filepath.Join(root, ".orc", "config.yaml")
	configTarget := filepath.Join(root, ".orc", "config-target.yaml")
	renameFile(t, configFile, configTarget)

	if err := os.Symlink(configTarget, configFile); err != nil {
		t.Fatalf("symlink config: %v", err)
	}

	writeFile(t, filepath.Join(root, ".orc", "workflows", "good.yaml"), "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(
		t, root,
		testRenameMigration("test-config-symlink", "config explicit target", exactTarget(".orc/config.yaml"), "legacy_field", "modern_field"),
		testRenameMigration("test-valid-workflow", "valid workflow", exactTarget(".orc/workflows/good.yaml"), "legacy_field", "modern_field"),
	)

	assertConflict(t, result, configPath, schemaMigrationConflictCode)
	assertConflict(t, result, configPath, "invalid-project-config")
	assertAction(t, result, ActionModify, ".orc/workflows/good.yaml")
}

func TestSchemaMigrationConfigDirectoryDoesNotBlockValidFile(t *testing.T) {
	root := currentScaffold(t)
	configFile := filepath.Join(root, ".orc", "config.yaml")
	removeFile(t, configFile)

	if err := os.Mkdir(configFile, 0o750); err != nil {
		t.Fatalf("mkdir config path: %v", err)
	}

	writeFile(t, filepath.Join(root, ".orc", "workflows", "good.yaml"), "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(
		t, root,
		testRenameMigration("test-config-dir", "config explicit target", exactTarget(".orc/config.yaml"), "legacy_field", "modern_field"),
		testRenameMigration("test-valid-workflow", "valid workflow", exactTarget(".orc/workflows/good.yaml"), "legacy_field", "modern_field"),
	)

	assertConflict(t, result, configPath, schemaMigrationConflictCode)
	assertConflict(t, result, configPath, "invalid-project-config")
	assertAction(t, result, ActionModify, ".orc/workflows/good.yaml")
}

func TestSchemaMigrationIgnoresUnknownUntargetedFiles(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "notes.txt"), "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-workflow", "workflow only", func(path string) bool {
		return strings.HasPrefix(path, ".orc/workflows/")
	}, "legacy_field", "modern_field"))

	for _, conflict := range result.Conflicts {
		if conflict.Path == ".orc/notes.txt" {
			t.Fatalf("conflicts = %#v, want unknown file ignored", result.Conflicts)
		}
	}
}

func TestSchemaMigrationIdempotenceContract(t *testing.T) {
	root := currentScaffold(t)

	fixtures := map[string]string{
		"old":     "legacy_field: true\n",
		"new":     "modern_field: true\n",
		"both":    "legacy_field: true\nmodern_field: false\n",
		"neither": "name: neither\n",
	}
	for name, content := range fixtures {
		writeFile(t, filepath.Join(root, ".orc", "workflows", name+".yaml"), content)
	}

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-idempotence", "rename field", func(path string) bool {
		return strings.HasPrefix(path, ".orc/workflows/") && strings.HasSuffix(path, ".yaml")
	}, "legacy_field", "modern_field"))

	assertAction(t, result, ActionModify, ".orc/workflows/old.yaml")
	assertConflict(t, result, ".orc/workflows/both.yaml", schemaMigrationConflictCode)

	for _, path := range []string{".orc/workflows/new.yaml", ".orc/workflows/neither.yaml"} {
		for _, action := range result.Actions {
			if action.Path == path {
				t.Fatalf("action = %#v, want no-op for %s", action, path)
			}
		}
	}
}

func TestSchemaMigrationPlansBeforeTypedConfigValidation(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: 1\nsetup_version: 1\nworkflows: invalid\nlegacy_field: true\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-config-raw", "raw config field", exactTarget(configPath), "legacy_field", "modern_field"))

	assertAction(t, result, ActionModify, configPath)

	if len(result.Actions) != 1 {
		t.Fatalf("actions = %#v, want only schema action while typed config is invalid", result.Actions)
	}
}

func TestSchemaMigrationInvalidRawConfigDoesNotBlockValidFile(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")
	writeFile(t, filepath.Join(root, ".orc", "workflows", "good.yaml"), "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-invalid-config", "invalid config path scoped", func(path string) bool {
		return strings.HasPrefix(path, ".orc/workflows/") && strings.HasSuffix(path, ".yaml")
	}, "legacy_field", "modern_field"))

	assertConflict(t, result, configPath, "invalid-project-config")
	assertAction(t, result, ActionModify, ".orc/workflows/good.yaml")
}

func TestSchemaMigrationInvalidTargetedFileDoesNotBlockValidFile(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, filepath.Join(root, ".orc", "workflows", "bad.yaml"), "legacy_field: [\n")
	writeFile(t, filepath.Join(root, ".orc", "workflows", "good.yaml"), "legacy_field: true\n")

	result := mustPlanWithSchemaMigrations(t, root, testRenameMigration("test-invalid", "invalid path scoped", func(path string) bool {
		return strings.HasPrefix(path, ".orc/workflows/") && strings.HasSuffix(path, ".yaml")
	}, "legacy_field", "modern_field"))

	assertSkippedAction(t, result, ".orc/workflows/bad.yaml", "schema-migration-skipped")
	assertAction(t, result, ActionModify, ".orc/workflows/good.yaml")

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.Contains(applied.ModifiedPaths, ".orc/workflows/good.yaml") {
		t.Fatalf("modified paths = %#v, want good workflow", applied.ModifiedPaths)
	}
}

func mustPlanWithSchemaMigrations(t *testing.T, root string, migrations ...schemaMigration) *Result {
	t.Helper()

	result, err := planWithOptions(root, planOptions{schemaMigrations: migrations})
	if err != nil {
		t.Fatalf("planWithOptions returned error: %v", err)
	}

	return result
}

func hasActionForPath(result *Result, path string) bool {
	return slices.ContainsFunc(result.Actions, func(action Action) bool {
		return action.Path == path
	})
}

func assertNoActionForPath(t *testing.T, result *Result, path string) {
	t.Helper()

	if hasActionForPath(result, path) {
		t.Fatalf("actions = %#v, want no action for %s", result.Actions, path)
	}
}

func hasSchemaMigrationAction(result *Result, migrationID string) bool {
	return slices.ContainsFunc(result.Actions, func(action Action) bool {
		return strings.Contains(action.Reason, "schema migration "+migrationID+":")
	})
}

func schemaMigrationConflictForPath(t *testing.T, result *Result, path string) Conflict {
	t.Helper()

	for _, conflict := range result.Conflicts {
		if conflict.Path == path && conflict.Code == schemaMigrationConflictCode {
			return conflict
		}
	}

	t.Fatalf("missing schema migration conflict for %s; conflicts = %#v", path, result.Conflicts)

	return Conflict{}
}

func exactTarget(path string) func(string) bool {
	return func(candidate string) bool { return candidate == path }
}

func testEditMigration(id, summary, path string, edits []SurgicalEdit) schemaMigration {
	return schemaMigration{
		ID:      id,
		Summary: summary,
		Target:  exactTarget(path),
		Plan: func(schemaMigrationFile) schemaMigrationDecision {
			return schemaMigrationDecision{Edits: edits}
		},
	}
}

func testRenameMigration(id, summary string, target func(string) bool, oldField, newField string) schemaMigration {
	return schemaMigration{
		ID:      id,
		Summary: summary,
		Target:  target,
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			doc := file.Doc
			if file.Markdown {
				if !file.HasFrontmatter {
					return schemaMigrationDecision{}
				}

				if file.InvalidMarkdown != nil || file.InvalidYAML != nil {
					return schemaMigrationDecision{Skipped: "targeted Markdown frontmatter is invalid"}
				}

				doc = file.Frontmatter
			} else if file.InvalidYAML != nil {
				return schemaMigrationDecision{Skipped: "targeted YAML is invalid"}
			}

			hasOld := hasYAMLField(doc, oldField)
			hasNew := hasYAMLField(doc, newField)

			switch {
			case hasOld && !hasNew:
				value := fmtScalar(mapValue(doc, oldField))
				if value == "" {
					value = "true"
				}

				return schemaMigrationDecision{Edits: []SurgicalEdit{
					{Kind: EditRemoveYAMLField, Path: oldField},
					{Kind: EditAddYAMLField, Path: newField, Value: value},
				}}
			case hasOld && hasNew:
				return schemaMigrationDecision{Conflict: "old and new fields both exist", Guidance: "remove one of the fields before applying this schema migration"}
			default:
				return schemaMigrationDecision{}
			}
		},
	}
}

func hasYAMLField(doc yaml.MapSlice, field string) bool {
	_, ok := mapLookup(doc, field)
	return ok
}

func mapValue(doc yaml.MapSlice, field string) any {
	value, _ := mapLookup(doc, field)
	return value
}

func fmtScalar(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "true"
		}

		return "false"
	case string:
		return typed
	default:
		return ""
	}
}
