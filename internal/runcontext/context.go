package runcontext

import (
	"context"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// Context is the shared project/run/workflow load result.
type Context struct {
	Project  *config.Project
	Workflow config.Workflow
	Store    *runstore.Store
	Run      *runstore.Run

	ConfigSnapshotVersion    int
	ConfigSnapshotVersionDir string
}

// Load loads run-store state and the run's pinned config snapshot.
func Load(root, runID string) (Context, error) {
	return LoadContext(context.Background(), root, runID)
}

// LoadContext loads run-store state and the run's pinned config snapshot unless ctx is canceled.
func LoadContext(ctx context.Context, root, runID string) (Context, error) {
	if ctx == nil {
		return Context{}, stableerr.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	store, err := runstore.Open(root)
	if err != nil {
		return Context{}, err
	}
	run, err := store.LoadContext(ctx, runID)
	if err != nil {
		return Context{}, err
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	snapshot, err := configsnapshot.LoadCurrent(run)
	if err != nil {
		return Context{}, err
	}
	project := snapshot.Project
	workflowConfig, ok := project.Workflows[run.Status.Workflow]
	if !ok {
		return Context{}, stableerr.Errorf("workflow %q from run %q is not configured", run.Status.Workflow, run.ID)
	}
	return Context{
		Project:                  project,
		Workflow:                 workflowConfig,
		Store:                    store,
		Run:                      run,
		ConfigSnapshotVersion:    snapshot.Version,
		ConfigSnapshotVersionDir: snapshot.VersionDir,
	}, nil
}
