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
	result := executeCLIRunStart(t, root, []string{"--task", "# Task"}, nil)
	workflowPath := filepath.Join(root, ".orc", "workflows", "implementation.yaml")
	workflowContent := string(readCLIFile(t, workflowPath))
	workflowContent = strings.Replace(workflowContent, "timeout: 30m", "timeout: 45m", 1)
	writeCLIFile(t, workflowPath, workflowContent)

	output := executeCLICommand(t, []string{"run", "refresh-config", result.runID})
	assertCLIOutputContainsAll(t, output, []string{
		"refreshed run " + result.runID + " config 000001 -> 000002",
		"manifest sha256:",
	})
	current := string(readCLIFile(t, filepath.Join(root, ".orc", "runs", result.runID, "config", "current.json")))
	assertCLIOutputContainsAll(t, current, []string{`"version": 2`, `"version_dir": "000002"`})
}

func TestExecuteRunRefreshConfigRejectsForceFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"run", "refresh-config", "run-1", "--force"}, &stdout, &stderr); err == nil {
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
	output := executeCLICommand(t, []string{"run", "refresh-config", "--help"})
	assertCLIOutputContainsAll(t, output, []string{
		"orc run refresh-config <run-id>",
		"There is no --force flag in v1.",
	})
}
