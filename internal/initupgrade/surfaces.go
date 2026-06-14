package initupgrade

import (
	"strings"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

type migrationParseMode string

const (
	migrationParseYAML                migrationParseMode = "yaml"
	migrationParseMarkdownFrontmatter migrationParseMode = "markdown_frontmatter"
	migrationParseScaffoldManifest    migrationParseMode = "scaffold_manifest_metadata"
)

type migrationSurface struct {
	Name               string
	Target             func(path string) bool
	ParseMode          migrationParseMode
	SupportedShape     string
	DocsSummary        string
	ExplicitTargetOnly bool
}

var migrationSurfaces = []migrationSurface{
	{
		Name:           "config",
		Target:         exactSchemaMigrationTarget(configPath),
		ParseMode:      migrationParseYAML,
		SupportedShape: "top-level YAML mapping in .orc/config.yaml",
		DocsSummary:    "Project config migrations should inspect raw .orc/config.yaml YAML before typed config validation and plan only AST-backed map edits.",
	},
	{
		Name:           "workflow",
		Target:         workflowMigrationTarget,
		ParseMode:      migrationParseYAML,
		SupportedShape: "top-level YAML mapping with a steps mapping whose values are step mappings",
		DocsSummary:    "Workflow migrations should use the workflow step visitor for .orc/workflows/** files, including user-created workflows not referenced from .orc/config.yaml.",
	},
	{
		Name:           "runtime",
		Target:         runtimeMigrationTarget,
		ParseMode:      migrationParseYAML,
		SupportedShape: "top-level YAML mapping in .orc/runtimes/**",
		DocsSummary:    "Runtime migrations should use AST-backed map helpers for top-level and nested map inspection and edits.",
	},
	{
		Name:           "agent-frontmatter",
		Target:         agentFrontmatterMigrationTarget,
		ParseMode:      migrationParseMarkdownFrontmatter,
		SupportedShape: "Markdown file in .orc/agents/** with YAML frontmatter delimited by ---",
		DocsSummary:    "Agent migrations should edit YAML frontmatter only and preserve Markdown body bytes exactly. Files without frontmatter no-op unless the migration documents another rule.",
	},
	{
		Name:               "scaffold-manifest-metadata",
		Target:             exactSchemaMigrationTarget(initconfig.ScaffoldManifestPath()),
		ParseMode:          migrationParseScaffoldManifest,
		SupportedShape:     "scaffold ownership metadata in .orc/scaffold.lock.yaml",
		DocsSummary:        "Manifest metadata migrations may target .orc/scaffold.lock.yaml only when explicitly declared and must not change scaffold ownership refresh rules.",
		ExplicitTargetOnly: true,
	},
}

func workflowMigrationTarget(path string) bool {
	return strings.HasPrefix(path, ".orc/workflows/") && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"))
}

func runtimeMigrationTarget(path string) bool {
	return strings.HasPrefix(path, ".orc/runtimes/") && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"))
}

func agentFrontmatterMigrationTarget(path string) bool {
	return strings.HasPrefix(path, ".orc/agents/") && strings.HasSuffix(path, ".md")
}
