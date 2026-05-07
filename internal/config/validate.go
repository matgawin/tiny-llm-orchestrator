package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultLoopCapsEnabled = true
	defaultLoopCapsSoft    = 2
	defaultLoopCapsHard    = 4

	sandboxMountModeRO   = "ro"
	sandboxMountModeRW   = "rw"
	sandboxMountModeAuto = "auto"
)

func validateProjectConfig(projectRoot string, cfg *ProjectConfig) error {
	if cfg.Version != schemaVersion {
		return fmt.Errorf("config version = %d, want %d", cfg.Version, schemaVersion)
	}
	if len(cfg.Workflows) == 0 {
		return errors.New("config must declare at least one workflow")
	}
	if len(cfg.Agents) == 0 {
		return errors.New("config must declare at least one agent")
	}
	if err := validateLoopCapsConfig("defaults.loop_caps", cfg.Defaults.LoopCaps); err != nil {
		return err
	}
	for name, ref := range cfg.Workflows {
		if ref.Path == "" {
			return fmt.Errorf("workflow %q path is required", name)
		}
		if err := validateLoopCapsConfig(fmt.Sprintf("workflows.%s.loop_caps", name), ref.LoopCaps); err != nil {
			return err
		}
		effective := resolveLoopCaps(cfg.Defaults.LoopCaps, ref.LoopCaps)
		if err := validateEffectiveLoopCaps(fmt.Sprintf("workflows.%s.loop_caps", name), effective); err != nil {
			return err
		}
	}
	if cfg.Sandbox != nil {
		if err := validateSandboxConfig(projectRoot, cfg.Sandbox); err != nil {
			return err
		}
	}
	return nil
}

func validateSandboxConfig(projectRoot string, sandbox *SandboxConfig) error {
	if len(sandbox.Command.Argv) == 0 {
		return errors.New("sandbox.command.argv must declare at least one argument")
	}
	for i, arg := range sandbox.Command.Argv {
		if arg == "" {
			return fmt.Errorf("sandbox.command.argv[%d] is empty", i)
		}
	}
	if sandbox.CWD == "" {
		sandbox.CWD = "."
	}
	if err := validateSandboxCWD(projectRoot, sandbox.CWD); err != nil {
		return err
	}
	if !sandbox.Bubblewrap.Network.Set {
		sandbox.Bubblewrap.Network = RequiredBool{Value: true, Set: true}
	}
	if err := validateBubblewrapMountConfig(sandbox.Bubblewrap.Mounts); err != nil {
		return err
	}
	if err := validateSandboxEnvConfig(sandbox.Env); err != nil {
		return err
	}
	for i, mount := range sandbox.Mounts {
		if err := validateSandboxMount(projectRoot, i, mount); err != nil {
			return err
		}
	}
	return nil
}

func validateSandboxCWD(projectRoot, cwd string) error {
	if filepath.IsAbs(cwd) {
		return fmt.Errorf("sandbox.cwd %q must be repo-relative", cwd)
	}
	clean := filepath.Clean(cwd)
	if cwd != "" && clean != cwd {
		return fmt.Errorf("sandbox.cwd %q must be clean and stay under repository root", cwd)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sandbox.cwd %q must be clean and stay under repository root", cwd)
	}
	path := filepath.Join(projectRoot, clean)
	realRoot, realPath, err := resolveExistingProjectPath(projectRoot, path)
	if err != nil {
		return fmt.Errorf("sandbox.cwd %q: %w", cwd, err)
	}
	if err := validateResolvedUnderRoot(realRoot, realPath); err != nil {
		return fmt.Errorf("sandbox.cwd %q: %w", cwd, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return fmt.Errorf("sandbox.cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox.cwd %q must be a directory", cwd)
	}
	return nil
}

func validateBubblewrapMountConfig(mounts BubblewrapMountConfig) error {
	if err := validatePresetMountMode("sandbox.bubblewrap.mounts.repo", mounts.Repo, false); err != nil {
		return err
	}
	if err := validatePresetMountMode("sandbox.bubblewrap.mounts.beads", mounts.Beads, true); err != nil {
		return err
	}
	if err := validatePresetMountMode("sandbox.bubblewrap.mounts.codex_home", mounts.CodexHome, false); err != nil {
		return err
	}
	if err := validatePresetMountMode("sandbox.bubblewrap.mounts.tmp", mounts.Tmp, false); err != nil {
		return err
	}
	return nil
}

func validatePresetMountMode(name, mode string, allowAuto bool) error {
	if mode == "" || mode == sandboxMountModeRO || mode == sandboxMountModeRW {
		return nil
	}
	if allowAuto && mode == sandboxMountModeAuto {
		return nil
	}
	allowed := "ro, rw"
	if allowAuto {
		allowed = "auto, ro, rw"
	}
	return fmt.Errorf("%s %q is invalid; allowed: %s", name, mode, allowed)
}

func validateSandboxEnvConfig(env SandboxEnvConfig) error {
	for i, name := range env.Pass {
		if name == "" {
			return fmt.Errorf("sandbox.env.pass[%d] is empty", i)
		}
	}
	for name := range env.Set {
		if name == "" {
			return errors.New("sandbox.env.set contains an empty variable name")
		}
	}
	return nil
}

func validateSandboxMount(projectRoot string, index int, mount SandboxMount) error {
	name := fmt.Sprintf("sandbox.mounts[%d]", index)
	if mount.Host == "" {
		return fmt.Errorf("%s.host is required", name)
	}
	if mount.Target == "" {
		return fmt.Errorf("%s.target is required", name)
	}
	if mount.Mode != sandboxMountModeRO && mount.Mode != sandboxMountModeRW {
		return fmt.Errorf("%s.mode %q is invalid; allowed: ro, rw", name, mount.Mode)
	}
	if mount.Mode == sandboxMountModeRW && !filepath.IsAbs(mount.Host) {
		clean := filepath.Clean(mount.Host)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s.host %q must not traverse outside repository root for writable mounts", name, mount.Host)
		}
	}

	hostPath := mount.Host
	if !filepath.IsAbs(hostPath) {
		hostPath = filepath.Join(projectRoot, hostPath)
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		if mount.Optional.Value && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s.host %q does not exist", name, mount.Host)
		}
		return fmt.Errorf("%s.host %q: %w", name, mount.Host, err)
	}
	if info == nil {
		return fmt.Errorf("%s.host %q does not exist", name, mount.Host)
	}
	if mount.Mode != sandboxMountModeRW || filepath.IsAbs(mount.Host) {
		return nil
	}

	realRoot, realPath, err := resolveExistingProjectPath(projectRoot, hostPath)
	if err != nil {
		return fmt.Errorf("%s.host %q: %w", name, mount.Host, err)
	}
	if err := validateResolvedUnderRoot(realRoot, realPath); err != nil {
		return fmt.Errorf("%s.host %q must not escape repository root for writable mounts", name, mount.Host)
	}
	return nil
}

func resolveExistingProjectPath(projectRoot, path string) (string, string, error) {
	realRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	return realRoot, realPath, nil
}

func validateResolvedUnderRoot(realRoot, realPath string) error {
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return fmt.Errorf("resolve path relative to repository root: %w", err)
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path must not escape repository root")
	}
	return nil
}

func validateWorkflow(workflow Workflow, agents map[string]Agent) error {
	if err := validateWorkflowShape(workflow); err != nil {
		return err
	}

	declaredPairs := resultPairSet{}
	for stepName, step := range workflow.Steps {
		stepPairs, err := validateStep(stepName, step, workflow.Steps, agents)
		if err != nil {
			return err
		}
		for pair := range stepPairs {
			declaredPairs[pair] = struct{}{}
		}
	}

	return validateRetries(workflow.Defaults.Retries, declaredPairs)
}

func validateWorkflowShape(workflow Workflow) error {
	if workflow.Name == "" {
		return errors.New("name is required")
	}
	if workflow.Start == "" {
		return errors.New("start is required")
	}
	if workflow.Execution.Mode != executionModeSequential {
		return fmt.Errorf("unsupported execution mode %q; allowed: %s", workflow.Execution.Mode, executionModeSequential)
	}
	if err := validateTaskContext(workflow.TaskContext); err != nil {
		return err
	}
	if err := validateVCSPolicy(workflow.VCS); err != nil {
		return err
	}
	if err := validateDefaults(workflow.Defaults); err != nil {
		return err
	}
	if len(workflow.Steps) == 0 {
		return errors.New("steps are required")
	}
	if _, ok := workflow.Steps[workflow.Start]; !ok {
		return fmt.Errorf("start step %q is not declared", workflow.Start)
	}
	return nil
}

func validateStep(stepName string, step Step, steps map[string]Step, agents map[string]Agent) (resultPairSet, error) {
	if err := validateStepKind(stepName, step, agents); err != nil {
		return nil, err
	}
	if len(step.AllowedResults) == 0 {
		return nil, fmt.Errorf("step %q allowed_results are required", stepName)
	}

	stepPairs, err := validateAllowedResults(stepName, step.AllowedResults)
	if err != nil {
		return nil, err
	}
	if err := validateTransitions(stepName, step.On, stepPairs, steps); err != nil {
		return nil, err
	}

	return stepPairs, nil
}

func validateStepKind(stepName string, step Step, agents map[string]Agent) error {
	kind := step.EffectiveKind()
	if step.Script.Body != "" {
		return fmt.Errorf("step %q script.body is not supported in v1", stepName)
	}
	switch kind {
	case StepKindAgent:
		if step.Agent == "" {
			return fmt.Errorf("step %q agent is required", stepName)
		}
		if len(step.Command.Argv) > 0 {
			return fmt.Errorf("step %q kind agent must not set command", stepName)
		}
		if step.Script.Path != "" || len(step.Script.Args) > 0 {
			return fmt.Errorf("step %q kind agent must not set script", stepName)
		}
		if _, ok := agents[step.Agent]; !ok {
			return fmt.Errorf("step %q references missing agent %q", stepName, step.Agent)
		}
	case StepKindCommand:
		if step.Agent != "" {
			return fmt.Errorf("step %q kind command must not set agent", stepName)
		}
		if len(step.Command.Argv) == 0 {
			return fmt.Errorf("step %q command.argv must declare at least one argument", stepName)
		}
		for i, arg := range step.Command.Argv {
			if arg == "" {
				return fmt.Errorf("step %q command.argv[%d] is empty", stepName, i)
			}
		}
		if step.Script.Path != "" || len(step.Script.Args) > 0 {
			return fmt.Errorf("step %q kind command must not set script", stepName)
		}
	case StepKindScript:
		if step.Agent != "" {
			return fmt.Errorf("step %q kind script must not set agent", stepName)
		}
		if len(step.Command.Argv) > 0 {
			return fmt.Errorf("step %q kind script must not set command", stepName)
		}
		if step.Script.Path == "" {
			return fmt.Errorf("step %q script.path is required", stepName)
		}
		if err := validateRepoRelativePath("step "+stepName+" script.path", step.Script.Path); err != nil {
			return err
		}
		for i, arg := range step.Script.Args {
			if arg == "" {
				return fmt.Errorf("step %q script.args[%d] is empty", stepName, i)
			}
		}
	default:
		return fmt.Errorf("step %q has unsupported kind %q; allowed: agent, command, script", stepName, step.Kind)
	}
	if step.CWD != "" {
		if err := validateRepoRelativePath("step "+stepName+" cwd", step.CWD); err != nil {
			return err
		}
	}
	return nil
}

func validateRepoRelativePath(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if filepath.IsAbs(value) {
		return fmt.Errorf("%s %q must be repo-relative", name, value)
	}
	clean := filepath.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q must be clean and stay under repository root", name, value)
	}
	return nil
}

func validateAllowedResults(stepName string, allowedResults map[string][]string) (resultPairSet, error) {
	stepPairs := resultPairSet{}
	for status, results := range allowedResults {
		if _, ok := allowedReportStatuses[status]; !ok {
			return nil, fmt.Errorf("step %q has invalid status %q; allowed: %s", stepName, status, formatStringSet(allowedReportStatuses))
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("step %q status %q must declare at least one result", stepName, status)
		}
		for _, result := range results {
			if result == "" {
				return nil, fmt.Errorf("step %q status %q has empty result", stepName, status)
			}
			stepPairs[resultPairKey(status, result)] = struct{}{}
		}
	}
	return stepPairs, nil
}

func validateTransitions(stepName string, transitions map[string]string, stepPairs resultPairSet, steps map[string]Step) error {
	if len(transitions) == 0 {
		return fmt.Errorf("step %q on transitions are required", stepName)
	}
	for pair, target := range transitions {
		if _, ok := stepPairs[pair]; !ok {
			return fmt.Errorf("step %q transition %q is not declared in allowed_results; allowed pairs: %s", stepName, pair, formatStringSet(stepPairs))
		}
		_, stepTarget := steps[target]
		_, terminalTarget := allowedTerminalStates[target]
		if !stepTarget && !terminalTarget {
			return fmt.Errorf("step %q transition %q targets unknown step or terminal state %q", stepName, pair, target)
		}
	}
	for pair := range stepPairs {
		if _, ok := transitions[pair]; !ok {
			return fmt.Errorf("step %q allowed result %q has no deterministic on transition; allowed pairs: %s", stepName, pair, formatStringSet(stepPairs))
		}
	}
	return nil
}

func validateRetries(retries map[string]int, declaredPairs resultPairSet) error {
	for key, retryCount := range retries {
		if retryCount < 0 {
			return fmt.Errorf("retry key %q has negative retry count %d; retry counts must be >= 0", key, retryCount)
		}
		if _, ok := declaredPairs[key]; !ok {
			return fmt.Errorf("retry key %q is not declared in workflow allowed_results; allowed pairs: %s", key, formatStringSet(declaredPairs))
		}
	}

	return nil
}

func validateTaskContext(taskContext TaskContext) error {
	if _, ok := allowedTaskContextBeads[taskContext.Beads]; !ok {
		return fmt.Errorf("task_context.beads %q is invalid; allowed: %s", taskContext.Beads, formatStringSet(allowedTaskContextBeads))
	}
	if !taskContext.MarkdownFallback.Set {
		return errors.New("task_context.markdown_fallback is required")
	}
	return nil
}

func validateVCSPolicy(policy VCSPolicy) error {
	if value := policy.DirtyStart; value != "" {
		if _, ok := allowedDirtyStartPolicies[value]; !ok {
			return fmt.Errorf("vcs.dirty_start %q is invalid; allowed: %s", value, formatStringSet(allowedDirtyStartPolicies))
		}
	}
	if value := policy.NoVCS; value != "" {
		if _, ok := allowedNoVCSPolicies[value]; !ok {
			return fmt.Errorf("vcs.no_vcs %q is invalid; allowed: %s", value, formatStringSet(allowedNoVCSPolicies))
		}
	}
	return nil
}

func resultPairKey(status, result string) string {
	return status + "/" + result
}

func validateDefaults(defaults Defaults) error {
	if err := validatePositiveDuration("defaults.timeout", defaults.Timeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("defaults.report_exit_grace", defaults.ReportExitGrace); err != nil {
		return err
	}
	if defaults.Retries == nil {
		return errors.New("defaults.retries is required")
	}
	return nil
}

func validatePositiveDuration(name string, value Duration) error {
	if !value.Set {
		return fmt.Errorf("%s is required", name)
	}
	if value.Duration <= 0 {
		return fmt.Errorf("%s must be > 0, got %s", name, value.Duration)
	}
	return nil
}

func validateLoopCapsConfig(name string, caps LoopCapsConfig) error {
	if caps.Soft.Set && caps.Soft.Value < 0 {
		return fmt.Errorf("%s.soft must be >= 0, got %d", name, caps.Soft.Value)
	}
	if caps.Hard.Set && caps.Hard.Value < 0 {
		return fmt.Errorf("%s.hard must be >= 0, got %d", name, caps.Hard.Value)
	}
	return nil
}

func validateEffectiveLoopCaps(name string, caps EffectiveLoopCaps) error {
	if !caps.Enabled {
		return nil
	}
	if caps.Soft <= 0 {
		return fmt.Errorf("%s.soft must be > 0 when enabled, got %d", name, caps.Soft)
	}
	if caps.Hard <= 0 {
		return fmt.Errorf("%s.hard must be > 0 when enabled, got %d", name, caps.Hard)
	}
	if caps.Hard <= caps.Soft {
		return fmt.Errorf("%s.hard must be greater than soft when enabled, got hard=%d soft=%d", name, caps.Hard, caps.Soft)
	}
	return nil
}

func resolveLoopCaps(defaults, workflow LoopCapsConfig) EffectiveLoopCaps {
	effective := EffectiveLoopCaps{
		Enabled: defaultLoopCapsEnabled,
		Soft:    defaultLoopCapsSoft,
		Hard:    defaultLoopCapsHard,
	}
	effective = applyLoopCapsConfig(effective, defaults)
	effective = applyLoopCapsConfig(effective, workflow)
	return effective
}

func applyLoopCapsConfig(effective EffectiveLoopCaps, caps LoopCapsConfig) EffectiveLoopCaps {
	if caps.Enabled.Set {
		effective.Enabled = caps.Enabled.Value
	}
	if caps.Soft.Set {
		effective.Soft = caps.Soft.Value
	}
	if caps.Hard.Set {
		effective.Hard = caps.Hard.Value
	}
	return effective
}

func workflowAgentRefs(workflow Workflow, agentPaths map[string]string) map[string]AgentRef {
	refs := map[string]AgentRef{}
	for _, step := range workflow.Steps {
		if step.EffectiveKind() != StepKindAgent {
			continue
		}
		refs[step.Agent] = AgentRef{
			ID:   step.Agent,
			Path: agentPaths[step.Agent],
		}
	}
	return refs
}
