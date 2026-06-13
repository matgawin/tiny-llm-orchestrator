package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRunRefreshConfigPublishesSnapshot(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	result := executeCLIRunStart(t, root, []string{cliFlagTask, cliTaskMarkdown}, nil)
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	workflowContent := string(readCLIFile(t, workflowPath))
	workflowContent = strings.Replace(workflowContent, "timeout: 30m", "timeout: 45m", 1)
	writeCLIFile(t, workflowPath, workflowContent)

	output := executeCLICommand(t, []string{commandRun, cliCommandRefreshConfig, result.runID})
	assertCLIOutputContainsAll(t, output, []string{
		"refreshed run " + result.runID + " config 000001 -> 000002",
		"manifest sha256:",
	})
	current := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, cliCommandConfig, "current.json")))
	assertCLIOutputContainsAll(t, current, []string{`"version": 2`, `"version_dir": "000002"`})
}

func TestExecuteRunRefreshConfigRejectsForceFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{commandRun, cliCommandRefreshConfig, cliRunIDOne, "--force"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want --force rejection")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if !strings.Contains(stderr.String(), "unknown flag: --force") {
		t.Fatalf("stderr = %q, want unsupported --force rejection", stderr.String())
	}
}

func TestExecuteRunRefreshConfigHelp(t *testing.T) {
	output := executeCLICommand(t, []string{commandRun, cliCommandRefreshConfig, helpFlag})
	assertCLIOutputContainsAll(t, output, []string{
		cliRefreshConfigUsage,
		"There is no --force flag in v1.",
	})
}
