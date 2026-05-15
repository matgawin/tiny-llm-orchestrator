package config

import (
	"fmt"
	"path/filepath"

	"tiny-llm-orchestrator/orc/internal/stableerr"

	"github.com/goccy/go-yaml"
)

// Load reads .orc/config.yaml, all referenced runtimes, all referenced
// workflows, and all referenced agent descriptors from projectRoot.
func Load(projectRoot string) (*Project, error) {
	if projectRoot == "" {
		return nil, stableerr.New("project root is required")
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	orcDir := filepath.Join(absRoot, configDirName)
	realOrcDir, err := filepath.EvalSymlinks(orcDir)
	if err != nil {
		return nil, fmt.Errorf("resolve .orc directory: %w", err)
	}

	configPath := filepath.Join(orcDir, configFileName)
	var cfg ProjectConfig
	if err := readYAML(realOrcDir, configPath, &cfg); err != nil {
		return nil, err
	}
	if err := validateProjectConfig(absRoot, &cfg); err != nil {
		return nil, err
	}

	runtimes, err := loadRuntimes(orcDir, realOrcDir, cfg.Runtimes)
	if err != nil {
		return nil, err
	}
	agents, err := loadAgents(orcDir, realOrcDir, cfg.Agents)
	if err != nil {
		return nil, err
	}
	workflows, err := loadWorkflows(orcDir, realOrcDir, cfg.Defaults.LoopCaps, cfg.Workflows, cfg.Agents, agents, runtimes)
	if err != nil {
		return nil, err
	}
	if err := validateSelectedRuntimeSandboxRequirementConflicts(cfg.Sandbox, workflows, runtimes); err != nil {
		return nil, err
	}

	return &Project{
		Root:       absRoot,
		OrcDir:     orcDir,
		RealOrcDir: realOrcDir,
		Config:     cfg,
		Workflows:  workflows,
		Agents:     agents,
		Runtimes:   runtimes,
	}, nil
}

func loadRuntimes(orcDir, realOrcDir string, refs map[string]string) (map[string]Runtime, error) {
	runtimes := make(map[string]Runtime, len(refs))
	for id, relPath := range refs {
		path, err := resolveConfigRef(orcDir, realOrcDir, "runtime", id, relPath)
		if err != nil {
			return nil, err
		}
		runtime, err := loadRuntime(realOrcDir, path)
		if err != nil {
			return nil, fmt.Errorf("runtime %q file %q: %w", id, relPath, err)
		}
		if runtime.ID != id {
			return nil, stableerr.Errorf("runtime %q file %q: id %q does not match runtime map key", id, relPath, runtime.ID)
		}
		runtimes[id] = runtime
	}
	return runtimes, nil
}

func loadAgents(orcDir, realOrcDir string, refs map[string]string) (map[string]Agent, error) {
	agents := make(map[string]Agent, len(refs))
	for id, relPath := range refs {
		path, err := resolveConfigRef(orcDir, realOrcDir, "agent", id, relPath)
		if err != nil {
			return nil, err
		}
		agent, err := loadAgent(realOrcDir, path)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", id, err)
		}
		if agent.ID != id {
			return nil, stableerr.Errorf("agent map key %q does not match descriptor id %q", id, agent.ID)
		}
		agents[id] = agent
	}
	return agents, nil
}

func loadWorkflows(orcDir, realOrcDir string, defaults LoopCapsConfig, workflowRefs map[string]WorkflowReference, agentPaths map[string]string, agents map[string]Agent, runtimes map[string]Runtime) (map[string]Workflow, error) {
	workflows := make(map[string]Workflow, len(workflowRefs))
	for name, ref := range workflowRefs {
		path, err := resolveConfigRef(orcDir, realOrcDir, "workflow", name, ref.Path)
		if err != nil {
			return nil, err
		}
		workflow, err := loadWorkflow(realOrcDir, path)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
		if workflow.Name != name {
			return nil, stableerr.Errorf("workflow map key %q does not match workflow name %q", name, workflow.Name)
		}
		if err := validateWorkflow(workflow, agents, runtimes); err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
		workflow.LoopCaps = resolveLoopCaps(defaults, ref.LoopCaps)
		workflow.ReferencedAgents = workflowAgentRefs(workflow, agentPaths)
		workflows[name] = workflow
	}
	return workflows, nil
}

func resolveConfigRef(orcDir, realOrcDir, kind, id, relPath string) (string, error) {
	path, err := resolveOrcRelativePath(orcDir, realOrcDir, relPath)
	if err != nil {
		return "", fmt.Errorf("%s %q path %q: %w", kind, id, relPath, err)
	}
	return path, nil
}

func loadWorkflow(realOrcDir, path string) (Workflow, error) {
	content, err := readConfigFile(realOrcDir, path)
	if err != nil {
		return Workflow{}, err
	}
	workflow, err := decodeWorkflow(content, path)
	if err != nil {
		return Workflow{}, err
	}
	workflow.SourcePath = path
	return workflow, nil
}

type workflowConfigYAML struct {
	Name        string        `yaml:"name"`
	Start       string        `yaml:"start"`
	Execution   Execution     `yaml:"execution"`
	TaskContext TaskContext   `yaml:"task_context"`
	VCS         VCSPolicy     `yaml:"vcs"`
	Defaults    Defaults      `yaml:"defaults"`
	Steps       yaml.MapSlice `yaml:"steps"`
}

func decodeWorkflow(content []byte, path string) (Workflow, error) {
	var raw workflowConfigYAML
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return Workflow{}, fmt.Errorf("parse %s: %w", path, err)
	}
	steps, stepOrder, err := decodeWorkflowSteps(raw.Steps, path)
	if err != nil {
		return Workflow{}, err
	}
	return Workflow{
		Name:        raw.Name,
		Start:       raw.Start,
		Execution:   raw.Execution,
		TaskContext: raw.TaskContext,
		VCS:         raw.VCS,
		Defaults:    raw.Defaults,
		Steps:       steps,
		StepOrder:   stepOrder,
	}, nil
}

func decodeWorkflowSteps(rawSteps yaml.MapSlice, path string) (map[string]Step, []string, error) {
	steps := make(map[string]Step, len(rawSteps))
	order := make([]string, 0, len(rawSteps))
	for _, item := range rawSteps {
		stepID, ok := item.Key.(string)
		if !ok {
			continue
		}
		if _, exists := steps[stepID]; exists {
			return nil, nil, stableerr.Errorf("parse %s: duplicate step %q", path, stepID)
		}
		content, err := yaml.Marshal(item.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s step %q: %w", path, stepID, err)
		}
		var step Step
		if err := yaml.Unmarshal(content, &step); err != nil {
			return nil, nil, fmt.Errorf("parse %s step %q: %w", path, stepID, err)
		}
		steps[stepID] = step
		order = append(order, stepID)
	}
	return steps, order, nil
}
