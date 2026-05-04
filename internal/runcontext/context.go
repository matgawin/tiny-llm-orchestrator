package runcontext

import (
	"fmt"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
)

// Context is the shared project/run/workflow load result.
type Context struct {
	Project  *config.Project
	Workflow config.Workflow
	Store    *runstore.Store
	Run      *runstore.Run
}

// Load loads project config, run-store state, and the run's workflow config.
func Load(root, runID string) (Context, error) {
	project, err := config.Load(root)
	if err != nil {
		return Context{}, fmt.Errorf("load project config: %w", err)
	}
	store, err := runstore.Open(project.Root)
	if err != nil {
		return Context{}, err
	}
	run, err := store.Load(runID)
	if err != nil {
		return Context{}, err
	}
	workflowConfig, ok := project.Workflows[run.Status.Workflow]
	if !ok {
		return Context{}, fmt.Errorf("workflow %q from run %q is not configured", run.Status.Workflow, run.ID)
	}
	return Context{Project: project, Workflow: workflowConfig, Store: store, Run: run}, nil
}
