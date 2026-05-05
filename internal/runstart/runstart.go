// Package runstart resolves explicit task context and creates a durable run.
package runstart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/vcs"
)

const (
	SourceBead             = "bead"
	SourceTaskFile         = "task_file"
	SourceInlineTask       = "inline_task"
	SourceStdinTask        = "stdin_task"
	SourceFallbackTaskFile = "fallback_task_file"

	taskContextBeadsDisabled = "disabled"
	taskContextBeadsRequired = "required"
)

// Options describes a noninteractive run start request.
type Options struct {
	Root             string
	RunID            string
	Workflow         string
	BeadID           string
	FallbackTaskFile string
	TaskFile         string
	TaskText         string
	TaskStdin        bool
	Stdin            io.Reader
	Env              []string
	Time             time.Time
}

// Result describes the created run.
type Result struct {
	RunID string
	Path  string
}

// Snapshot is the stable metadata persisted as task/snapshot.json.
type Snapshot struct {
	SchemaVersion int          `json:"schema_version"`
	Source        Source       `json:"source"`
	BeadLookup    BeadLookup   `json:"bead_lookup"`
	Fallback      FallbackInfo `json:"fallback"`
}

// Source describes the task context source used for the run.
type Source struct {
	Type    string            `json:"type"`
	BeadID  string            `json:"bead_id,omitempty"`
	Path    string            `json:"path,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// BeadLookup records the explicit bead lookup attempt, if any.
type BeadLookup struct {
	Attempted bool     `json:"attempted"`
	OK        bool     `json:"ok"`
	BeadID    string   `json:"bead_id,omitempty"`
	Command   []string `json:"command,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// FallbackInfo records whether fallback Markdown task context was used.
type FallbackInfo struct {
	Used       bool   `json:"used"`
	SourceType string `json:"source_type,omitempty"`
	Path       string `json:"path,omitempty"`
}

type resolvedTask struct {
	context  []byte
	snapshot Snapshot
	taskSlug string
}

// Start resolves task context, creates a run, and persists task artifacts.
func Start(ctx context.Context, opts Options) (Result, error) {
	if opts.Root == "" {
		return Result{}, errors.New("project root is required")
	}
	if opts.Workflow == "" {
		return Result{}, errors.New("workflow is required")
	}
	project, err := config.Load(opts.Root)
	if err != nil {
		return Result{}, fmt.Errorf("load project config: %w", err)
	}
	workflow, ok := project.Workflows[opts.Workflow]
	if !ok {
		return Result{}, fmt.Errorf("workflow %q is not configured", opts.Workflow)
	}
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	task, err := resolveTask(ctx, workflow, opts)
	if err != nil {
		return Result{}, err
	}
	vcsSnapshot, err := inspectPreRunVCS(ctx, workflow, opts)
	if err != nil {
		return Result{}, err
	}

	store, err := runstore.Open(project.Root)
	if err != nil {
		return Result{}, err
	}
	return createRun(opts, store, task, vcsSnapshot)
}

func createRun(opts Options, store *runstore.Store, task resolvedTask, vcsSnapshot vcs.Snapshot) (Result, error) {
	run, err := store.Create(runstore.CreateRunRequest{
		RunID:    opts.RunID,
		Workflow: opts.Workflow,
		TaskSlug: task.taskSlug,
		Time:     opts.Time,
	})
	if err != nil {
		return Result{}, err
	}
	if err := writeTaskArtifact(store, run.ID, runstore.KindTaskContext, task.context, opts.Time); err != nil {
		return Result{}, cleanupStartedRun(run.Path, err)
	}
	snapshot, err := json.MarshalIndent(task.snapshot, "", "  ")
	if err != nil {
		return Result{}, cleanupStartedRun(run.Path, fmt.Errorf("marshal task snapshot: %w", err))
	}
	snapshot = append(snapshot, '\n')
	if err := writeTaskArtifact(store, run.ID, runstore.KindTaskSnapshot, snapshot, opts.Time); err != nil {
		return Result{}, cleanupStartedRun(run.Path, err)
	}
	if _, err := vcs.WriteSnapshot(store, run.ID, "vcs-pre-run", vcsSnapshot, opts.Time); err != nil {
		return Result{}, cleanupStartedRun(run.Path, err)
	}
	return Result{RunID: run.ID, Path: run.Path}, nil
}

func inspectPreRunVCS(ctx context.Context, workflow config.Workflow, opts Options) (vcs.Snapshot, error) {
	snapshot, err := vcs.InspectPreRun(ctx, vcs.Options{
		Root: opts.Root,
		Env:  opts.Env,
		Time: opts.Time,
	})
	if err != nil {
		return vcs.Snapshot{}, fmt.Errorf("inspect VCS before run start: %w", err)
	}
	if snapshot.Kind == vcs.KindNone && workflow.VCS.EffectiveNoVCS() == config.VCSNoVCSBlock {
		return vcs.Snapshot{}, errors.New("workflow requires supported VCS but no supported VCS was detected")
	}
	if snapshot.Dirty && workflow.VCS.EffectiveDirtyStart() == config.VCSDirtyStartBlock {
		return vcs.Snapshot{}, fmt.Errorf("working copy is dirty; workflow %q blocks dirty starts", workflow.Name)
	}
	return snapshot, nil
}

func writeTaskArtifact(store *runstore.Store, runID string, kind runstore.ArtifactKind, content []byte, at time.Time) error {
	_, err := store.WriteArtifact(runID, runstore.Artifact{
		Kind:    kind,
		Name:    "task",
		Content: content,
		Time:    at,
	})
	return err
}

func resolveTask(ctx context.Context, workflow config.Workflow, opts Options) (resolvedTask, error) {
	if err := validateSources(opts); err != nil {
		return resolvedTask{}, err
	}
	if err := validateWorkflowPolicy(workflow, opts); err != nil {
		return resolvedTask{}, err
	}
	switch {
	case opts.BeadID != "":
		return resolveBeadTask(ctx, opts)
	case opts.TaskFile != "":
		return resolveTaskFile(opts.TaskFile, SourceTaskFile)
	case opts.TaskText != "":
		return resolvedMarkdownTask(SourceInlineTask, "", []byte(opts.TaskText), ""), nil
	case opts.TaskStdin:
		return resolveStdinTask(opts)
	default:
		return resolvedTask{}, errors.New("noninteractive run start requires --bead, --task-file, --task, or --task-stdin")
	}
}

func validateSources(opts Options) error {
	sources := selectedTaskSources(opts)
	count := 0
	for _, source := range sources {
		if source.selected {
			count++
		}
	}
	if count > 1 {
		return fmt.Errorf("%s are mutually exclusive", allowedTaskSourceList(sources))
	}
	if opts.FallbackTaskFile != "" && opts.BeadID == "" {
		return errors.New("--fallback-task-file requires --bead")
	}
	return nil
}

type taskSourceSelection struct {
	name     string
	selected bool
}

func selectedTaskSources(opts Options) []taskSourceSelection {
	return []taskSourceSelection{
		{name: "--bead", selected: opts.BeadID != ""},
		{name: "--task-file", selected: opts.TaskFile != ""},
		{name: "--task", selected: opts.TaskText != ""},
		{name: "--task-stdin", selected: opts.TaskStdin},
	}
}

func allowedTaskSourceList(sources []taskSourceSelection) string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.name)
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}

func validateWorkflowPolicy(workflow config.Workflow, opts Options) error {
	beads := workflow.TaskContext.Beads
	markdownAllowed := workflow.TaskContext.MarkdownFallback.Value
	switch {
	case beads == taskContextBeadsDisabled && opts.BeadID != "":
		return fmt.Errorf("workflow %q disables bead task context", workflow.Name)
	case beads == taskContextBeadsRequired && opts.BeadID == "":
		return fmt.Errorf("workflow %q requires bead task context", workflow.Name)
	}
	if !markdownAllowed {
		if opts.TaskFile != "" || opts.TaskText != "" || opts.TaskStdin {
			return fmt.Errorf("workflow %q disables Markdown task context", workflow.Name)
		}
		if opts.FallbackTaskFile != "" {
			return fmt.Errorf("workflow %q disables Markdown fallback task context", workflow.Name)
		}
	}
	return nil
}

func resolveBeadTask(ctx context.Context, opts Options) (resolvedTask, error) {
	command := []string{"bd", "show", opts.BeadID, "--json"}
	content, lookupErr := runBeadCommand(ctx, opts.Root, command, opts.Env)
	if lookupErr != nil {
		if opts.FallbackTaskFile == "" {
			return resolvedTask{}, fmt.Errorf("read bead %q: %w", opts.BeadID, lookupErr)
		}
		task, err := resolveTaskFile(opts.FallbackTaskFile, SourceFallbackTaskFile)
		if err != nil {
			return resolvedTask{}, err
		}
		applyFallbackSnapshotMetadata(&task.snapshot, opts, command, lookupErr)
		task.taskSlug = opts.BeadID
		return task, nil
	}
	snapshot := Snapshot{
		SchemaVersion: 1,
		Source: Source{
			Type:    SourceBead,
			BeadID:  opts.BeadID,
			Command: command,
			Env:     observedBeadsEnv(opts.Env),
		},
		BeadLookup: BeadLookup{
			Attempted: true,
			OK:        true,
			BeadID:    opts.BeadID,
			Command:   command,
		},
	}
	return resolvedTask{
		context:  beadMarkdownContext(opts.BeadID, content),
		snapshot: snapshot,
		taskSlug: opts.BeadID,
	}, nil
}

func applyFallbackSnapshotMetadata(snapshot *Snapshot, opts Options, command []string, lookupErr error) {
	// Source records the task context actually used; Fallback records that the
	// fallback file replaced a failed explicit bead lookup.
	snapshot.BeadLookup = BeadLookup{
		Attempted: true,
		OK:        false,
		BeadID:    opts.BeadID,
		Command:   command,
		Error:     lookupErr.Error(),
	}
	snapshot.Source.Env = observedBeadsEnv(opts.Env)
	snapshot.Fallback = FallbackInfo{
		Used:       true,
		SourceType: SourceTaskFile,
		Path:       opts.FallbackTaskFile,
	}
}

func runBeadCommand(ctx context.Context, dir string, command, env []string) ([]byte, error) {
	// #nosec G204 -- run start intentionally invokes the fixed external bd command.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return output, nil
}

func resolveTaskFile(path, sourceType string) (resolvedTask, error) {
	if path == "" {
		return resolvedTask{}, errors.New("task file path is required")
	}
	content, err := os.ReadFile(path) // #nosec G304 -- caller-provided task file is the explicit source.
	if err != nil {
		return resolvedTask{}, fmt.Errorf("read task file %q: %w", path, err)
	}
	return resolvedMarkdownTask(sourceType, path, content, taskSlugFromPath(path)), nil
}

func resolveStdinTask(opts Options) (resolvedTask, error) {
	if opts.Stdin == nil {
		return resolvedTask{}, errors.New("--task-stdin requires stdin")
	}
	content, err := io.ReadAll(opts.Stdin)
	if err != nil {
		return resolvedTask{}, fmt.Errorf("read task stdin: %w", err)
	}
	return resolvedMarkdownTask(SourceStdinTask, "", content, ""), nil
}

func resolvedMarkdownTask(sourceType, path string, content []byte, taskSlug string) resolvedTask {
	if len(content) > 0 {
		content = []byte(normalizeMarkdown(string(content)))
	}
	return resolvedTask{
		context: content,
		snapshot: Snapshot{
			SchemaVersion: 1,
			Source: Source{
				Type: sourceType,
				Path: path,
			},
		},
		taskSlug: taskSlug,
	}
}

func beadMarkdownContext(beadID string, content []byte) []byte {
	var b strings.Builder
	b.WriteString("# Bead ")
	b.WriteString(beadID)
	b.WriteString("\n\n```json\n")
	b.Write(content)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return []byte(b.String())
}

func normalizeMarkdown(content string) string {
	if content == "" || strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}

func taskSlugFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func observedBeadsEnv(env []string) map[string]string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "BEADS_DIR" {
			return map[string]string{"BEADS_DIR": value}
		}
	}
	return nil
}

func cleanupStartedRun(runPath string, cause error) error {
	if err := os.RemoveAll(runPath); err != nil { // #nosec G304 -- runPath is returned by the Run Store for the run just created.
		return errors.Join(cause, fmt.Errorf("cleanup run directory %s: %w", runPath, err))
	}
	return cause
}
