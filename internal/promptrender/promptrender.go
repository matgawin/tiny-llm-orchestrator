// Package promptrender renders role-specific prompts for worker attempts.
package promptrender

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	reportpkg "tiny-llm-orchestrator/orc/internal/report"
	"tiny-llm-orchestrator/orc/internal/runcontext"
	"tiny-llm-orchestrator/orc/internal/runstate"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/workflow"
)

const priorReportExcerptLimit = 1200

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
}

// Render loads run/config context, renders the worker prompt, and persists it
// as a prompt artifact through the Run Store.
func Render(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		return Result{}, stableerr.New("context is required")
	}

	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("render: %w", err)
	}

	if err := validateOptions(opts); err != nil {
		return Result{}, err
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
		return stableerr.New("project root is required")
	case opts.RunID == "":
		return stableerr.New("run id is required")
	case opts.StepID == "":
		return stableerr.New("step id is required")
	case opts.AgentID == "":
		return stableerr.New("agent id is required")
	case opts.AttemptID == "":
		return stableerr.New("attempt id is required")
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
		return renderContext{}, stableerr.Errorf("run %q has no selected runnable step; decision is %s", loaded.Run.ID, decision.Kind)
	}

	if !opts.AllowUnselectedStep && opts.StepID != decision.Step {
		return renderContext{}, stableerr.Errorf("step %q is not selected for run %q; selected step is %q", opts.StepID, loaded.Run.ID, decision.Step)
	}

	step, ok := loaded.Workflow.Steps[opts.StepID]
	if !ok {
		return renderContext{}, stableerr.Errorf("step %q is not declared in workflow %q", opts.StepID, loaded.Workflow.Name)
	}

	if step.Agent != opts.AgentID {
		return renderContext{}, stableerr.Errorf("step %q uses agent %q, not %q", opts.StepID, step.Agent, opts.AgentID)
	}

	agent, ok := loaded.Project.Agents[opts.AgentID]
	if !ok {
		return renderContext{}, stableerr.Errorf("agent %q is not configured", opts.AgentID)
	}

	return renderContext{
		store:    loaded.Store,
		run:      loaded.Run,
		workflow: loaded.Workflow,
		step:     step,
		agent:    agent,
	}, nil
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
	out.WriteString(progressGuidance)
	out.WriteString(reportContractIntro)

	for _, pair := range allowedPairs(renderCtx.step) {
		fmt.Fprintf(&out, "- `%s`\n", pair)
	}

	out.WriteString(reportCommandIntro)
	fmt.Fprintf(&out, "orc report --run %s --step %s --agent %s --attempt %s --status <status> --result <result> --summary \"<summary>\"\n", shellQuote(opts.RunID), shellQuote(opts.StepID), shellQuote(opts.AgentID), shellQuote(opts.AttemptID))
	out.WriteString("```\n")
	out.WriteString(reportOptionalFields)

	return out.Bytes(), nil
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

The launcher injects ` + "`ORC_PROGRESS_SOCKET`" + `, ` + "`ORC_PROGRESS_TOKEN`" + `, ` + "`ORC_RUN_ID`" + `, ` + "`ORC_STEP_ID`" + `, and ` + "`ORC_ATTEMPT_ID`" + ` for troubleshooting. You normally do not pass them manually. Live progress is optional operator feedback and is separate from the final report.

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

		fmt.Fprintf(&out, "%s\n", report.excerpt)

		if report.requireRead {
			fmt.Fprintf(&out, "\nThis excerpt is truncated. Read the full report before using this prior report as implementation, review, or correction input: `%s`\n", report.fullPath)
		}

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

	return "", stableerr.Errorf("run %q has no task context artifact", renderCtx.run.ID)
}

type reportContext struct {
	heading     string
	excerpt     string
	fullPath    string
	requireRead bool
}

func priorReportContexts(ctx context.Context, renderCtx renderContext) ([]reportContext, error) {
	var reports []reportContext
	for _, skipped := range renderCtx.run.Status.SkippedSteps {
		reports = append(reports, reportContext{
			heading: fmt.Sprintf("step %s skipped", skipped.StepID),
			excerpt: fmt.Sprintf("step %s skipped by human decision: %s", skipped.StepID, skipped.Reason),
		})
	}

	attemptReportPaths := attemptReportArtifactPaths(renderCtx.run.Status.Attempts)

	for _, ref := range renderCtx.run.Status.Artifacts {
		if ref.Kind != runstore.KindReport {
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("prior report contexts: %w", err)
		}

		content, err := renderCtx.store.ReadArtifactContext(ctx, renderCtx.run.ID, ref)
		if err != nil {
			return nil, fmt.Errorf("read prior report %s: %w", ref.Path, err)
		}

		if _, ok := attemptReportPaths[ref.Path]; ok {
			continue
		}

		excerpt, _ := excerptMarkdown(content, priorReportExcerptLimit)
		reports = append(reports, reportContext{
			heading: ref.Path,
			excerpt: excerpt,
		})
	}

	for _, attempt := range renderCtx.run.Status.Attempts {
		if attempt.Report == nil {
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("prior report contexts: %w", err)
		}

		if attempt.Report.ReportRef == nil || attempt.Report.ReportRef.Path == "" {
			return nil, stableerr.Errorf("run %q attempt %q step %q missing report_ref", renderCtx.run.ID, attempt.AttemptID, attempt.StepID)
		}

		content, err := renderCtx.store.ReadArtifactContext(ctx, renderCtx.run.ID, *attempt.Report.ReportRef)
		if err != nil {
			return nil, fmt.Errorf("read prior attempt report %s: %w", attempt.Report.ReportRef.Path, err)
		}

		fullPath := filepath.ToSlash(filepath.Join(".orc", "runs", renderCtx.run.ID, filepath.FromSlash(attempt.Report.ReportRef.Path)))
		excerpt, truncated := excerptMarkdown(content, priorReportExcerptLimit)
		reports = append(reports, reportContext{
			heading:     fmt.Sprintf("attempt %s (%s %s/%s)", attempt.AttemptID, attempt.StepID, attempt.Report.Status, attempt.Report.Result),
			excerpt:     excerpt,
			fullPath:    fullPath,
			requireRead: truncated,
		})
	}

	return reports, nil
}

func attemptReportArtifactPaths(attempts []runstore.Attempt) map[string]struct{} {
	paths := make(map[string]struct{})

	for _, attempt := range attempts {
		if attempt.Report == nil || attempt.Report.ReportRef == nil || attempt.Report.ReportRef.Path == "" {
			continue
		}

		paths[attempt.Report.ReportRef.Path] = struct{}{}
	}

	return paths
}

func excerptMarkdown(content []byte, limit int) (string, bool) {
	text := strings.TrimSpace(string(content))
	if text == "" {
		return "(empty report)", false
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}

	return strings.TrimSpace(string(runes[:limit])) + "\n\n[excerpt truncated]", true
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
