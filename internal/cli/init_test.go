package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExecuteInitDryRunUsesCurrentDirectory(t *testing.T) {
	withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "orc init dry-run:") {
		t.Fatalf("stdout = %q, want init dry-run routing", got)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitRejectsDryRunWithYes(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--dry-run", "--yes"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want invalid flags")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if got := stderr.String(); !strings.Contains(got, "orc init:") {
		t.Fatalf("stderr = %q, want init error context", got)
	}
}

func TestExecuteInitHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"orc init scaffolds", "Usage:", "--dry-run", "--yes"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteInitUpgradePlansWithoutWriting(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)

	before := string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml")))
	output := executeCLICommand(t, []string{"init", "upgrade"})

	assertCLIOutputContainsAll(t, output, []string{
		"orc init upgrade plan",
		"setup version: 0 -> 1",
		"planned changes:",
		"apply: run orc init upgrade --apply",
		".orc/runs/** is never modified",
		"missing setup_version is treated as legacy setup version 0",
	})

	if got := string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))); got != before {
		t.Fatalf("config changed during plan-only upgrade:\n%s", got)
	}
}

func TestExecuteInitUpgradeApplyWritesSafePlan(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)

	output := executeCLICommand(t, []string{"init", "upgrade", "--apply"})

	assertCLIOutputContainsAll(t, output, []string{
		"orc init upgrade applied",
		"setup version: 0 -> 1",
		"modified files:",
		".orc/config.yaml",
		"result: safe planned changes were written",
	})

	assertCLIOutputContainsAll(t, string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), []string{
		"version: 1\n",
		"setup_version: 1\n",
	})
}

func TestExecuteInitUpgradeApplyRefusesConflicts(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)
	writeCLIFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--apply"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want conflict refusal")
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"conflicts:",
		"customized-scaffold-file",
		"--apply will not write until conflicts are resolved",
	})
	assertCLIOutputContainsAll(t, stderr.String(), []string{
		"orc init upgrade",
		"conflicts must be resolved before --apply can write",
	})

	if strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version") {
		t.Fatalf("config gained setup_version despite conflict refusal")
	}
}

func TestExecuteInitUpgradeApplyReportsDirtyAffectedPathConflict(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)
	initCleanJJProject(t, root)
	appendCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "# local change\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--apply"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want dirty affected path refusal")
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"orc init upgrade apply refused",
		"planned changes:",
		"conflicts:",
		".orc/config.yaml",
		"dirty-affected-path",
		"--apply did not write because conflicts were detected during apply",
	})
	assertCLIOutputContainsAll(t, stderr.String(), []string{
		"orc init upgrade",
		"apply detected conflicts and did not write",
	})

	if strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version") {
		t.Fatalf("config gained setup_version despite dirty affected path")
	}
}

func TestExecuteInitUpgradeJSONPlanIncludesStructuredFields(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)

	if err := os.Remove(filepath.Join(root, ".orc", "runtimes", "codex.yaml")); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	writeCLIFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for JSON plan", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.CurrentSetupVersion != 0 || payload.TargetSetupVersion != 1 || payload.ConfigSchemaVersion != 1 {
		t.Fatalf("versions = current %d target %d schema %d, want 0 1 1", payload.CurrentSetupVersion, payload.TargetSetupVersion, payload.ConfigSchemaVersion)
	}

	if !hasInitUpgradeAction(payload.Actions, "create", ".orc/runtimes/codex.yaml") {
		t.Fatalf("actions = %#v, want runtime create action", payload.Actions)
	}

	if !hasInitUpgradeWarning(payload.Warnings, "older-setup") {
		t.Fatalf("warnings = %#v, want older-setup", payload.Warnings)
	}

	if !hasInitUpgradeConflict(payload.Conflicts, "customized-scaffold-file") {
		t.Fatalf("conflicts = %#v, want customized-scaffold-file", payload.Conflicts)
	}

	assertInitUpgradeJSONOmitsFields(t, stdout.Bytes(), "scope", "setup_version_guidance")
}

func TestExecuteInitUpgradeJSONApplyReportsDirtyAffectedPathConflict(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)
	initCleanJJProject(t, root)
	appendCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "# local change\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--apply", "--json"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want dirty affected path refusal")
	}

	if !strings.Contains(stderr.String(), "apply detected conflicts and did not write") {
		t.Fatalf("stderr = %q, want apply conflict refusal", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Applied || !payload.Refused {
		t.Fatalf("applied/refused = %t/%t, want false/true", payload.Applied, payload.Refused)
	}

	if !hasInitUpgradeConflict(payload.Conflicts, "dirty-affected-path") {
		t.Fatalf("conflicts = %#v, want dirty-affected-path", payload.Conflicts)
	}

	if payload.ApplyRefusal == nil || payload.ApplyRefusal.Reason != "apply detected conflicts; --apply did not write" {
		t.Fatalf("apply_refusal = %#v, want refusal reason", payload.ApplyRefusal)
	}

	assertInitUpgradeJSONApplyRefusalOmitsConflicts(t, stdout.Bytes())

	if strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version") {
		t.Fatalf("config gained setup_version despite dirty affected path")
	}
}

func TestExecuteInitUpgradeJSONApplyIncludesWrittenPaths(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--apply", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for JSON apply", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if !payload.Applied || payload.Refused {
		t.Fatalf("applied/refused = %t/%t, want true/false", payload.Applied, payload.Refused)
	}

	if !containsString(payload.ModifiedPaths, ".orc/config.yaml") {
		t.Fatalf("modified paths = %#v, want config path", payload.ModifiedPaths)
	}

	if !hasInitUpgradeWarning(payload.Warnings, "older-setup") {
		t.Fatalf("warnings = %#v, want older setup warning in JSON", payload.Warnings)
	}

	assertInitUpgradeJSONOmitsFields(t, stdout.Bytes(), "apply_warnings", "apply_result", "scope", "setup_version_guidance")
}

func TestExecuteInitUpgradeHelpDoesNotExposeDryRunFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "upgrade", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	output := stdout.String()
	assertCLIOutputContainsAll(t, output, []string{"Bare orc init upgrade is plan-only", "--apply", "--json", "orc run refresh-config <run-id>"})

	if strings.Contains(output, "--dry-run") {
		t.Fatalf("help output advertised --dry-run:\n%s", output)
	}

	stdout.Reset()
	stderr.Reset()

	if err := Execute([]string{"init", "upgrade", "--dry-run"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown --dry-run flag")
	}

	if !strings.Contains(stderr.String(), "unknown flag: --dry-run") {
		t.Fatalf("stderr = %q, want unknown dry-run flag", stderr.String())
	}
}

func TestExecuteInitUpgradeDoesNotModifyRunsTree(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)

	runPath := filepath.Join(root, ".orc", "runs", "run-1", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(runPath), 0o750); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeCLIFile(t, runPath, "setup_version: 999\n")

	output := executeCLICommand(t, []string{"init", "upgrade", "--apply"})
	if got := string(readCLIFile(t, runPath)); got != "setup_version: 999\n" {
		t.Fatalf("run file = %q, want untouched", got)
	}

	assertCLIOutputContainsAll(t, output, []string{"orc run refresh-config <run-id>"})
}

func TestExecuteRunStartWarnsForOlderLiveSetup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	removeCLISetupVersion(t, root)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "start", "--workflow", "implementation", "--task", "# Task"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stderr.String(), `warning: project Tiny Orc setup version 0 is older than this orc supports (1); run "orc init upgrade" to inspect the upgrade plan`) {
		t.Fatalf("stderr = %q, want older setup warning", stderr.String())
	}
}

func TestExecuteRunStartDoesNotWarnForCurrentSetup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "start", "--workflow", "implementation", "--task", "# Task"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no older setup warning", stderr.String())
	}
}

func TestExecuteInitUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{"init", "--bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	output := stderr.String()
	for _, want := range []string{`unknown flag: --bogus`, "Usage:", "orc init"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteInitYesCreatesScaffold(t *testing.T) {
	root := withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "--yes"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if got := stdout.String(); !strings.Contains(got, "created .orc/config.yaml") {
		t.Fatalf("stdout = %q, want scaffold creation output", got)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if _, err := os.Stat(filepath.Join(root, ".orc", "config.yaml")); err != nil {
		t.Fatalf("config stat error: %v", err)
	}
}

func TestExecuteWithInputInitForwardsInteractiveInput(t *testing.T) {
	root := withTempCwd(t)

	if err := os.MkdirAll(filepath.Join(root, ".orc"), 0o755); err != nil {
		t.Fatalf("create .orc: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, ".orc", "config.yaml"), []byte("custom: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer

	stdin := confirmThreeInitPromptsThroughCLI()
	if err := ExecuteWithInput([]string{"init"}, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("ExecuteWithInput returned error: %v\nstderr: %s", err, stderr.String())
	}

	if got := stdout.String(); !strings.Contains(got, "Overwrite .orc/config.yaml?") {
		t.Fatalf("stdout = %q, want forwarded interactive response", got)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func withTempCwd(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir temp root: %v", err)
	}

	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	return root
}

func confirmThreeInitPromptsThroughCLI() *strings.Reader {
	return strings.NewReader(strings.Join([]string{"yes", "yes", "yes"}, "\n") + "\n")
}

type initUpgradeTestJSON struct {
	ConfigSchemaVersion int `json:"config_schema_version"`
	CurrentSetupVersion int `json:"current_setup_version"`
	TargetSetupVersion  int `json:"target_setup_version"`
	Actions             []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"actions"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
	Conflicts []struct {
		Code string `json:"code"`
	} `json:"conflicts"`
	Applied       bool     `json:"applied"`
	Refused       bool     `json:"refused"`
	ModifiedPaths []string `json:"modified_paths"`
	ApplyRefusal  *struct {
		Reason string `json:"reason"`
	} `json:"apply_refusal"`
}

func decodeInitUpgradeJSON(t *testing.T, content []byte) initUpgradeTestJSON {
	t.Helper()

	var payload initUpgradeTestJSON
	if err := json.Unmarshal(content, &payload); err != nil {
		t.Fatalf("decode init upgrade JSON %q: %v", string(content), err)
	}

	return payload
}

func assertInitUpgradeJSONOmitsFields(t *testing.T, content []byte, fields ...string) {
	t.Helper()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("decode init upgrade raw JSON %q: %v", string(content), err)
	}

	for _, field := range fields {
		if _, ok := raw[field]; ok {
			t.Fatalf("init upgrade JSON included redundant field %q:\n%s", field, string(content))
		}
	}
}

func assertInitUpgradeJSONApplyRefusalOmitsConflicts(t *testing.T, content []byte) {
	t.Helper()

	var raw struct {
		ApplyRefusal map[string]json.RawMessage `json:"apply_refusal"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("decode init upgrade raw JSON %q: %v", string(content), err)
	}

	if raw.ApplyRefusal == nil {
		t.Fatalf("init upgrade JSON omitted apply_refusal:\n%s", string(content))
	}

	if _, ok := raw.ApplyRefusal["conflicts"]; ok {
		t.Fatalf("init upgrade JSON duplicated conflicts in apply_refusal:\n%s", string(content))
	}
}

func hasInitUpgradeAction(actions []struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}, kind, path string,
) bool {
	for _, action := range actions {
		if action.Kind == kind && action.Path == path {
			return true
		}
	}

	return false
}

func hasInitUpgradeWarning(warnings []struct {
	Code string `json:"code"`
}, code string,
) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}

	return false
}

func hasInitUpgradeConflict(conflicts []struct {
	Code string `json:"code"`
}, code string,
) bool {
	for _, conflict := range conflicts {
		if conflict.Code == code {
			return true
		}
	}

	return false
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func removeCLISetupVersion(t *testing.T, root string) {
	t.Helper()

	configPath := filepath.Join(root, ".orc", "config.yaml")
	content := string(readCLIFile(t, configPath))
	content = strings.Replace(content, "setup_version: 1\n", "", 1)
	writeCLIFile(t, configPath, content)
}

func appendCLIFile(t *testing.T, path, content string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}

	defer func() {
		if err := file.Close(); err != nil {
			t.Fatalf("close %s: %v", path, err)
		}
	}()

	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func initCleanJJProject(t *testing.T, root string) {
	t.Helper()

	if _, err := exec.LookPath("jj"); err != nil {
		t.Skipf("jj not available: %v", err)
	}

	runCLITestCommand(t, root, "jj", "git", "init", "--colocate", ".")
	runCLITestCommand(t, root, "jj", "describe", "-m", "test baseline")
	runCLITestCommand(t, root, "jj", "new")
}

func runCLITestCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(output))
	}
}
