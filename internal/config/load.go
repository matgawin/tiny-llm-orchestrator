package config

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Load reads .orc/config.yaml, all referenced workflows, and all referenced
// agent descriptors from projectRoot.
func Load(projectRoot string) (*Project, error) {
	if projectRoot == "" {
		return nil, errors.New("project root is required")
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
	if err := validateProjectConfig(cfg); err != nil {
		return nil, err
	}

	agents, err := loadAgents(orcDir, realOrcDir, cfg.Agents)
	if err != nil {
		return nil, err
	}
	workflows, err := loadWorkflows(orcDir, realOrcDir, cfg.Workflows, cfg.Agents, agents)
	if err != nil {
		return nil, err
	}

	return &Project{
		Root:       absRoot,
		OrcDir:     orcDir,
		RealOrcDir: realOrcDir,
		Config:     cfg,
		Workflows:  workflows,
		Agents:     agents,
	}, nil
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
			return nil, fmt.Errorf("agent map key %q does not match descriptor id %q", id, agent.ID)
		}
		agents[id] = agent
	}
	return agents, nil
}

func loadWorkflows(orcDir, realOrcDir string, workflowPaths, agentPaths map[string]string, agents map[string]Agent) (map[string]Workflow, error) {
	workflows := make(map[string]Workflow, len(workflowPaths))
	for name, relPath := range workflowPaths {
		path, err := resolveConfigRef(orcDir, realOrcDir, "workflow", name, relPath)
		if err != nil {
			return nil, err
		}
		workflow, err := loadWorkflow(realOrcDir, path)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
		if workflow.Name != name {
			return nil, fmt.Errorf("workflow map key %q does not match workflow name %q", name, workflow.Name)
		}
		if err := validateWorkflow(workflow, agents); err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
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
	var workflow Workflow
	if err := readYAML(realOrcDir, path, &workflow); err != nil {
		return Workflow{}, err
	}
	workflow.SourcePath = path
	return workflow, nil
}
