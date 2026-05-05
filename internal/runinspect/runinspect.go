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
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	notAvailable         = "not available"
	none                 = "none"
	pendingWorkerOutcome = "pending_worker_outcome"
)

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
	loaded, err := runcontext.Load(opts.Root, opts.RunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Workflow{}, nil, fmt.Errorf("run %q not found", opts.RunID)
		}
		return config.Workflow{}, nil, err
	}
	return loaded.Workflow, loaded.Run, nil
}

func evaluate(workflowConfig config.Workflow, run *runstore.Run) (workflow.Decision, error) {
	if _, ok := runstore.PendingLauncherOutcome(run.Status); ok {
		return workflow.Decision{Kind: workflow.DecisionTerminal, RunStatus: pendingWorkerOutcome}, nil
	}
	decision, err := workflow.Evaluate(workflowConfig, runstate.WorkflowState(run.Status))
	if err != nil {
		return workflow.Decision{}, fmt.Errorf("evaluate run %q: %w", run.ID, err)
	}
	if _, ok := runstore.LatestReportedOutcome(run.Status); ok && decision.Kind == workflow.DecisionRetryStep {
		return workflow.Decision{Kind: workflow.DecisionTerminal, RunStatus: pendingWorkerOutcome}, nil
	}
	return decision, nil
}

func renderStatus(w io.Writer, run *runstore.Run, decision workflow.Decision) {
	printRunHeader(w, run, decision)
	_, _ = fmt.Fprintf(w, "workflow: %s\n", run.Status.Workflow)
	_, _ = fmt.Fprintf(w, "created_at: %s\n", formatTime(run.Status.CreatedAt))
	_, _ = fmt.Fprintf(w, "updated_at: %s\n", formatTime(run.Status.UpdatedAt))
	_, _ = fmt.Fprintf(w, "last_sequence: %d\n", run.Status.LastSequence)
	_, _ = fmt.Fprintf(w, "selected_step: %s\n", selectedStep(decision))
	_, _ = fmt.Fprintf(w, "active_attempt: %s\n", activeAttemptValue(run.Status.ActiveAttempt))
	_, _ = fmt.Fprintf(w, "terminal_reason: %s\n", terminalReason(decision))
	reports := reportPaths(run.Status.Artifacts)
	printReportPaths(w, reports)
	printArtifacts(w, run.Status.Artifacts)
	printTerminalHumanState(w, run, decision, reports)
}

func renderNext(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	printRunHeader(w, run, decision)
	_, _ = fmt.Fprintf(w, "decision: %s\n", decision.Kind)
	switch decision.Kind {
	case workflow.DecisionSelectStep:
		step := workflowConfig.Steps[decision.Step]
		_, _ = fmt.Fprintf(w, "selected_step: %s\n", decision.Step)
		_, _ = fmt.Fprintf(w, "agent: %s\n", step.Agent)
		_, _ = fmt.Fprintln(w, "launch: not launched")
	case workflow.DecisionWaitActiveAttempt:
		_, _ = fmt.Fprintf(w, "active_attempt: %s\n", activeAttemptValue(run.Status.ActiveAttempt))
		if run.Status.ActiveAttempt != nil {
			_, _ = fmt.Fprintf(w, "selected_step: %s\n", run.Status.ActiveAttempt.StepID)
			_, _ = fmt.Fprintf(w, "agent: %s\n", run.Status.ActiveAttempt.AgentID)
		}
		_, _ = fmt.Fprintln(w, "launch: already active")
	case workflow.DecisionTerminal:
		_, _ = fmt.Fprintf(w, "terminal_reason: %s\n", terminalReason(decision))
		_, _ = fmt.Fprintln(w, "launch: no worker should launch")
		reports := reportPaths(run.Status.Artifacts)
		printReportPaths(w, reports)
		printTerminalHumanState(w, run, decision, reports)
	}
}

func activeAttemptValue(attempt *runstore.Attempt) string {
	if attempt == nil {
		return none
	}
	return attempt.AttemptID
}

func printRunHeader(w io.Writer, run *runstore.Run, decision workflow.Decision) {
	_, _ = fmt.Fprintf(w, "run: %s\n", run.ID)
	_, _ = fmt.Fprintf(w, "state: %s\n", effectiveRunState(run, decision))
}

func effectiveRunState(run *runstore.Run, decision workflow.Decision) string {
	if decision.Kind == workflow.DecisionTerminal && isTerminalHumanState(decision.RunStatus) {
		return decision.RunStatus
	}
	return run.Status.State
}

func selectedStep(decision workflow.Decision) string {
	if decision.Kind == workflow.DecisionSelectStep {
		return decision.Step
	}
	return none
}

func terminalReason(decision workflow.Decision) string {
	if decision.Kind != workflow.DecisionTerminal {
		return none
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

func printTerminalHumanState(w io.Writer, run *runstore.Run, decision workflow.Decision, reports []string) {
	state := effectiveRunState(run, decision)
	if !isTerminalHumanState(state) {
		return
	}
	printHumanStateDetail(w, state, len(reports) > 0)
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
