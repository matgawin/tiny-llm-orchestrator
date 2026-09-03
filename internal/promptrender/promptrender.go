// Package promptrender renders role-specific prompts for worker attempts.
package promptrender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/attemptdeadline"
	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/configsnapshot"
	reportpkg "tiny-llm-orchestrator/orc/internal/report"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

// Options describes a prompt rendering request from a worker launcher.
type Options struct {
	Root      string
	RunID     string
	StepID    string
	AgentID   string
	AttemptID string
	// AllowUnselectedStep bypasses only the selected-step check for running runs.
	// Terminal runs, undeclared steps, and agent mismatches are still rejected.
	AllowUnselectedStep bool
	Time                time.Time
}

// Result describes the persisted prompt artifact.
type Result struct {
	// Ref is the canonical run-store artifact reference with a path relative to
	// the run directory.
	Ref runstore.ArtifactRef
	// Path is the absolute prompt artifact path for launcher convenience. It is
	// empty when the store did not return a committed artifact ref.
	Path string
	// Content is the rendered prompt bytes returned for callers that need to
	// inspect or pass the prompt without rereading the artifact.
	Content []byte
}

type renderContext struct {
	store    *runstore.Store
	run      *runstore.Run
	workflow config.Workflow
	step     config.Step
	agent    config.Agent
	attempt  runstore.Attempt
}

// Render loads run/config context, renders the worker prompt, and persists it
// as a prompt artifact through the Run Store.
func Render(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("render: %w", err)
	}

	if err := validateOptions(opts); err != nil {
		return Result{}, err
	}

	if opts.Time.IsZero() {
		opts.Time = time.Now().UTC()
	} else {
		opts.Time = opts.Time.UTC()
	}

	renderCtx, err := loadRenderContext(ctx, opts)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("render: %w", err)
	}

	content, err := renderPrompt(ctx, renderCtx, opts)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("render: %w", err)
	}

	ref, err := renderCtx.store.WriteArtifactContext(ctx, opts.RunID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    opts.StepID,
		Content: content,
		Time:    opts.Time,
	})

	result := resultFromArtifact(renderCtx.run.Path, ref, content)
	if err != nil {
		return result, fmt.Errorf("render: %w", err)
	}

	return result, nil
}

func resultFromArtifact(runPath string, ref runstore.ArtifactRef, content []byte) Result {
	if ref.Path == "" {
		return Result{Ref: ref, Content: content}
	}

	return Result{
		Ref:     ref,
		Path:    filepath.ToSlash(filepath.Join(runPath, filepath.FromSlash(ref.Path))),
		Content: content,
	}
}

func validateOptions(opts Options) error {
	switch {
	case opts.Root == "":
		return errors.New("project root is required")
	case opts.RunID == "":
		return errors.New("run id is required")
	case opts.StepID == "":
		return errors.New("step id is required")
	case opts.AgentID == "":
		return errors.New("agent id is required")
	case opts.AttemptID == "":
		return errors.New("attempt id is required")
	default:
		return nil
	}
}

func loadRenderContext(ctx context.Context, opts Options) (renderContext, error) {
	loaded, err := runcontext.LoadContext(ctx, opts.Root, opts.RunID)
	if err != nil {
		return renderContext{}, fmt.Errorf("load render context: %w", err)
	}

	decision, err := renderSelectionDecision(loaded.Workflow, loaded.Run)
	if err != nil {
		return renderContext{}, fmt.Errorf("evaluate run %q: %w", loaded.Run.ID, err)
	}

	if decision.Kind != workflow.DecisionSelectStep {
		return renderContext{}, fmt.Errorf("run %q has no selected runnable step; decision is %s", loaded.Run.ID, decision.Kind)
	}

	if !opts.AllowUnselectedStep && opts.StepID != decision.Step {
		return renderContext{}, fmt.Errorf("step %q is not selected for run %q; selected step is %q", opts.StepID, loaded.Run.ID, decision.Step)
	}

	step, ok := loaded.Workflow.Steps[opts.StepID]
	if !ok {
		return renderContext{}, fmt.Errorf("step %q is not declared in workflow %q", opts.StepID, loaded.Workflow.Name)
	}

	if step.Agent != opts.AgentID {
		return renderContext{}, fmt.Errorf("step %q uses agent %q, not %q", opts.StepID, step.Agent, opts.AgentID)
	}

	agent, ok := loaded.Project.Agents[opts.AgentID]
	if !ok {
		return renderContext{}, fmt.Errorf("agent %q is not configured", opts.AgentID)
	}

	attempt, ok := findAttempt(loaded.Run.Status.Attempts, opts.AttemptID)
	if !ok {
		return renderContext{}, fmt.Errorf("attempt %q is not present in run %q", opts.AttemptID, loaded.Run.ID)
	}

	if attempt.StepID != opts.StepID || attempt.AgentID != opts.AgentID {
		return renderContext{}, fmt.Errorf("attempt %q identity is %s/%s, not %s/%s", opts.AttemptID, attempt.StepID, attempt.AgentID, opts.StepID, opts.AgentID)
	}

	return renderContext{
		store:    loaded.Store,
		run:      loaded.Run,
		workflow: loaded.Workflow,
		step:     step,
		agent:    agent,
		attempt:  attempt,
	}, nil
}

func findAttempt(attempts []runstore.Attempt, id string) (runstore.Attempt, bool) {
	for _, attempt := range attempts {
		if attempt.AttemptID == id {
			return attempt, true
		}
	}

	return runstore.Attempt{}, false
}

func renderSelectionDecision(workflowConfig config.Workflow, run *runstore.Run) (workflow.Decision, error) {
	if active := run.Status.ActiveAttempt; active != nil {
		return workflow.Decision{Kind: workflow.DecisionSelectStep, Step: active.StepID, RunStatus: run.Status.State}, nil
	}

	decision, err := workflow.Evaluate(workflowConfig, runstate.WorkflowState(run.Status))
	if err != nil {
		return workflow.Decision{}, fmt.Errorf("evaluate workflow for prompt rendering: %w", err)
	}

	return decision, nil
}

func renderPrompt(ctx context.Context, renderCtx renderContext, opts Options) ([]byte, error) {
	taskContext, err := taskContextContent(ctx, renderCtx)
	if err != nil {
		return nil, err
	}

	reports, err := priorReportContexts(ctx, renderCtx)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.WriteString(promptTitle)
	out.WriteString("## Attempt Metadata\n\n")
	fmt.Fprintf(&out, "- run_id: `%s`\n", opts.RunID)
	fmt.Fprintf(&out, "- workflow: `%s`\n", renderCtx.workflow.Name)
	fmt.Fprintf(&out, "- step_id: `%s`\n", opts.StepID)
	fmt.Fprintf(&out, "- agent_id: `%s`\n", opts.AgentID)
	fmt.Fprintf(&out, "- attempt_id: `%s`\n\n", opts.AttemptID)

	out.WriteString("## Role Descriptor\n\n")
	fmt.Fprintf(&out, "- id: `%s`\n", renderCtx.agent.ID)
	fmt.Fprintf(&out, "- role: `%s`\n", renderCtx.agent.Role)
	fmt.Fprintf(&out, "- description: %s\n\n", renderCtx.agent.Description)
	fmt.Fprintf(&out, "%s\n\n", renderCtx.agent.Body)

	out.WriteString("## Task Context\n\n")
	fmt.Fprintf(&out, "%s\n\n", strings.TrimSpace(taskContext))

	out.WriteString(renderLoopContext(renderCtx, opts))
	out.WriteString(renderPriorReports(reports))

	if reviewerRole(renderCtx.agent.Role) {
		evidence, err := renderVerificationEvidence(renderCtx)
		if err != nil {
			return nil, err
		}

		out.WriteString(evidence)
	}

	if correctionBoundary(renderCtx) {
		out.WriteString("## Correction Search Boundary\n\nStart from the blocking finding, failed command, and changed lines. Inspect direct definitions, callers, and dependencies only. Do not search for additional defects or improvements. Report blocked before broader repository investigation.\n\n")
	}

	out.WriteString(progressGuidance)

	group, err := attemptdeadline.GroupForAttempt(renderCtx.run, renderCtx.attempt)
	if err != nil {
		return nil, fmt.Errorf("resolve attempt deadline action: %w", err)
	}

	guidance, err := attemptdeadline.FromAttempt(renderCtx.attempt, opts.Time, group)
	if err != nil {
		return nil, fmt.Errorf("calculate attempt deadline: %w", err)
	}

	renderAttemptDeadline(&out, guidance)
	out.WriteString(reportContractIntro)

	for _, pair := range allowedPairs(renderCtx.step) {
		fmt.Fprintf(&out, "- `%s`\n", pair)
	}

	out.WriteString(reportCommandIntro)
	fmt.Fprintf(&out, "orc report --run %s --step %s --agent %s --attempt %s --status <status> --result <result> --summary \"<summary>\"\n", shellQuote(opts.RunID), shellQuote(opts.StepID), shellQuote(opts.AgentID), shellQuote(opts.AttemptID))
	out.WriteString("```\n")
	out.WriteString(reportOptionalFields)

	if slices.Contains(allowedPairs(renderCtx.step), "done/changes_requested") {
		out.WriteString(`Use a JSON report for ` + "`done/changes_requested`" + `. Direct report flags cannot provide findings. Each finding must use this exact object shape:

` + "```json" + `
{
  "finding_id": "time-left-missing-error-output",
  "category": "correctness",
  "path": "internal/cli/time_left.go",
  "location": "executeTimeLeft",
  "summary": "The command returns an error without writing it."
}
` + "```" + `

All five fields are required, must be non-empty, and must be trimmed. ` + "`finding_id`" + ` must match ` + "`[a-z0-9][a-z0-9._-]{0,63}`" + ` and must be unique in one report. ` + "`path`" + ` must be a clean project-relative slash path. It must not be absolute, contain a backslash or empty segment, or contain ` + "`.`" + ` or ` + "`..`" + ` segments. A ` + "`done/changes_requested`" + ` report requires one or more findings. A ` + "`done/approved`" + ` report must not contain findings.
`)
	}

	return out.Bytes(), nil
}

func renderAttemptDeadline(out *bytes.Buffer, guidance attemptdeadline.Guidance) {
	out.WriteString("## Attempt Deadline\n\n")
	fmt.Fprintf(out, "- started_at: `%s`\n", attemptdeadline.FormatTime(guidance.StartedAt))
	fmt.Fprintf(out, "- deadline: `%s`\n", attemptdeadline.FormatTime(guidance.Deadline))
	fmt.Fprintf(out, "- timeout: `%s`\n", guidance.TimeoutRaw)
	fmt.Fprintf(out, "- calculated_at: `%s`\n", attemptdeadline.FormatTime(guidance.CalculatedAt))
	fmt.Fprintf(out, "- initial_remaining: `%s`\n", guidance.Remaining)
	fmt.Fprintf(out, "- initial_phase: `%s`\n", guidance.Phase)
	fmt.Fprintf(out, "- initial_action: `%s`\n\n", guidance.Action)
	out.WriteString("These initial values were calculated when Orc rendered this prompt. Use `orc time-left` when you need current deadline, elapsed time, remaining time, timeout, phase, and action guidance. The command reads the injected worker environment by default; `--json` is available for hooks.\n\nDeadline guidance is only an aid during the attempt. Final completion or blockage still goes through `orc report`.\n\n")
}

func renderLoopContext(renderCtx renderContext, opts Options) string {
	caps := renderCtx.workflow.LoopCaps
	if !caps.Enabled {
		return ""
	}

	count := renderCtx.run.Status.WorkflowLoop.Counts[opts.StepID]
	if count <= caps.Soft {
		return ""
	}

	var out strings.Builder
	out.WriteString("## Workflow Loop Context\n\n")
	fmt.Fprintf(&out, "- workflow: `%s`\n", renderCtx.workflow.Name)
	fmt.Fprintf(&out, "- repeated_state: `%s`\n", opts.StepID)
	fmt.Fprintf(&out, "- current_count: `%d`\n", count)
	fmt.Fprintf(&out, "- soft_cap: `%d`\n", caps.Soft)
	fmt.Fprintf(&out, "- hard_cap: `%d`\n", caps.Hard)

	statuses := priorLoopStatuses(renderCtx.run.Status.WorkflowLoop.Entries, opts.StepID)
	if len(statuses) > 0 {
		fmt.Fprintf(&out, "- prior_statuses: `%s`\n", strings.Join(statuses, "`, `"))
	} else {
		out.WriteString("- prior_statuses: not available\n")
	}

	out.WriteString("\nThis workflow state is past its soft loop cap. Use this attempt to break the loop with new information, choose a terminal/human-handoff report when blocked, or escalate clearly instead of repeating the same outcome.\n\n")

	return out.String()
}

func priorLoopStatuses(entries []runstore.WorkflowStateEntry, state string) []string {
	statuses := make([]string, 0)

	for _, entry := range entries {
		if entry.State != state || entry.TriggerStatus == "" {
			continue
		}

		status := entry.TriggerStatus
		if entry.TriggerResult != "" {
			status += "/" + entry.TriggerResult
		}

		statuses = append(statuses, status)
	}

	return statuses
}

const (
	promptTitle = "# Tiny Orc Worker Prompt\n\n"

	progressGuidance = `## Live Progress

When useful, send short operator-visible updates with ` + "`orc progress <short update>`" + ` at crucial points such as starting analysis, choosing an approach, beginning tests, or finding a blocker. Do not stream logs, file lists, diffs, frequent heartbeat messages, or routine chatter through live progress.

The launcher injects ` + "`ORC_PROGRESS_SOCKET`" + `, ` + "`ORC_PROGRESS_TOKEN`" + `, ` + "`ORC_RUN_ID`" + `, ` + "`ORC_STEP_ID`" + `, ` + "`ORC_ATTEMPT_ID`" + `, ` + "`ORC_PROJECT_ROOT`" + `, ` + "`ORC_ATTEMPT_STARTED_AT`" + `, ` + "`ORC_ATTEMPT_DEADLINE`" + `, and ` + "`ORC_ATTEMPT_TIMEOUT`" + ` for troubleshooting. You normally do not pass them manually. Live progress is optional operator feedback and is separate from the final report.

`

	reportContractIntro = `## Report Contract

When this attempt is complete or blocked, report through ` + "`orc report`" + `. Do not write directly into ` + "`.orc/runs`" + `.

Allowed status/result pairs for this step:

`

	reportCommandIntro = `
Use this command shape with one allowed status/result pair:

` + "```bash\n"

	reportOptionalFields = `
Optional structured report fields:

- ` + "`--changed-path <path>`" + `: changed path; repeatable.
- ` + "`--command <command>`" + `: command run; repeatable.
- ` + "`--test <test>`" + `: test or verification result; repeatable.
- ` + "`--risk <risk>`" + `: risk, caveat, or unverified area; repeatable.
- ` + "`--follow-up <title>`" + `: follow-up suggestion title; repeatable.
- ` + "`--report-file <path>`" + `: Markdown detail file to copy into the run store.

For richer structured reports, you may instead write a JSON report file and use
` + "`orc report --json-file <path>`" + `. Do not combine ` + "`--json-file`" + ` with report field flags.
`
)

func renderPriorReports(reports []reportContext) string {
	var out strings.Builder
	out.WriteString("## Prior Report Context\n\n")

	if len(reports) == 0 {
		out.WriteString("No prior reports are recorded for this run.\n\n")
		return out.String()
	}

	for _, report := range reports {
		fmt.Fprintf(&out, "### %s\n\n", report.heading)

		if report.fullPath != "" {
			fmt.Fprintf(&out, "Full report: `%s`\n\n", report.fullPath)
		}

		fmt.Fprintf(&out, "%s\n", report.content)

		out.WriteString("\n")
	}

	return out.String()
}

func taskContextContent(ctx context.Context, renderCtx renderContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("task context content: %w", err)
	}

	for _, ref := range renderCtx.run.Status.Artifacts {
		if ref.Kind != runstore.KindTaskContext {
			continue
		}

		content, err := renderCtx.store.ReadArtifactContext(ctx, renderCtx.run.ID, ref)
		if err != nil {
			return "", fmt.Errorf("read task context %s: %w", ref.Path, err)
		}

		return string(content), nil
	}

	return "", fmt.Errorf("run %q has no task context artifact", renderCtx.run.ID)
}

type reportContext struct {
	heading  string
	content  string
	fullPath string
}

func priorReportContexts(ctx context.Context, renderCtx renderContext) ([]reportContext, error) {
	selected, err := selectReports(renderCtx)
	if err != nil {
		return nil, err
	}

	var reports []reportContext

	for _, attempt := range selected {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("prior report contexts: %w", err)
		}

		if attempt.Report.ReportRef == nil || attempt.Report.ReportRef.Path == "" {
			return nil, fmt.Errorf("run %q attempt %q step %q missing report_ref", renderCtx.run.ID, attempt.AttemptID, attempt.StepID)
		}

		content, err := renderCtx.store.ReadArtifactContext(ctx, renderCtx.run.ID, *attempt.Report.ReportRef)
		if err != nil {
			return nil, fmt.Errorf("read prior attempt report %s: %w", attempt.Report.ReportRef.Path, err)
		}

		fullPath := filepath.ToSlash(filepath.Join(".orc", "runs", renderCtx.run.ID, filepath.FromSlash(attempt.Report.ReportRef.Path)))
		reports = append(reports, reportContext{
			heading:  fmt.Sprintf("attempt %s (%s %s/%s)", attempt.AttemptID, attempt.StepID, attempt.Report.Status, attempt.Report.Result),
			content:  strings.TrimSpace(string(content)),
			fullPath: fullPath,
		})
	}

	return reports, nil
}

func acceptedReports(attempts []runstore.Attempt) []runstore.Attempt {
	var out []runstore.Attempt

	for _, a := range attempts {
		if a.State == runstore.AttemptStateReported && a.Report != nil && a.ReportRef != nil {
			out = append(out, a)
		}
	}

	return out
}

func snapshotVersion(a runstore.Attempt) int {
	if a.ConfigSnapshotVersion == 0 {
		return 1
	}

	return a.ConfigSnapshotVersion
}

func selectReports(renderCtx renderContext) ([]runstore.Attempt, error) {
	accepted := acceptedReports(renderCtx.run.Status.Attempts)

	var planner, implementation, modifying, routing *runstore.Attempt

	startSeq := attemptStartedSequence(renderCtx.run.Events, renderCtx.attempt.AttemptID)

	for i := range accepted {
		a := &accepted[i]

		snapshot, err := configsnapshot.LoadVersion(renderCtx.run, snapshotVersion(*a))
		if err != nil {
			return nil, fmt.Errorf("load report snapshot: %w", err)
		}

		agent := snapshot.Project.Agents[a.AgentID]
		if agent.Role == "planner" {
			planner = later(planner, a)
		}

		if receivesImplementationReport(renderCtx.agent.Role) && implementationRole(agent.Role) {
			implementation = later(implementation, a)
		}

		if len(a.Report.ChangedPaths) > 0 {
			modifying = later(modifying, a)
		}

		if startSeq != 0 && a.ConsumedByEvent == startSeq {
			routing = a
		}
	}

	fresh, err := freshVerification(renderCtx)
	if err != nil {
		return nil, err
	}

	ordered := []*runstore.Attempt{planner, implementation, modifying, fresh, routing}
	if routing != nil {
		for _, finding := range routing.Report.Findings {
			for i := range accepted {
				if containsFinding(accepted[i].Report.Findings, finding.FindingID) {
					ordered = append(ordered, &accepted[i])
					break
				}
			}
		}
	}

	seen := map[string]bool{}

	var out []runstore.Attempt

	for _, a := range ordered {
		if a != nil && !seen[a.AttemptID] {
			seen[a.AttemptID] = true
			out = append(out, *a)
		}
	}

	return out, nil
}

func later(current, candidate *runstore.Attempt) *runstore.Attempt {
	if current == nil || candidate.ReportRef.EventSequence > current.ReportRef.EventSequence {
		return candidate
	}

	return current
}

func containsFinding(values []runstore.Finding, id string) bool {
	return slices.ContainsFunc(values, func(value runstore.Finding) bool { return value.FindingID == id })
}

func attemptStartedSequence(events []runstore.Event, attemptID string) int {
	for _, event := range events {
		if event.Type != "attempt.started" {
			continue
		}

		var p struct {
			Attempt struct {
				AttemptID string `json:"attempt_id"`
			} `json:"attempt"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && p.Attempt.AttemptID == attemptID {
			return event.Sequence
		}
	}

	return 0
}

func freshVerification(renderCtx renderContext) (*runstore.Attempt, error) {
	wantVersion := snapshotVersion(renderCtx.attempt)

	var (
		changedSeq int
		fresh      *runstore.Attempt
	)

	for _, a := range acceptedReports(renderCtx.run.Status.Attempts) {
		if len(a.Report.ChangedPaths) > 0 && a.ReportRef.EventSequence > changedSeq {
			changedSeq = a.ReportRef.EventSequence
		}
	}

	for i := range renderCtx.run.Status.Attempts {
		a := &renderCtx.run.Status.Attempts[i]
		if a.State != runstore.AttemptStateReported || a.Report == nil || a.ReportRef == nil || snapshotVersion(*a) != wantVersion || a.Report.Status != "done" || a.Report.Result != "passed" {
			continue
		}

		snapshot, err := configsnapshot.LoadVersion(renderCtx.run, snapshotVersion(*a))
		if err != nil {
			return nil, fmt.Errorf("load verification snapshot: %w", err)
		}

		step, ok := snapshot.Project.Workflows[renderCtx.run.Status.Workflow].Steps[a.StepID]
		if !ok || step.Verification != "full" || (step.EffectiveKind() != config.StepKindCommand && step.EffectiveKind() != config.StepKindScript) || a.ReportRef.EventSequence <= changedSeq {
			continue
		}

		fresh = later(fresh, a)
	}

	return fresh, nil
}

func reviewerRole(role string) bool {
	return role == "reviewer" || strings.HasSuffix(role, "-reviewer")
}

func receivesImplementationReport(role string) bool {
	return implementationRole(role) || role == "tester" || reviewerRole(role)
}

func implementationRole(role string) bool {
	return role == "coder" || role == "mechanical-coder"
}

func correctionRoute(ctx renderContext) bool {
	seq := attemptStartedSequence(ctx.run.Events, ctx.attempt.AttemptID)
	if seq == 0 {
		return false
	}

	for _, a := range acceptedReports(ctx.run.Status.Attempts) {
		if a.ConsumedByEvent == seq {
			return a.Report.Status == "failed" || a.Report.Result == "changes_requested"
		}
	}

	return false
}

func correctionBoundary(ctx renderContext) bool {
	return implementationRole(ctx.agent.Role) && correctionRoute(ctx)
}

func renderVerificationEvidence(ctx renderContext) (string, error) {
	var out strings.Builder
	out.WriteString("## Verification Evidence\n\n")

	evidence, err := freshVerification(ctx)
	if err != nil {
		return "", err
	}

	if evidence != nil {
		snapshot, err := configsnapshot.LoadVersion(ctx.run, snapshotVersion(*evidence))
		if err != nil {
			return "", fmt.Errorf("load verification evidence snapshot: %w", err)
		}

		step := snapshot.Project.Workflows[ctx.run.Status.Workflow].Steps[evidence.StepID]
		fmt.Fprintf(&out, "- step_id: `%s`\n- attempt_id: `%s`\n- command: `%s`\n- result: `%s/%s`\n- event_sequence: `%d`\n\n", evidence.StepID, evidence.AttemptID, shellDisplay(step), evidence.Report.Status, evidence.Report.Result, evidence.ReportRef.EventSequence)
		out.WriteString("This full-verification result is fresh. Do not repeat the full check. Accurate `changed_paths` values control freshness.\n\n")

		return out.String(), nil
	}

	if len(ctx.workflow.Verification.FullCheck.Argv) > 0 {
		fmt.Fprintf(&out, "No fresh full-verification evidence exists. Run `%s` from the project root before `done/approved`. If it cannot run, report `blocked/blocked`. Accurate `changed_paths` values control freshness.\n\n", quoteArgv(ctx.workflow.Verification.FullCheck.Argv))
		return out.String(), nil
	}

	out.WriteString("No fresh full-verification evidence or fallback command exists. Report `blocked/blocked`. Accurate `changed_paths` values control freshness.\n\n")

	return out.String(), nil
}

func shellDisplay(step config.Step) string {
	if step.EffectiveKind() == config.StepKindCommand {
		return quoteArgv(step.Command.Argv)
	}

	return quoteArgv(append([]string{step.Script.Path}, step.Script.Args...))
}

func quoteArgv(argv []string) string {
	values := make([]string, len(argv))
	for i, value := range argv {
		values[i] = shellQuote(value)
	}

	return strings.Join(values, " ")
}

func allowedPairs(step config.Step) []string {
	var pairs []string

	for status, results := range step.AllowedResults {
		for _, result := range results {
			if !reportpkg.WorkerReportableOutcome(status, result) {
				continue
			}

			pairs = append(pairs, status+"/"+result)
		}
	}

	slices.Sort(pairs)

	return pairs
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}

	if strings.IndexFunc(value, func(r rune) bool { return !shellSafeRune(r) }) == -1 {
		return value
	}

	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellSafeRune(r rune) bool {
	return r >= '0' && r <= '9' ||
		r >= 'A' && r <= 'Z' ||
		r >= 'a' && r <= 'z' ||
		strings.ContainsRune("._-/:", r)
}
