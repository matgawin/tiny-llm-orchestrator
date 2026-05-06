// Package runinspect renders read-only run inspection output.
package runinspect

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/vcs"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const (
	notAvailable = "not available"
	none         = "none"

	outcomeReasonLimit  = 400
	summaryExcerptLimit = 4000
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
	renderStatus(inspection.stdout, inspection.workflow, inspection.run, inspection.decision)
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

// SummaryContext prints compact review context from persisted run state.
func SummaryContext(ctx context.Context, opts Options) error {
	inspection, err := inspect(opts)
	if err != nil {
		return err
	}
	return renderSummaryContext(ctx, inspection)
}

type inspection struct {
	stdout   io.Writer
	store    *runstore.Store
	workflow config.Workflow
	run      *runstore.Run
	decision workflow.Decision
}

type vcsSnapshotRef struct {
	ref      runstore.ArtifactRef
	snapshot vcs.Snapshot
}

type summaryInputs struct {
	reported             []runstore.Attempt
	reportFields         reportFields
	recordedFollowups    string
	latestPreRunVCS      *vcsSnapshotRef
	latestPostRunVCS     *vcsSnapshotRef
	vcsPaths             vcsPathSummary
	combinedChangedPaths []string
}

type reportFields struct {
	changedPaths []string
	commands     []string
	tests        []string
	risks        []string
	followups    []runstore.Followup
}

type vcsPathSummary struct {
	postRun       []string
	preExisting   []string
	newlyObserved []string
}

type headingLevel string

const (
	headingSubsection headingLevel = "###"
	headingNested     headingLevel = "####"
)

func inspect(opts Options) (inspection, error) {
	workflowConfig, store, run, err := loadProjectRun(opts)
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
		store:    store,
		workflow: workflowConfig,
		run:      run,
		decision: decision,
	}, nil
}

func loadProjectRun(opts Options) (config.Workflow, *runstore.Store, *runstore.Run, error) {
	if opts.Root == "" {
		return config.Workflow{}, nil, nil, errors.New("project root is required")
	}
	if opts.RunID == "" {
		return config.Workflow{}, nil, nil, errors.New("run id is required")
	}
	loaded, err := runcontext.Load(opts.Root, opts.RunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Workflow{}, nil, nil, fmt.Errorf("run %q not found", opts.RunID)
		}
		return config.Workflow{}, nil, nil, err
	}
	return loaded.Workflow, loaded.Store, loaded.Run, nil
}

func evaluate(workflowConfig config.Workflow, run *runstore.Run) (workflow.Decision, error) {
	decision, err := workflow.Evaluate(workflowConfig, runstate.WorkflowState(run.Status))
	if err != nil {
		return workflow.Decision{}, fmt.Errorf("evaluate run %q: %w", run.ID, err)
	}
	return decision, nil
}

func renderStatus(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	printRunHeader(w, run, decision)
	_, _ = fmt.Fprintf(w, "workflow: %s\n", run.Status.Workflow)
	_, _ = fmt.Fprintf(w, "created_at: %s\n", formatTime(run.Status.CreatedAt))
	_, _ = fmt.Fprintf(w, "updated_at: %s\n", formatTime(run.Status.UpdatedAt))
	_, _ = fmt.Fprintf(w, "last_sequence: %d\n", run.Status.LastSequence)
	_, _ = fmt.Fprintf(w, "selected_step: %s\n", selectedStep(decision))
	_, _ = fmt.Fprintf(w, "active_attempt: %s\n", activeAttemptValue(run.Status.ActiveAttempt))
	_, _ = fmt.Fprintf(w, "terminal_reason: %s\n", terminalReason(decision))
	printDecisionOutcomeDetails(w, workflowConfig, run, decision)
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
		printSelectedStep(w, workflowConfig, decision.Step)
		_, _ = fmt.Fprintln(w, "launch: not launched")
	case workflow.DecisionRetryStep:
		printSelectedStep(w, workflowConfig, decision.Step)
		printDecisionOutcomeDetails(w, workflowConfig, run, decision)
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
		printDecisionOutcomeDetails(w, workflowConfig, run, decision)
		_, _ = fmt.Fprintln(w, "launch: no worker should launch")
		reports := reportPaths(run.Status.Artifacts)
		printReportPaths(w, reports)
		printTerminalHumanState(w, run, decision, reports)
	}
}

func printSelectedStep(w io.Writer, workflowConfig config.Workflow, stepID string) {
	step := workflowConfig.Steps[stepID]
	_, _ = fmt.Fprintf(w, "selected_step: %s\n", stepID)
	_, _ = fmt.Fprintf(w, "agent: %s\n", step.Agent)
}

func renderSummaryContext(ctx context.Context, inspection inspection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	run := inspection.run
	w := inspection.stdout
	state := effectiveRunState(run, inspection.decision)
	_, _ = fmt.Fprintln(w, "# Summary Context")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "## Run")
	printStringField(w, "run_id", run.ID)
	printStringField(w, "workflow", run.Status.Workflow)
	printStringField(w, "persisted_state", run.Status.State)
	printStringField(w, "effective_state", state)
	printStringField(w, "terminal_state", summaryTerminalState(state))
	_, _ = fmt.Fprintf(w, "- last_sequence: %d\n", run.Status.LastSequence)
	_, _ = fmt.Fprintln(w)

	taskContext, err := readRequiredArtifactText(inspection.store, run, runstore.KindTaskContext)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "## Task Context")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, fencedMarkdown(excerptText(taskContext, summaryExcerptLimit)))
	_, _ = fmt.Fprintln(w)

	stepOrder := workflowStepOrder(inspection.workflow)
	_, _ = fmt.Fprintln(w, "## Workflow Path")
	for _, stepID := range stepOrder {
		step := inspection.workflow.Steps[stepID]
		_, _ = fmt.Fprintln(w, "- step:")
		printIndentedStringField(w, "id", stepID)
		printIndentedStringField(w, "agent", step.Agent)
		_, _ = fmt.Fprintf(w, "  attempts: %d\n", countStepAttempts(run.Status.Attempts, stepID))
	}
	printStringField(w, "decision", string(inspection.decision.Kind))
	if inspection.decision.Kind == workflow.DecisionSelectStep {
		printStringField(w, "selected_step", inspection.decision.Step)
	}
	if inspection.decision.Kind == workflow.DecisionTerminal {
		printStringField(w, "terminal_reason", terminalReason(inspection.decision))
	}
	if state == workflow.RunStatusBlockedForHuman {
		printStringField(w, "human_attention", workflow.RunStatusBlockedForHuman)
	}
	_, _ = fmt.Fprintln(w)

	inputs, err := buildSummaryInputs(inspection.store, run, stepOrder)
	if err != nil {
		return err
	}
	renderWorkerReports(w, inputs.reported)
	renderChanges(w, inputs.reportFields, inputs.vcsPaths, inputs.combinedChangedPaths)
	renderCommandsAndTests(w, inputs.reportFields)
	renderRisks(w, state, inputs.reportFields)
	renderFollowups(w, inputs.reportFields.followups, inputs.recordedFollowups)
	renderVCS(w, inputs.latestPreRunVCS, inputs.latestPostRunVCS)
	renderHumanReviewFocus(w, state, inputs.reportFields, strings.TrimSpace(inputs.recordedFollowups) != "", inputs.latestPostRunVCS != nil)
	return nil
}

func renderWorkerReports(w io.Writer, attempts []runstore.Attempt) {
	_, _ = fmt.Fprintln(w, "## Worker Reports")
	if len(attempts) == 0 {
		_, _ = fmt.Fprintln(w, "- none")
		_, _ = fmt.Fprintln(w)
		return
	}
	for _, attempt := range attempts {
		report := attempt.Report
		_, _ = fmt.Fprintln(w, "### Report")
		printStringField(w, "step", attempt.StepID)
		printStringField(w, "attempt", attempt.AttemptID)
		printStringField(w, "agent", attempt.AgentID)
		printStringField(w, "outcome_status", report.Status)
		printStringField(w, "outcome_result", report.Result)
		printStringField(w, "summary", report.Summary)
		printOptionalStringField(w, "report_artifact", attempt.ReportRef)
	}
	_, _ = fmt.Fprintln(w)
}

func renderChanges(w io.Writer, fields reportFields, paths vcsPathSummary, combinedChangedPaths []string) {
	_, _ = fmt.Fprintln(w, "## Changes")
	printStringList(w, "Worker Reported", fields.changedPaths)
	printStringList(w, "VCS Post-Run", paths.postRun)
	printStringList(w, "VCS Pre-Existing", paths.preExisting)
	printStringList(w, "VCS Newly Observed", paths.newlyObserved)
	printStringList(w, "Combined", combinedChangedPaths)
	_, _ = fmt.Fprintln(w)
}

func renderCommandsAndTests(w io.Writer, fields reportFields) {
	_, _ = fmt.Fprintln(w, "## Commands And Tests")
	printStringList(w, "Commands", fields.commands)
	printStringList(w, "Tests", fields.tests)
	_, _ = fmt.Fprintln(w)
}

func renderRisks(w io.Writer, state string, fields reportFields) {
	_, _ = fmt.Fprintln(w, "## Risks")
	if len(fields.risks) == 0 && state != workflow.RunStatusBlockedForHuman {
		_, _ = fmt.Fprintln(w, "- none")
		_, _ = fmt.Fprintln(w)
		return
	}
	for _, risk := range fields.risks {
		_, _ = fmt.Fprintf(w, "- %s\n", quoteScalar(risk))
	}
	if state == workflow.RunStatusBlockedForHuman {
		_, _ = fmt.Fprintln(w, "- blocked_for_human requires human attention")
	}
	_, _ = fmt.Fprintln(w)
}

func renderFollowups(w io.Writer, structured []runstore.Followup, recorded string) {
	_, _ = fmt.Fprintln(w, "## Follow-Ups")
	_, _ = fmt.Fprintln(w, "### Structured Report Follow-Ups")
	if len(structured) == 0 {
		_, _ = fmt.Fprintln(w, "- none")
	} else {
		for _, followup := range structured {
			printStringField(w, "title", followup.Title)
			if followup.Details != "" {
				printIndentedStringField(w, "details", followup.Details)
			}
		}
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "### Recorded Follow-Ups")
	if strings.TrimSpace(recorded) == "" {
		_, _ = fmt.Fprintln(w, "- none")
	} else {
		_, _ = fmt.Fprintln(w, fencedMarkdown(excerptText(recorded, summaryExcerptLimit)))
	}
	_, _ = fmt.Fprintln(w)
}

func renderVCS(w io.Writer, preRun, postRun *vcsSnapshotRef) {
	_, _ = fmt.Fprintln(w, "## VCS")
	printVCSSnapshot(w, "Pre-Run", preRun)
	printVCSSnapshot(w, "Post-Run", postRun)
	_, _ = fmt.Fprintln(w)
}

func renderHumanReviewFocus(w io.Writer, state string, fields reportFields, hasRecordedFollowups, postRunVCS bool) {
	_, _ = fmt.Fprintln(w, "## Suggested Human Review Focus")
	var bullets []string
	if state == workflow.RunStatusBlockedForHuman {
		bullets = append(bullets, "Resolve the blocked_for_human terminal state before treating the run as ready.")
	}
	if len(fields.risks) > 0 {
		bullets = append(bullets, "Review reported risks and caveats.")
	}
	if len(fields.tests) == 0 {
		bullets = append(bullets, "Confirm verification because no worker-reported tests are recorded.")
	}
	if len(fields.followups) > 0 || hasRecordedFollowups {
		bullets = append(bullets, "Review recorded follow-ups before final handoff.")
	}
	if !postRunVCS {
		bullets = append(bullets, "Post-run VCS snapshot is not recorded.")
	}
	if len(bullets) == 0 && state == workflow.RunStatusReadyForHuman {
		bullets = append(bullets, "Confirm the changes, tests, and VCS summary before final handoff.")
	}
	if len(bullets) == 0 {
		_, _ = fmt.Fprintln(w, "- none")
	} else {
		for _, bullet := range bullets {
			_, _ = fmt.Fprintf(w, "- %s\n", bullet)
		}
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
	if decision.Kind == workflow.DecisionSelectStep || decision.Kind == workflow.DecisionRetryStep {
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

func latestConsumableOutcome(run *runstore.Run) (runstore.Attempt, bool) {
	if run == nil {
		return runstore.Attempt{}, false
	}
	return runstore.LatestConsumableOutcome(run.Status)
}

func printDecisionOutcomeDetails(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	switch decision.Kind {
	case workflow.DecisionRetryStep:
		printRetryDecision(w, workflowConfig, run, decision)
	case workflow.DecisionTerminal:
		printTerminalOutcome(w, workflowConfig, run, decision)
	}
}

func printRetryDecision(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	outcome, hasOutcome := latestConsumableOutcome(run)
	if !hasOutcome {
		return
	}
	pair := outcomePair(outcome)
	_, _ = fmt.Fprintf(w, "retrying_after: %s\n", pair)
	_, _ = fmt.Fprintf(w, "retry_count: %s\n", retryCountText(workflowConfig, pair, decision.Retry.Counts[pair]))
	_, _ = fmt.Fprintf(w, "retry_source_attempt: %s\n", outcome.AttemptID)
}

func printTerminalOutcome(w io.Writer, workflowConfig config.Workflow, run *runstore.Run, decision workflow.Decision) {
	outcome, hasOutcome := latestConsumableOutcome(run)
	if !hasOutcome {
		return
	}
	pair := outcomePair(outcome)
	_, _ = fmt.Fprintf(w, "last_outcome: %s\n", pair)
	_, _ = fmt.Fprintf(w, "last_attempt: %s\n", outcome.AttemptID)
	if reason, ok := invalidReportReason(outcome); ok {
		_, _ = fmt.Fprintf(w, "invalid_report_reason: %s\n", reason)
	}
	if outcome.Status == "failed" {
		_, _ = fmt.Fprintf(w, "retry_exhausted: %s\n", pair)
		_, _ = fmt.Fprintf(w, "retry_count: %s\n", retryCountText(workflowConfig, pair, retryCountConsumedByRun(run, outcome, pair)))
	}
	_, _ = fmt.Fprintf(w, "transition: %s -> %s\n", pair, decision.RunStatus)
}

func outcomePair(outcome runstore.Attempt) string {
	return outcome.Status + "/" + outcome.Result
}

func retryCountConsumedByRun(run *runstore.Run, outcome runstore.Attempt, pair string) int {
	if run.Status.RetryLineage != nil && run.Status.RetryLineage.StepID == outcome.StepID {
		return run.Status.RetryLineage.Counts[pair]
	}
	return 0
}

func invalidReportReason(attempt runstore.Attempt) (string, bool) {
	if attempt.State != runstore.AttemptStateInvalidReport || attempt.Report == nil || attempt.Report.Summary == "" {
		return "", false
	}
	reason := strings.NewReplacer("\r", " ", "\n", " ").Replace(excerptText(attempt.Report.Summary, outcomeReasonLimit))
	return strings.TrimSpace(reason), true
}

func retryCountText(workflowConfig config.Workflow, pair string, consumed int) string {
	return fmt.Sprintf("%d/%d", consumed, workflowConfig.Defaults.Retries[pair])
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

func summaryTerminalState(state string) string {
	if isTerminalHumanState(state) || state == workflow.RunStatusCancelled {
		return state
	}
	return none
}

func workflowStepOrder(workflowConfig config.Workflow) []string {
	if len(workflowConfig.StepOrder) > 0 {
		return slices.Clone(workflowConfig.StepOrder)
	}
	steps := make([]string, 0, len(workflowConfig.Steps))
	for stepID := range workflowConfig.Steps {
		steps = append(steps, stepID)
	}
	slices.Sort(steps)
	return steps
}

func countStepAttempts(attempts []runstore.Attempt, stepID string) int {
	count := 0
	for _, attempt := range attempts {
		if attempt.StepID == stepID {
			count++
		}
	}
	return count
}

func reportedAttemptsByWorkflowOrder(attempts []runstore.Attempt, stepOrder []string) []runstore.Attempt {
	byStep := map[string][]runstore.Attempt{}
	for _, attempt := range attempts {
		if attempt.Report == nil {
			continue
		}
		byStep[attempt.StepID] = append(byStep[attempt.StepID], attempt)
	}
	var ordered []runstore.Attempt
	seen := map[string]struct{}{}
	for _, stepID := range stepOrder {
		ordered = append(ordered, byStep[stepID]...)
		seen[stepID] = struct{}{}
	}
	var rest []string
	for stepID := range byStep {
		if _, ok := seen[stepID]; !ok {
			rest = append(rest, stepID)
		}
	}
	slices.Sort(rest)
	for _, stepID := range rest {
		ordered = append(ordered, byStep[stepID]...)
	}
	return ordered
}

func buildSummaryInputs(store *runstore.Store, run *runstore.Run, stepOrder []string) (summaryInputs, error) {
	reported := reportedAttemptsByWorkflowOrder(run.Status.Attempts, stepOrder)
	vcsSnapshots, err := readVCSSnapshots(store, run)
	if err != nil {
		return summaryInputs{}, err
	}
	latestPreRunVCS := latestVCSSnapshot(vcsSnapshots, vcs.PhasePreRun)
	latestPostRunVCS := latestVCSSnapshot(vcsSnapshots, vcs.PhasePostRun)
	recordedFollowups, err := readOptionalArtifactText(store, run, runstore.KindFollowup)
	if err != nil {
		return summaryInputs{}, err
	}
	reportFields := collectReportFields(reported)
	vcsPaths := summarizeVCSPaths(latestPreRunVCS, latestPostRunVCS)
	return summaryInputs{
		reported:             reported,
		reportFields:         reportFields,
		recordedFollowups:    recordedFollowups,
		latestPreRunVCS:      latestPreRunVCS,
		latestPostRunVCS:     latestPostRunVCS,
		vcsPaths:             vcsPaths,
		combinedChangedPaths: uniqueStrings(append(slices.Clone(reportFields.changedPaths), vcsPaths.postRun...)),
	}, nil
}

func collectReportFields(attempts []runstore.Attempt) reportFields {
	var fields reportFields
	for _, attempt := range attempts {
		report := attempt.Report
		fields.changedPaths = append(fields.changedPaths, report.ChangedPaths...)
		fields.commands = append(fields.commands, report.Commands...)
		fields.tests = append(fields.tests, report.Tests...)
		fields.risks = append(fields.risks, report.Risks...)
		fields.followups = append(fields.followups, report.Followups...)
	}
	fields.changedPaths = uniqueStrings(fields.changedPaths)
	fields.commands = uniqueStrings(fields.commands)
	fields.tests = uniqueStrings(fields.tests)
	fields.risks = uniqueStrings(fields.risks)
	return fields
}

func readRequiredArtifactText(store *runstore.Store, run *runstore.Run, kind runstore.ArtifactKind) (string, error) {
	content, found, err := readArtifactText(store, run, kind)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("run %q has no %s artifact", run.ID, kind)
	}
	return content, nil
}

func readOptionalArtifactText(store *runstore.Store, run *runstore.Run, kind runstore.ArtifactKind) (string, error) {
	content, _, err := readArtifactText(store, run, kind)
	return content, err
}

func readArtifactText(store *runstore.Store, run *runstore.Run, kind runstore.ArtifactKind) (string, bool, error) {
	for _, ref := range run.Status.Artifacts {
		if ref.Kind != kind {
			continue
		}
		content, err := store.ReadArtifact(run.ID, ref)
		if err != nil {
			return "", false, fmt.Errorf("read %s artifact %s: %w", kind, ref.Path, err)
		}
		return string(content), true, nil
	}
	return "", false, nil
}

func readVCSSnapshots(store *runstore.Store, run *runstore.Run) ([]vcsSnapshotRef, error) {
	var snapshots []vcsSnapshotRef
	for _, ref := range run.Status.Artifacts {
		if !isVCSSnapshotRef(ref) {
			continue
		}
		content, err := store.ReadArtifact(run.ID, ref)
		if err != nil {
			return nil, fmt.Errorf("read VCS snapshot %s: %w", ref.Path, err)
		}
		var snapshot vcs.Snapshot
		if err := json.Unmarshal(content, &snapshot); err != nil {
			return nil, fmt.Errorf("decode VCS snapshot %s: %w", ref.Path, err)
		}
		snapshots = append(snapshots, vcsSnapshotRef{ref: ref, snapshot: snapshot})
	}
	return snapshots, nil
}

func isVCSSnapshotRef(ref runstore.ArtifactRef) bool {
	if ref.Kind != runstore.KindSnapshot {
		return false
	}
	return strings.HasSuffix(ref.Path, "-vcs-pre-run.json") || strings.HasSuffix(ref.Path, "-vcs-post-run.json")
}

func latestVCSSnapshot(snapshots []vcsSnapshotRef, phase string) *vcsSnapshotRef {
	var latest *vcsSnapshotRef
	for i := range snapshots {
		if snapshots[i].snapshot.Phase != phase {
			continue
		}
		if latest == nil || compareArtifactRefs(latest.ref, snapshots[i].ref) < 0 {
			latest = &snapshots[i]
		}
	}
	return latest
}

func printVCSSnapshot(w io.Writer, label string, snapshotRef *vcsSnapshotRef) {
	_, _ = fmt.Fprintf(w, "### %s\n", label)
	if snapshotRef == nil {
		_, _ = fmt.Fprintln(w, "- not recorded")
		return
	}
	snapshot := snapshotRef.snapshot
	printStringField(w, "artifact", snapshotRef.ref.Path)
	printStringField(w, "kind", snapshot.Kind)
	_, _ = fmt.Fprintf(w, "- dirty: %t\n", snapshot.Dirty)
	_, _ = fmt.Fprintln(w, "- summary:")
	_, _ = fmt.Fprintln(w, fencedMarkdown(excerptText(snapshot.Summary, summaryExcerptLimit)))
	printStringListAtLevel(w, "Changed Paths", snapshot.ChangedPaths, headingNested)
}

func summarizeVCSPaths(pre, post *vcsSnapshotRef) vcsPathSummary {
	if post == nil {
		return vcsPathSummary{}
	}
	postChangedPaths := uniqueStrings(post.snapshot.ChangedPaths)
	summary := vcsPathSummary{}
	if pre == nil {
		summary.postRun = postChangedPaths
		summary.newlyObserved = summary.postRun
		return summary
	}
	preChangedPaths := uniqueStrings(pre.snapshot.ChangedPaths)
	postPaths := map[string]struct{}{}
	for _, path := range postChangedPaths {
		postPaths[path] = struct{}{}
	}
	prePaths := map[string]struct{}{}
	for _, path := range preChangedPaths {
		prePaths[path] = struct{}{}
		if _, stillChanged := postPaths[path]; stillChanged {
			summary.preExisting = append(summary.preExisting, path)
		}
	}
	for _, path := range postChangedPaths {
		if _, existedBeforeRun := prePaths[path]; existedBeforeRun {
			continue
		}
		summary.newlyObserved = append(summary.newlyObserved, path)
	}
	summary.postRun = postChangedPaths
	return summary
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func printStringList(w io.Writer, label string, values []string) {
	printStringListAtLevel(w, label, values, headingSubsection)
}

func printStringListAtLevel(w io.Writer, label string, values []string, level headingLevel) {
	_, _ = fmt.Fprintf(w, "%s %s\n", level, label)
	if len(values) == 0 {
		_, _ = fmt.Fprintln(w, "- none")
		return
	}
	for _, value := range values {
		_, _ = fmt.Fprintf(w, "- %s\n", quoteScalar(value))
	}
}

func printStringField(w io.Writer, label, value string) {
	printStringFieldWithPrefix(w, "- ", label, value)
}

func printIndentedStringField(w io.Writer, label, value string) {
	printStringFieldWithPrefix(w, "  ", label, value)
}

func printStringFieldWithPrefix(w io.Writer, prefix, label, value string) {
	_, _ = fmt.Fprintf(w, "%s%s: %s\n", prefix, label, quoteScalar(value))
}

func printOptionalStringField(w io.Writer, label string, ref *runstore.ArtifactRef) {
	if ref == nil {
		_, _ = fmt.Fprintf(w, "- %s: %s\n", label, none)
		return
	}
	printStringField(w, label, ref.Path)
}

func quoteScalar(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return strconv.Quote(value)
	}
	return string(encoded)
}

func fencedMarkdown(content string) string {
	fence := markdownFence(content)
	return fence + "md\n" + content + "\n" + fence
}

func markdownFence(content string) string {
	longest := 0
	current := 0
	for _, r := range content {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	if longest < 3 {
		longest = 3
	} else {
		longest++
	}
	return strings.Repeat("`", longest)
}

func excerptText(content string, limit int) string {
	text := strings.TrimSpace(content)
	if text == "" {
		return "(empty)"
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n\n[excerpt truncated]"
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}
