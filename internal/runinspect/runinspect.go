// Package runinspect renders read-only run inspection output.
package runinspect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const notAvailable = "not available"

// Options describes a read-only run inspection request.
type Options struct {
	Root   string
	RunID  string
	Stdout io.Writer
}

// Status prints persisted run state and currently inspectable artifacts.
func Status(_ context.Context, opts Options) error {
	inspection, err := inspect(opts)
	if err != nil {
		return err
	}
	renderStatus(inspection.stdout, inspection.run, inspection.decision)
	return nil
}

// Next prints the workflow-selected next action without launching a worker.
func Next(_ context.Context, opts Options) error {
	inspection, err := inspect(opts)
	if err != nil {
		return err
	}
	renderNext(inspection.stdout, inspection.workflow, inspection.run, inspection.decision)
	return nil
}

type inspection struct {
	stdout   io.Writer
	workflow config.Workflow
	run      *runstore.Run
	decision workflow.Decision
}

func inspect(opts Options) (inspection, error) {
	workflowConfig, run, err := loadProjectRun(opts)
	if err != nil {
		return inspection{}, err
	}
	decision, err := evaluate(workflowConfig, run)
	if err != nil {
		return inspection{}, err
	}
	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}
	return inspection{
		stdout:   out,
		workflow: workflowConfig,
		run:      run,
		decision: decision,
	}, nil
}

func loadProjectRun(opts Options) (config.Workflow, *runstore.Run, error) {
	if opts.Root == "" {
		return config.Workflow{}, nil, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return config.Workflow{}, nil, errors.New("run id is required")
	}
	project, err := config.Load(opts.Root)
	if err != nil {
		return config.Workflow{}, nil, fmt.Errorf("load project config: %w", err)
	}
	store, err := runstore.Open(project.Root)
	if err != nil {
		return config.Workflow{}, nil, err
	}
	run, err := store.Load(opts.RunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Workflow{}, nil, fmt.Errorf("run %q not found", opts.RunID)
		}
		return config.Workflow{}, nil, err
	}
	workflowConfig, ok := project.Workflows[run.Status.Workflow]
	if !ok {
		return config.Workflow{}, nil, fmt.Errorf("workflow %q from run %q is not configured", run.Status.Workflow, run.ID)
	}
	return workflowConfig, run, nil
}

func evaluate(workflowConfig config.Workflow, run *runstore.Run) (workflow.Decision, error) {
	state := workflow.RunState{
		Status: run.Status.State,
	}
	decision, err := workflow.Evaluate(workflowConfig, state)
	if err != nil {
		return workflow.Decision{}, fmt.Errorf("evaluate run %q: %w", run.ID, err)
	}
	return decision, nil
}

func renderStatus(w io.Writer, run *runstore.Run, decision workflow.Decision) {
	printRunHeader(w, run)
	_, _ = fmt.Fprintf(w, "workflow: %s\n", run.Status.Workflow)
	_, _ = fmt.Fprintf(w, "created_at: %s\n", formatTime(run.Status.CreatedAt))
	_, _ = fmt.Fprintf(w, "updated_at: %s\n", formatTime(run.Status.UpdatedAt))
	_, _ = fmt.Fprintf(w, "last_sequence: %d\n", run.Status.LastSequence)
	_, _ = fmt.Fprintf(w, "selected_step: %s\n", selectedStep(decision))
	_, _ = fmt.Fprintf(w, "active_attempt: %s\n", notAvailable)
	_, _ = fmt.Fprintf(w, "terminal_reason: %s\n", terminalReason(decision))
	reports := reportPaths(run.Status.Artifacts)
	printReportPaths(w, reports)
	printArtifacts(w, run.Status.Artifacts)
	printTerminalHumanState(w, run, reports)
}

func renderNext(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	printRunHeader(w, run)
	_, _ = fmt.Fprintf(w, "decision: %s\n", decision.Kind)
	switch decision.Kind {
	case workflow.DecisionSelectStep:
		step := workflowConfig.Steps[decision.Step]
		_, _ = fmt.Fprintf(w, "selected_step: %s\n", decision.Step)
		_, _ = fmt.Fprintf(w, "agent: %s\n", step.Agent)
		_, _ = fmt.Fprintln(w, "launch: not launched")
	case workflow.DecisionTerminal:
		_, _ = fmt.Fprintf(w, "terminal_reason: %s\n", terminalReason(decision))
		_, _ = fmt.Fprintln(w, "launch: no worker should launch")
		reports := reportPaths(run.Status.Artifacts)
		printReportPaths(w, reports)
		printTerminalHumanState(w, run, reports)
	}
}

func printRunHeader(w io.Writer, run *runstore.Run) {
	_, _ = fmt.Fprintf(w, "run: %s\n", run.ID)
	_, _ = fmt.Fprintf(w, "state: %s\n", run.Status.State)
}

func selectedStep(decision workflow.Decision) string {
	if decision.Kind == workflow.DecisionSelectStep {
		return decision.Step
	}
	return "none"
}

func terminalReason(decision workflow.Decision) string {
	if decision.Kind != workflow.DecisionTerminal {
		return "none"
	}
	return decision.RunStatus
}

func printReportPaths(w io.Writer, reports []string) {
	_, _ = fmt.Fprintln(w, "recent_reports:")
	if len(reports) == 0 {
		_, _ = fmt.Fprintln(w, "  none")
		return
	}
	for _, path := range reports {
		_, _ = fmt.Fprintf(w, "  - %s\n", path)
	}
}

func reportPaths(refs []runstore.ArtifactRef) []string {
	var paths []string
	for _, ref := range refs {
		if ref.Kind == runstore.KindReport {
			paths = append(paths, ref.Path)
		}
	}
	return paths
}

func printArtifacts(w io.Writer, refs []runstore.ArtifactRef) {
	_, _ = fmt.Fprintln(w, "artifacts:")
	if len(refs) == 0 {
		_, _ = fmt.Fprintln(w, "  none")
		return
	}
	sorted := slices.Clone(refs)
	slices.SortFunc(sorted, compareArtifactRefs)
	for _, ref := range sorted {
		_, _ = fmt.Fprintf(w, "  - %s: %s\n", ref.Kind, ref.Path)
	}
}

func compareArtifactRefs(a, b runstore.ArtifactRef) int {
	if bySequence := cmp.Compare(a.EventSequence, b.EventSequence); bySequence != 0 {
		return bySequence
	}
	if byKind := cmp.Compare(a.Kind, b.Kind); byKind != 0 {
		return byKind
	}
	return cmp.Compare(a.Path, b.Path)
}

func printHumanReviewPaths(w io.Writer, run *runstore.Run) {
	_, _ = fmt.Fprintf(w, "summary_context: %s\n", filepath.ToSlash(run.Path))
	_, _ = fmt.Fprintf(w, "final_summaries: %s\n", filepath.ToSlash(filepath.Join(run.Path, "summaries")))
}

func printTerminalHumanState(w io.Writer, run *runstore.Run, reports []string) {
	if !isTerminalHumanState(run.Status.State) {
		return
	}
	printHumanStateDetail(w, run.Status.State, len(reports) > 0)
	printHumanReviewPaths(w, run)
}

func printHumanStateDetail(w io.Writer, state string, hasReport bool) {
	switch state {
	case workflow.RunStatusBlockedForHuman:
		if hasReport {
			_, _ = fmt.Fprintln(w, "human_attention: blocked_for_human; see recent_reports")
			return
		}
		_, _ = fmt.Fprintf(w, "human_attention: %s; report details %s\n", state, notAvailable)
	case workflow.RunStatusReadyForHuman:
		_, _ = fmt.Fprintln(w, "review_state: ready_for_human; no more workers should launch")
	}
}

func isTerminalHumanState(state string) bool {
	return state == workflow.RunStatusReadyForHuman || state == workflow.RunStatusBlockedForHuman
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}
