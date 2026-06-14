package initupgrade

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
)

func TestMigrationSurfaceCatalogContract(t *testing.T) {
	want := map[string]struct {
		path       string
		parseMode  migrationParseMode
		explicit   bool
		shapeToken string
	}{
		"config":                     {path: configPath, parseMode: migrationParseYAML, shapeToken: "config.yaml"},
		"workflow":                   {path: ".orc/workflows/custom.yaml", parseMode: migrationParseYAML, shapeToken: "steps mapping"},
		"runtime":                    {path: testCustomRuntimePath, parseMode: migrationParseYAML, shapeToken: ".orc/runtimes"},
		"agent-frontmatter":          {path: ".orc/agents/custom.md", parseMode: migrationParseMarkdownFrontmatter, shapeToken: "frontmatter"},
		"scaffold-manifest-metadata": {path: ".orc/scaffold.lock.yaml", parseMode: migrationParseScaffoldManifest, explicit: true, shapeToken: "scaffold.lock.yaml"},
	}

	seen := map[string]bool{}

	for _, surface := range migrationSurfaces {
		contract, ok := want[surface.Name]
		if !ok {
			t.Fatalf("unexpected surface %#v", surface)
		}

		seen[surface.Name] = true
		if surface.ParseMode != contract.parseMode {
			t.Fatalf("%s parse mode = %q, want %q", surface.Name, surface.ParseMode, contract.parseMode)
		}

		if surface.Target == nil || !surface.Target(contract.path) {
			t.Fatalf("%s target did not match %s", surface.Name, contract.path)
		}

		if surface.ExplicitTargetOnly != contract.explicit {
			t.Fatalf("%s explicit target = %v, want %v", surface.Name, surface.ExplicitTargetOnly, contract.explicit)
		}

		if !strings.Contains(surface.SupportedShape, contract.shapeToken) && !strings.Contains(surface.DocsSummary, contract.shapeToken) {
			t.Fatalf("%s catalog text missing %q: %#v", surface.Name, contract.shapeToken, surface)
		}
	}

	for name := range want {
		if !seen[name] {
			t.Fatalf("missing surface %s in catalog %#v", name, migrationSurfaces)
		}
	}
}

func TestWorkflowStepVisitorPlansScaffoldAndOrphanWorkflowSteps(t *testing.T) {
	root := currentScaffold(t)
	orphanPath := ".orc/workflows/orphan.yaml"
	writeFile(t, pathInRoot(root, orphanPath), "name: orphan\nsteps:\n  beta:\n    agent: coder\n  alpha:\n    agent: reviewer\n")

	result := mustPlanWithSchemaMigrations(t, root, testWorkflowStepMigration())

	for _, path := range []string{testWorkflowPath, orphanPath} {
		action := assertAction(t, result, ActionModify, path)
		assertEditPathPrefix(t, action, EditASTAddYAMLField, "steps.")
	}

	applied, err := Apply(context.Background(), result, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !slices.Contains(applied.ModifiedPaths, orphanPath) {
		t.Fatalf("modified paths = %#v, want orphan workflow modified", applied.ModifiedPaths)
	}

	orphan := readFile(t, pathInRoot(root, orphanPath))
	if !strings.Contains(orphan, "beta:\n    agent: coder\n    visited: beta\n  alpha:\n    agent: reviewer\n    visited: alpha\n") {
		t.Fatalf("orphan workflow content did not preserve step order with edits:\n%s", orphan)
	}
}

func TestWorkflowStepVisitorVisitsMultipleStepsInYAMLOrder(t *testing.T) {
	file := parseSchemaMigrationFile(".orc/workflows/custom.yaml", []byte("steps:\n  zeta:\n    agent: coder\n  alpha:\n    agent: reviewer\n  code:\n    agent: coder\n"))

	var got []string

	decision := visitWorkflowSteps(file, func(step workflowStepVisit) ([]SurgicalEdit, error) {
		agent, ok := step.Map.Value("agent")
		got = append(got, step.ID+"@"+step.Path.String()+"="+yamlScalarString(mustNode(t, agent, ok)))

		return nil, nil
	})

	if decision.Conflict != "" || decision.Skipped != "" {
		t.Fatalf("decision = %#v, want successful visit", decision)
	}

	want := []string{
		"zeta@steps.zeta=coder",
		"alpha@steps.alpha=reviewer",
		"code@steps.code=coder",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visits = %#v, want %#v", got, want)
	}
}

func TestWorkflowStepVisitorUnsupportedShapeIsPathScoped(t *testing.T) {
	root := currentScaffold(t)
	writeFile(t, pathInRoot(root, ".orc/workflows/bad-list.yaml"), "name: bad\nsteps:\n  - id: code\n    agent: coder\n")
	writeFile(t, pathInRoot(root, ".orc/workflows/bad-scalar.yaml"), "name: bad\nsteps:\n  code: coder\n")

	goodPath := ".orc/workflows/good.yaml"
	writeFile(t, pathInRoot(root, goodPath), "name: good\nsteps:\n  code:\n    agent: coder\n")

	result := mustPlanWithSchemaMigrations(t, root, testWorkflowStepMigration())

	for _, path := range []string{".orc/workflows/bad-list.yaml", ".orc/workflows/bad-scalar.yaml"} {
		skipped := assertSkippedAction(t, result, path, "schema-migration-skipped")
		if !strings.Contains(skipped.Guidance, "top-level steps mapping") {
			t.Fatalf("guidance = %q, want steps mapping guidance", skipped.Guidance)
		}
	}

	assertAction(t, result, ActionModify, goodPath)
}

func TestAgentFrontmatterHelperPreservesBodyAndNoOpsWithoutFrontmatter(t *testing.T) {
	root := currentScaffold(t)
	body := "\n# Planner\r\nKeep trailing spaces.  \n\n"
	writeFile(t, pathInRoot(root, ".orc/agents/with-frontmatter.md"), "---\nid: custom\nlegacy: true\n---\n"+body)
	writeFile(t, pathInRoot(root, ".orc/agents/no-frontmatter.md"), "# Agent\nlegacy: true\n")

	result := mustPlanWithSchemaMigrations(t, root, schemaMigration{
		ID:      "test-agent-frontmatter",
		Summary: "agent frontmatter authoring",
		Target:  agentFrontmatterMigrationTarget,
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			frontmatter, decision, ok := agentFrontmatter(file)
			if !ok {
				return decision
			}

			if !frontmatter.Exists("legacy") {
				return schemaMigrationDecision{}
			}

			return schemaMigrationDecision{Edits: []SurgicalEdit{
				frontmatter.RemoveASTField("legacy"),
				frontmatter.AddASTField("modern", yamlScalarTrue),
			}}
		},
	})

	assertAction(t, result, ActionModify, ".orc/agents/with-frontmatter.md")
	assertNoActionForPath(t, result, ".orc/agents/no-frontmatter.md")

	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, pathInRoot(root, ".orc/agents/with-frontmatter.md"))
	if !strings.HasSuffix(content, body) {
		t.Fatalf("body tail = %q, want exact body %q", content[len(content)-len(body):], body)
	}

	if strings.Contains(content, "legacy") || !strings.Contains(content, "modern: true\n") {
		t.Fatalf("frontmatter not migrated as expected:\n%s", content)
	}
}

func TestAgentFrontmatterHelperSetsBlockMappingAndPreservesBody(t *testing.T) {
	root := currentScaffold(t)
	body := "\n# Planner\r\nKeep trailing spaces.  \n\n"
	path := ".orc/agents/with-frontmatter.md"
	writeFile(t, pathInRoot(root, path), "---\nid: custom\nlimits: legacy\n---\n"+body)

	result := mustPlanWithSchemaMigrations(t, root, schemaMigration{
		ID:      "test-agent-frontmatter-set",
		Summary: "agent frontmatter set authoring",
		Target:  agentFrontmatterMigrationTarget,
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			frontmatter, decision, ok := agentFrontmatter(file)
			if !ok {
				return decision
			}

			if !frontmatter.Exists("limits") {
				return schemaMigrationDecision{}
			}

			return schemaMigrationDecision{Edits: []SurgicalEdit{
				frontmatter.SetASTField("limits", "enabled: true\nsoft: 2\nhard: 4"),
			}}
		},
	})

	action := assertAction(t, result, ActionModify, path)
	assertEdit(t, action, EditASTSetYAMLField, "limits")

	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, pathInRoot(root, path))
	if !strings.Contains(content, "limits:\n  enabled: true\n  soft: 2\n  hard: 4\n") {
		t.Fatalf("frontmatter not migrated as expected:\n%s", content)
	}

	if !strings.HasSuffix(content, body) {
		t.Fatalf("body tail = %q, want exact body %q", content[len(content)-len(body):], body)
	}
}

func TestRuntimeYAMLHelperMutatesNestedMapThroughSharedEngine(t *testing.T) {
	root := currentScaffold(t)
	runtimePath := testCustomRuntimePath
	writeFile(t, pathInRoot(root, runtimePath), "id: custom\noptions:\n  legacy: true\n")

	result := mustPlanWithSchemaMigrations(t, root, schemaMigration{
		ID:      "test-runtime-authoring",
		Summary: "runtime map authoring",
		Target:  runtimeMigrationTarget,
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			runtime, decision, ok := runtimeYAML(file)
			if !ok {
				return decision
			}

			options, ok := runtime.Map("options")
			if !ok || !options.Exists("legacy") {
				return schemaMigrationDecision{}
			}

			return schemaMigrationDecision{Edits: []SurgicalEdit{
				options.RemoveASTField("legacy"),
				options.AddASTField("modern", yamlScalarTrue),
			}}
		},
	})

	action := assertAction(t, result, ActionModify, runtimePath)
	assertEdit(t, action, EditASTRemoveYAMLField, "options.legacy")
	assertEdit(t, action, EditASTAddYAMLField, "options.modern")

	if _, err := Apply(context.Background(), result, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	content := readFile(t, pathInRoot(root, runtimePath))
	if strings.Contains(content, "legacy") || !strings.Contains(content, "options:\n  modern: true\n") {
		t.Fatalf("runtime content not migrated through AST edits:\n%s", content)
	}
}

func testWorkflowStepMigration() schemaMigration {
	return schemaMigration{
		ID:      "test-workflow-steps",
		Summary: "workflow step visitor",
		Target:  workflowMigrationTarget,
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			return visitWorkflowSteps(file, func(step workflowStepVisit) ([]SurgicalEdit, error) {
				if step.Map.Exists("visited") {
					return nil, nil
				}

				return []SurgicalEdit{step.Map.AddASTField("visited", step.ID)}, nil
			})
		},
	}
}

func assertEditPathPrefix(t *testing.T, action Action, kind EditKind, prefix string) {
	t.Helper()

	if slices.ContainsFunc(action.Edits, func(edit SurgicalEdit) bool {
		return edit.Kind == kind && strings.HasPrefix(edit.Path.String(), prefix)
	}) {
		return
	}

	t.Fatalf("missing %s edit with path prefix %q in action %#v", kind, prefix, action)
}

func mustNode(t *testing.T, node ast.Node, ok bool) ast.Node {
	t.Helper()

	if !ok {
		t.Fatalf("missing node")
	}

	return node
}

func pathInRoot(root, path string) string {
	return filepath.Join(root, filepath.FromSlash(path))
}
