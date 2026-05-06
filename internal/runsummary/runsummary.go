// Package runsummary records final human-review summaries for ready runs.
package runsummary

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// Options describes a final summary recording request.
type Options struct {
	Root  string
	RunID string
	File  string
}

// Result describes the persisted summary artifact.
type Result struct {
	RunID      string
	SummaryRef runstore.ArtifactRef
}

// Record copies an orchestrator-authored final summary into the run store.
func Record(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opts.Root == "" {
		return Result{}, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return Result{}, errors.New("run id is required")
	}
	if opts.File == "" {
		return Result{}, errors.New("summary file is required")
	}
	store, err := runstore.Open(opts.Root)
	if err != nil {
		return Result{}, err
	}
	content, err := os.ReadFile(opts.File) // #nosec G304 -- caller-provided summary file is the command input being snapshotted.
	if err != nil {
		return Result{}, fmt.Errorf("read summary file %q: %w", opts.File, err)
	}
	ref, err := store.WriteArtifactIfState(opts.RunID, workflow.RunStatusReadyForHuman, runstore.Artifact{
		Kind:    runstore.KindSummary,
		Name:    filepath.Base(opts.File),
		Content: content,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("run %q not found", opts.RunID)
		}
		var stateErr *runstore.StateMismatchError
		if errors.As(err, &stateErr) {
			return Result{}, fmt.Errorf("%w to record final summary; use summary-context for inspection", err)
		}
		return Result{}, err
	}
	return Result{RunID: opts.RunID, SummaryRef: ref}, nil
}
