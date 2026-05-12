package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRunConfigShowsSnapshotMetadata(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	workflowContent := string(readCLIFile(t, workflowPath))
	workflowContent = strings.Replace(workflowContent, "timeout: 30m", "timeout: 45m", 1)
	writeCLIFile(t, workflowPath, workflowContent)
	executeCLICommand(t, []string{"run", "refresh-config", result.runID})
	writeCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "version: [\n")

	output := executeCLICommand(t, []string{"run", "config", result.runID})
	assertCLIOutputContainsAll(t, output, []string{
		"run: " + result.runID,
		"current_config_snapshot:",
		"  version: 2",
		"  version_dir: 000002",
		"  resolved: config/000002/resolved.json",
		"  manifest: config/000002/manifest.json",
		"  manifest_hash: sha256:",
		"  source_files: ",
		"  source_hash: sha256:",
		"refresh_history:",
		"    version: 000001 -> 000002",
		"    source: \"cli\"",
	})
}

func TestExecuteRunConfigHelp(t *testing.T) {
	output := executeCLICommand(t, []string{"run", "config", "--help"})
	assertCLIOutputContainsAll(t, output, []string{
		"orc run config <run-id>",
		"current snapshot version",
		"does not load live .orc config",
	})
}
