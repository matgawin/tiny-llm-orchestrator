// Package promptrender renders role-specific prompts for worker attempts.
package promptrender

import (
	"bytes"
	"context"
	"errors"
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
		return Result{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validateOptions(opts); err != nil {
		return Result{}, err
	}
	renderCtx, err := loadRenderContext(opts)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	content, err := renderPrompt(ctx, renderCtx, opts)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	ref, err := renderCtx.store.WriteArtifact(opts.RunID, runstore.Artifact{
		Kind:    runstore.KindPrompt,
		Name:    opts.StepID,
		Content: content,
		Time:    opts.Time,
	})
	result := resultFromArtifact(renderCtx.run.Path, ref, content)
	if err != nil {
		return result, err
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

func loadRenderContext(opts Options) (renderContext, error) {
	loaded, err := runcontext.Load(opts.Root, opts.RunID)
	if err != nil {
		return renderContext{}, err
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
	return workflow.Evaluate(workflowConfig, runstate.WorkflowState(run.Status))
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

	out.WriteString(renderPriorReports(reports))
	out.WriteString(reportContractIntro)
	for _, pair := range allowedPairs(renderCtx.step) {
		fmt.Fprintf(&out, "- `%s`\n", pair)
	}
	out.WriteString(reportCommandIntro)
	fmt.Fprintf(&out, "orc report --run %s --step %s --agent %s --attempt %s --status <status> --result <result> --summary \"<summary>\"\n", shellQuote(opts.RunID), shellQuote(opts.StepID), shellQuote(opts.AgentID), shellQuote(opts.AttemptID))
	out.WriteString("```\n")
	return out.Bytes(), nil
}

const (
	promptTitle = "# Tiny Orc Worker Prompt\n\n"

	reportContractIntro = `## Report Contract

When this attempt is complete or blocked, report through ` + "`orc report`" + `. Do not write directly into ` + "`.orc/runs`" + `.

Allowed status/result pairs for this step:

`

	reportCommandIntro = `
Use this command shape with one allowed status/result pair:

` + "```bash\n"
)

func renderPriorReports(reports []reportContext) string {
	var out strings.Builder
	out.WriteString("## Prior Report Context\n\n")
	if len(reports) == 0 {
		out.WriteString("No prior report artifacts are recorded for this run.\n\n")
		return out.String()
	}
	for _, report := range reports {
		fmt.Fprintf(&out, "### %s\n\n%s\n\n", report.path, report.excerpt)
	}
	return out.String()
}

func taskContextContent(ctx context.Context, renderCtx renderContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for _, ref := range renderCtx.run.Status.Artifacts {
		if ref.Kind != runstore.KindTaskContext {
			continue
		}
		content, err := renderCtx.store.ReadArtifact(renderCtx.run.ID, ref)
		if err != nil {
			return "", fmt.Errorf("read task context %s: %w", ref.Path, err)
		}
		return string(content), nil
	}
	return "", fmt.Errorf("run %q has no task context artifact", renderCtx.run.ID)
}

type reportContext struct {
	path    string
	excerpt string
}

func priorReportContexts(ctx context.Context, renderCtx renderContext) ([]reportContext, error) {
	var reports []reportContext
	for _, ref := range renderCtx.run.Status.Artifacts {
		if ref.Kind != runstore.KindReport {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := renderCtx.store.ReadArtifact(renderCtx.run.ID, ref)
		if err != nil {
			return nil, fmt.Errorf("read prior report %s: %w", ref.Path, err)
		}
		reports = append(reports, reportContext{
			path:    ref.Path,
			excerpt: excerptMarkdown(content, priorReportExcerptLimit),
		})
	}
	return reports, nil
}

func excerptMarkdown(content []byte, limit int) string {
	text := strings.TrimSpace(string(content))
	if text == "" {
		return "(empty report)"
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n\n[excerpt truncated]"
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
