package config

import (
	"errors"
	"fmt"
)

func validateProjectConfig(cfg ProjectConfig) error {
	if cfg.Version != schemaVersion {
		return fmt.Errorf("config version = %d, want %d", cfg.Version, schemaVersion)
	}
	if len(cfg.Workflows) == 0 {
		return errors.New("config must declare at least one workflow")
	}
	if len(cfg.Agents) == 0 {
		return errors.New("config must declare at least one agent")
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
	if step.Agent == "" {
		return nil, fmt.Errorf("step %q agent is required", stepName)
	}
	if _, ok := agents[step.Agent]; !ok {
		return nil, fmt.Errorf("step %q references missing agent %q", stepName, step.Agent)
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

func workflowAgentRefs(workflow Workflow, agentPaths map[string]string) map[string]AgentRef {
	refs := map[string]AgentRef{}
	for _, step := range workflow.Steps {
		refs[step.Agent] = AgentRef{
			ID:   step.Agent,
			Path: agentPaths[step.Agent],
		}
	}
	return refs
}
