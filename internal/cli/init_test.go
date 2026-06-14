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

	"tiny-llm-orchestrator/orc/internal/initupgrade"
)

const initUpgradeStatusPartialLine = "status: partial"

func TestExecuteInitDryRunUsesCurrentDirectory(t *testing.T) {
	withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, cliFlagDryRun}, &stdout, &stderr); err != nil {
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

	if err := Execute([]string{commandInit, cliFlagDryRun, cliFlagYes}, &stdout, &stderr); err == nil {
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

	if err := Execute([]string{commandInit, helpFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"orc init scaffolds", cliUsage, cliFlagDryRun, cliFlagYes} {
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
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	before := string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml")))
	output := executeCLICommand(t, []string{commandInit, commandUpgrade})

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
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	output := executeCLICommand(t, []string{commandInit, commandUpgrade, cliFlagApply})

	assertCLIOutputContainsAll(t, output, []string{
		"orc init upgrade applied",
		"setup version: 0 -> 1",
		"modified files:",
		cliConfigPath,
		"result: safe planned changes were written",
	})

	assertCLIOutputContainsAll(t, string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), []string{
		"version: 1\n",
		"setup_version: 1\n",
	})
}

func TestExecuteInitUpgradeApplyPartiallyAppliesWithManualRefreshSkippedAction(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)
	writeCLIFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagApply}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"orc init upgrade partially applied",
		initUpgradeStatusPartialLine,
		"modified files:",
		cliConfigPath,
		"skipped actions:",
		"customized-scaffold-file",
		"local customization was preserved",
		"result: safe planned changes were written; manual refresh items remain",
	})

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for nonfatal skipped scaffold", stderr.String())
	}

	if !strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version: 1\n") {
		t.Fatalf("config did not gain setup_version during partial apply")
	}
}

func TestExecuteInitUpgradeApplyReportsDirtyAffectedPathConflict(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)
	initCleanJJProject(t, root)
	appendCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "# local change\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagApply}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want dirty affected path refusal")
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		"orc init upgrade partially applied",
		initUpgradeStatusPartialLine,
		"created files:",
		"AGENTS.md",
		"conflicts:",
		cliConfigPath,
		"dirty-affected-path",
		"result: safe independent changes were written; unresolved conflicts remain",
	})
	assertCLIOutputContainsAll(t, stderr.String(), []string{
		"orc init upgrade",
		"applied safe changes but unresolved conflicts remain",
	})

	if strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version") {
		t.Fatalf("config gained setup_version despite dirty affected path")
	}
}

func TestExecuteInitUpgradeJSONPlanIncludesStructuredFields(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	if err := os.Remove(filepath.Join(root, ".orc", "runtimes", "codex.yaml")); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	writeCLIFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagJSON}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for JSON plan", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if payload.CurrentSetupVersion != 0 || payload.TargetSetupVersion != 1 || payload.ConfigSchemaVersion != 1 {
		t.Fatalf("versions = current %d target %d schema %d, want 0 1 1", payload.CurrentSetupVersion, payload.TargetSetupVersion, payload.ConfigSchemaVersion)
	}

	if !hasInitUpgradeAction(payload.Actions, "create", ".orc/runtimes/codex.yaml") {
		t.Fatalf("actions = %#v, want runtime create action", payload.Actions)
	}

	if !hasInitUpgradeWarning(payload.Warnings, "older-setup") {
		t.Fatalf("warnings = %#v, want older-setup", payload.Warnings)
	}

	if !hasInitUpgradeSkippedAction(payload.SkippedActions, "customized-scaffold-file") {
		t.Fatalf("skipped actions = %#v, want customized-scaffold-file", payload.SkippedActions)
	}

	if len(payload.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none for customized scaffold", payload.Conflicts)
	}

	assertInitUpgradeJSONOmitsFields(t, stdout.Bytes(), "scope", "setup_version_guidance")
}

func TestExecuteInitUpgradeJSONApplyReportsDirtyAffectedPathConflict(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)
	initCleanJJProject(t, root)
	appendCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "# local change\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagApply, cliFlagJSON}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want dirty affected path refusal")
	}

	if !strings.Contains(stderr.String(), "applied safe changes but unresolved conflicts remain") {
		t.Fatalf("stderr = %q, want partial apply conflict", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if !payload.Applied || !payload.Refused {
		t.Fatalf("applied/refused = %t/%t, want true/true", payload.Applied, payload.Refused)
	}

	if !hasInitUpgradeConflict(payload.Conflicts, "dirty-affected-path") {
		t.Fatalf("conflicts = %#v, want dirty-affected-path", payload.Conflicts)
	}

	if payload.ApplyRefusal == nil || payload.ApplyRefusal.Reason != "apply completed with unresolved conflicts" {
		t.Fatalf("apply_refusal = %#v, want refusal reason", payload.ApplyRefusal)
	}

	assertInitUpgradeJSONApplyRefusalOmitsConflicts(t, stdout.Bytes())

	if strings.Contains(string(readCLIFile(t, filepath.Join(root, ".orc", "config.yaml"))), "setup_version") {
		t.Fatalf("config gained setup_version despite dirty affected path")
	}
}

func TestExecuteInitUpgradeJSONApplyReportsCustomizedScaffoldSkip(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{"init", "--yes"})
	removeCLISetupVersion(t, root)
	writeCLIFile(t, filepath.Join(root, ".orc", "agents", "planner.md"), "custom planner\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"init", "upgrade", "--apply", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for nonfatal skipped scaffold", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if !payload.Applied || payload.Refused {
		t.Fatalf("applied/refused = %t/%t, want true/false", payload.Applied, payload.Refused)
	}

	if !hasInitUpgradeSkippedAction(payload.SkippedActions, "customized-scaffold-file") {
		t.Fatalf("skipped actions = %#v, want customized-scaffold-file", payload.SkippedActions)
	}

	if len(payload.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none for customized scaffold", payload.Conflicts)
	}
}

func TestExecuteInitUpgradeJSONApplyIncludesWrittenPaths(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagApply, cliFlagJSON}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for JSON apply", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusApplied {
		t.Fatalf("status = %q, want applied", payload.Status)
	}

	if !payload.Applied || payload.Refused {
		t.Fatalf("applied/refused = %t/%t, want true/false", payload.Applied, payload.Refused)
	}

	if !containsString(payload.ModifiedPaths, cliConfigPath) {
		t.Fatalf("modified paths = %#v, want config path", payload.ModifiedPaths)
	}

	if !hasInitUpgradeWarning(payload.Warnings, "older-setup") {
		t.Fatalf("warnings = %#v, want older setup warning in JSON", payload.Warnings)
	}

	assertInitUpgradeJSONOmitsFields(t, stdout.Bytes(), "apply_warnings", "apply_result", "scope", "setup_version_guidance")
}

func TestExecuteInitUpgradeJSONApplyReportsDependencySkippedAction(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	configPath := filepath.Join(root, ".orc", "config.yaml")
	replaceCLIFile(t, configPath, "runtimes:\n  codex: runtimes/codex.yaml\n", "runtimes:\n  codex: runtimes/custom.yaml\n")

	if err := os.Remove(filepath.Join(root, ".orc", "runtimes", "codex.yaml")); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagApply, cliFlagJSON}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unresolved config conflict")
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if !hasInitUpgradeSkippedActionCode(payload.SkippedActions, "dependency-skipped") {
		t.Fatalf("skipped actions = %#v, want dependency-skipped", payload.SkippedActions)
	}

	if hasInitUpgradeConflict(payload.Conflicts, "dependency-skipped") {
		t.Fatalf("conflicts = %#v, want dependency-skipped omitted from conflicts", payload.Conflicts)
	}

	if !strings.Contains(stderr.String(), "unresolved conflicts remain") {
		t.Fatalf("stderr = %q, want unresolved conflicts", stderr.String())
	}
}

func TestExecuteInitUpgradeJSONPlanReportsDependencySkippedAction(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	configPath := filepath.Join(root, ".orc", "config.yaml")
	replaceCLIFile(t, configPath, "runtimes:\n  codex: runtimes/codex.yaml\n", "runtimes:\n  codex: runtimes/custom.yaml\n")

	if err := os.Remove(filepath.Join(root, ".orc", "runtimes", "codex.yaml")); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagJSON}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if !hasInitUpgradeSkippedActionCode(payload.SkippedActions, "dependency-skipped") {
		t.Fatalf("skipped actions = %#v, want dependency-skipped", payload.SkippedActions)
	}

	if len(payload.SkippedActions[0].DependsOn) == 0 {
		t.Fatalf("skipped actions = %#v, want depends_on", payload.SkippedActions)
	}
}

func TestInitUpgradeOutputIncludesSchemaMigrationReason(t *testing.T) {
	const schemaMigrationReason = "schema migration test-output: rename custom workflow field"

	editPath, err := initupgrade.ParseYAMLPath("new_field")
	if err != nil {
		t.Fatalf("ParseYAMLPath returned error: %v", err)
	}

	plan := &initupgrade.Result{
		ProjectRoot:         "/tmp/project",
		ConfigSchemaVersion: 1,
		CurrentSetupVersion: 1,
		TargetSetupVersion:  1,
		Actions: []initupgrade.Action{{
			Kind:   initupgrade.ActionModify,
			Path:   ".orc/workflows/custom.yaml",
			Reason: schemaMigrationReason,
			Edits: []initupgrade.SurgicalEdit{{
				Kind:  initupgrade.EditAddYAMLField,
				Path:  editPath,
				Value: "true",
			}},
		}},
	}

	var stdout bytes.Buffer
	if err := printInitUpgradePlan(&stdout, plan); err != nil {
		t.Fatalf("printInitUpgradePlan returned error: %v", err)
	}

	assertCLIOutputContainsAll(t, stdout.String(), []string{
		schemaMigrationReason,
		"edit: add_yaml_field new_field",
	})

	payload := initUpgradePlanJSON(plan)
	if len(payload.Actions) != 1 || payload.Actions[0].Reason != schemaMigrationReason {
		t.Fatalf("JSON actions = %#v, want schema migration reason", payload.Actions)
	}
}

func TestExecuteInitUpgradeOutputsProductionSchemaMigration(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	replaceCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  max_loops: 3\n")

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagJSON}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())

	action := initUpgradeJSONActionForPath(t, payload, cliConfigPath)
	if !strings.Contains(action.Reason, "schema migration config-defaults-max-loops-to-loop-caps: migrate defaults.max_loops to defaults.loop_caps") {
		t.Fatalf("action reason = %q, want production schema migration", action.Reason)
	}

	if !hasInitUpgradeJSONEdit(action.Edits, string(initupgrade.EditRemoveYAMLField), "defaults.max_loops") ||
		!hasInitUpgradeJSONEdit(action.Edits, string(initupgrade.EditAddYAMLField), "defaults.loop_caps") {
		t.Fatalf("action edits = %#v, want max_loops removal and loop_caps add", action.Edits)
	}

	output := executeCLICommand(t, []string{commandInit, commandUpgrade})
	assertCLIOutputContainsAll(t, output, []string{
		"schema migration config-defaults-max-loops-to-loop-caps: migrate defaults.max_loops to defaults.loop_caps",
		"edit: remove_yaml_field defaults.max_loops",
		"edit: add_yaml_field defaults.loop_caps",
	})
}

func TestExecuteInitUpgradeReportsProductionSchemaMigrationConflictWithSafeActions(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	replaceCLIFile(t, filepath.Join(root, ".orc", "config.yaml"), "defaults:\n  loop_caps:\n    enabled: true\n    soft: 2\n    hard: 4\n", "defaults:\n  max_loops: 3\n  loop_caps:\n    enabled: true\n    soft: 3\n    hard: 4\n")

	if err := os.Remove(filepath.Join(root, ".orc", "runtimes", "codex.yaml")); err != nil {
		t.Fatalf("remove runtime: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, commandUpgrade, cliFlagJSON}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for JSON plan", stderr.String())
	}

	payload := decodeInitUpgradeJSON(t, stdout.Bytes())
	if payload.Status != initUpgradeStatusPartial {
		t.Fatalf("status = %q, want partial", payload.Status)
	}

	if !hasInitUpgradeConflict(payload.Conflicts, "schema-migration-conflict") {
		t.Fatalf("conflicts = %#v, want schema migration conflict", payload.Conflicts)
	}

	if hasInitUpgradeAction(payload.Actions, "modify", cliConfigPath) {
		t.Fatalf("actions = %#v, want no config action while schema migration conflicts", payload.Actions)
	}

	if !hasInitUpgradeAction(payload.Actions, "create", ".orc/runtimes/codex.yaml") {
		t.Fatalf("actions = %#v, want unrelated runtime create action", payload.Actions)
	}

	human := executeCLICommand(t, []string{commandInit, commandUpgrade})
	assertCLIOutputContainsAll(t, human, []string{
		initUpgradeStatusPartialLine,
		"schema-migration-conflict",
		"schema migration config-defaults-max-loops-to-loop-caps",
		"create .orc/runtimes/codex.yaml",
	})
}

func TestExecuteInitUpgradeHelpDoesNotExposeDryRunFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{commandInit, commandUpgrade, helpFlag}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	output := stdout.String()
	assertCLIOutputContainsAll(t, output, []string{"Bare orc init upgrade is plan-only", cliFlagApply, cliFlagJSON, cliRefreshConfigUsage})

	if strings.Contains(output, cliFlagDryRun) {
		t.Fatalf("help output advertised --dry-run:\n%s", output)
	}

	stdout.Reset()
	stderr.Reset()

	if err := Execute([]string{commandInit, commandUpgrade, cliFlagDryRun}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown --dry-run flag")
	}

	if !strings.Contains(stderr.String(), "unknown flag: --dry-run") {
		t.Fatalf("stderr = %q, want unknown dry-run flag", stderr.String())
	}
}

func TestExecuteInitUpgradeDoesNotModifyRunsTree(t *testing.T) {
	root := withTempCwd(t)
	executeCLICommand(t, []string{commandInit, cliFlagYes})
	removeCLISetupVersion(t, root)

	runPath := filepath.Join(root, ".orc", "runs", cliRunIDOne, "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(runPath), 0o750); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeCLIFile(t, runPath, "setup_version: 999\n")

	output := executeCLICommand(t, []string{commandInit, commandUpgrade, cliFlagApply})
	if got := string(readCLIFile(t, runPath)); got != "setup_version: 999\n" {
		t.Fatalf("run file = %q, want untouched", got)
	}

	assertCLIOutputContainsAll(t, output, []string{cliRefreshConfigUsage})
}

func TestExecuteRunStartWarnsForOlderLiveSetup(t *testing.T) {
	root := withTempCwd(t)
	writeCLIProject(t, root, "optional", true)
	removeCLISetupVersion(t, root)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandRun, cliCommandStart, cliFlagWorkflow, cliWorkflowImplementation, cliFlagTask, cliTaskMarkdown}, &stdout, &stderr); err != nil {
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
	if err := Execute([]string{commandRun, cliCommandStart, cliFlagWorkflow, cliWorkflowImplementation, cliFlagTask, cliTaskMarkdown}, &stdout, &stderr); err != nil {
		t.Fatalf("Execute returned error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no older setup warning", stderr.String())
	}
}

func TestExecuteInitUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Execute([]string{commandInit, cliFlagBogus}, &stdout, &stderr); err == nil {
		t.Fatal("Execute returned nil error, want unknown flag error")
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	output := stderr.String()
	for _, want := range []string{`unknown flag: --bogus`, cliUsage, "orc init"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestExecuteInitYesCreatesScaffold(t *testing.T) {
	root := withTempCwd(t)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{commandInit, cliFlagYes}, &stdout, &stderr); err != nil {
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
	if err := ExecuteWithInput([]string{commandInit}, stdin, &stdout, &stderr); err != nil {
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
	return strings.NewReader(strings.Join([]string{cliConfirmYes, cliConfirmYes, cliConfirmYes}, "\n") + "\n")
}

type initUpgradeTestJSON struct {
	Status              string `json:"status"`
	ConfigSchemaVersion int    `json:"config_schema_version"`
	CurrentSetupVersion int    `json:"current_setup_version"`
	TargetSetupVersion  int    `json:"target_setup_version"`
	Actions             []struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
		Edits  []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"edits"`
	} `json:"actions"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
	Conflicts []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"conflicts"`
	SkippedActions []struct {
		Path       string   `json:"path"`
		Code       string   `json:"code"`
		Message    string   `json:"message"`
		Guidance   string   `json:"guidance"`
		ActionKind string   `json:"action_kind"`
		DependsOn  []string `json:"depends_on"`
	} `json:"skipped_actions"`
	Applied       bool     `json:"applied"`
	Refused       bool     `json:"refused"`
	ModifiedPaths []string `json:"modified_paths"`
	ApplyRefusal  *struct {
		Reason string `json:"reason"`
	} `json:"apply_refusal"`
}

func initUpgradeJSONActionForPath(t *testing.T, payload initUpgradeTestJSON, path string) struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Edits  []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"edits"`
} {
	t.Helper()

	for _, action := range payload.Actions {
		if action.Path == path {
			return action
		}
	}

	t.Fatalf("missing JSON action for %s; actions = %#v", path, payload.Actions)

	return struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
		Edits  []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"edits"`
	}{}
}

func hasInitUpgradeJSONEdit(edits []struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}, kind, path string,
) bool {
	return slices.ContainsFunc(edits, func(edit struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	},
	) bool {
		return edit.Kind == kind && edit.Path == path
	})
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
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Edits  []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"edits"`
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
	Code    string `json:"code"`
	Message string `json:"message"`
}, code string,
) bool {
	for _, conflict := range conflicts {
		if conflict.Code == code {
			return true
		}
	}

	return false
}

func hasInitUpgradeSkippedAction(skipped []struct {
	Path       string   `json:"path"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Guidance   string   `json:"guidance"`
	ActionKind string   `json:"action_kind"`
	DependsOn  []string `json:"depends_on"`
}, code string,
) bool {
	for _, item := range skipped {
		if item.Code == code && item.Path != "" && item.Message != "" && strings.Contains(item.Guidance, "local customization was preserved") && item.ActionKind != "" {
			return true
		}
	}

	return false
}

func hasInitUpgradeSkippedActionCode(skipped []struct {
	Path       string   `json:"path"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Guidance   string   `json:"guidance"`
	ActionKind string   `json:"action_kind"`
	DependsOn  []string `json:"depends_on"`
}, code string,
) bool {
	for _, item := range skipped {
		if item.Code == code && item.Path != "" && item.Message != "" && item.Guidance != "" && item.ActionKind != "" {
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

func replaceCLIFile(t *testing.T, path, old, replacement string) {
	t.Helper()

	content := string(readCLIFile(t, path))
	if !strings.Contains(content, old) {
		t.Fatalf("%s missing content %q", path, old)
	}

	writeCLIFile(t, path, strings.Replace(content, old, replacement, 1))
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

	runCLITestCommand(t, root, "jj", "git", commandInit, "--colocate", ".")
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
