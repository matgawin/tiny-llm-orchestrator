// Package vcs inspects project working-copy state with read-only VCS commands.
package vcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
)

const (
	KindJJ   = "jj"
	KindGit  = "git"
	KindNone = "none"

	PhasePreRun  = "pre_run"
	PhasePostRun = "post_run"
	PhaseRefresh = "config_refresh"

	schemaVersion = 1
)

var (
	jjRootCommand    = []string{"jj", "root"}
	jjStatusCommand  = []string{"jj", "status"}
	gitRootCommand   = []string{"git", "rev-parse", "--show-toplevel"}
	gitStatusCommand = []string{"git", "status", "--porcelain=v1", "-z", "--untracked-files=all"}
)

// Options controls VCS command execution.
type Options struct {
	Root string
	Env  []string
	Time time.Time
}

// Snapshot is the stable JSON contract persisted for VCS observations.
type Snapshot struct {
	SchemaVersion int        `json:"schema_version"`
	Phase         string     `json:"phase"`
	Kind          string     `json:"kind"`
	Dirty         bool       `json:"dirty"`
	Summary       string     `json:"summary"`
	ChangedPaths  []string   `json:"changed_paths"`
	Commands      [][]string `json:"commands"`
	Error         string     `json:"error,omitempty"`
}

// InspectPreRun records the working-copy state before a run starts.
func InspectPreRun(ctx context.Context, opts Options) (Snapshot, error) {
	return inspect(ctx, opts, PhasePreRun)
}

// InspectPostRun records the working-copy state after a run has produced work.
func InspectPostRun(ctx context.Context, opts Options) (Snapshot, error) {
	return inspect(ctx, opts, PhasePostRun)
}

// InspectRefresh records the working-copy state when a run adopts refreshed config.
func InspectRefresh(ctx context.Context, opts Options) (Snapshot, error) {
	return inspect(ctx, opts, PhaseRefresh)
}

// RecordPostRun writes a post-run VCS snapshot artifact for an existing run.
func RecordPostRun(ctx context.Context, store *runstore.Store, runID string, opts Options) (runstore.ArtifactRef, Snapshot, error) {
	if store == nil {
		return runstore.ArtifactRef{}, Snapshot{}, errors.New("run store is required")
	}
	snapshot, err := InspectPostRun(ctx, opts)
	if err != nil {
		return runstore.ArtifactRef{}, snapshot, err
	}
	ref, err := WriteSnapshot(ctx, store, runID, "vcs-post-run", snapshot, opts.Time)
	return ref, snapshot, err
}

// WriteSnapshot persists a VCS snapshot as a run-store JSON snapshot artifact.
func WriteSnapshot(ctx context.Context, store *runstore.Store, runID, name string, snapshot Snapshot, at time.Time) (runstore.ArtifactRef, error) {
	content, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return runstore.ArtifactRef{}, fmt.Errorf("marshal VCS snapshot: %w", err)
	}
	content = append(content, '\n')
	return store.WriteArtifactContext(ctx, runID, runstore.Artifact{
		Kind:    runstore.KindSnapshot,
		Name:    name,
		Content: content,
		Time:    at,
	})
}

func inspect(ctx context.Context, opts Options, phase string) (Snapshot, error) {
	if opts.Root == "" {
		return Snapshot{}, errors.New("project root is required")
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	if ok, err := probeVCS(ctx, opts.Root, env, jjRootCommand); err != nil {
		return Snapshot{}, err
	} else if ok {
		return inspectJJ(ctx, opts.Root, env, phase)
	}
	if ok, err := probeVCS(ctx, opts.Root, env, gitRootCommand); err != nil {
		return Snapshot{}, err
	} else if ok {
		return inspectGit(ctx, opts.Root, env, phase)
	}
	return Snapshot{
		SchemaVersion: schemaVersion,
		Phase:         phase,
		Kind:          KindNone,
		Dirty:         false,
		Summary:       "No supported VCS detected.",
		ChangedPaths:  []string{},
		Commands: [][]string{
			jjRootCommand,
			gitRootCommand,
		},
	}, nil
}

func probeVCS(ctx context.Context, root string, env, command []string) (bool, error) {
	output, err := runCommand(ctx, root, env, command)
	if err == nil {
		return strings.TrimSpace(output) != "", nil
	}
	if vcsProbeUnavailable(err) {
		return false, nil
	}
	return false, err
}

func inspectJJ(ctx context.Context, root string, env []string, phase string) (Snapshot, error) {
	output, err := runCommand(ctx, root, env, jjStatusCommand)
	if err != nil {
		return Snapshot{}, err
	}
	changed := parseJJChangedPaths(output)
	dirty := len(changed) > 0 || !strings.Contains(output, "The working copy has no changes.")
	return Snapshot{
		SchemaVersion: schemaVersion,
		Phase:         phase,
		Kind:          KindJJ,
		Dirty:         dirty,
		Summary:       strings.TrimSpace(output),
		ChangedPaths:  changed,
		Commands: [][]string{
			jjRootCommand,
			jjStatusCommand,
		},
	}, nil
}

func inspectGit(ctx context.Context, root string, env []string, phase string) (Snapshot, error) {
	output, err := runCommand(ctx, root, env, gitStatusCommand)
	if err != nil {
		return Snapshot{}, err
	}
	changed := parseGitChangedPathsZ(output)
	return Snapshot{
		SchemaVersion: schemaVersion,
		Phase:         phase,
		Kind:          KindGit,
		Dirty:         len(changed) > 0,
		Summary:       gitSummary(changed),
		ChangedPaths:  changed,
		Commands: [][]string{
			jjRootCommand,
			gitRootCommand,
			gitStatusCommand,
		},
	}, nil
}

func runCommand(ctx context.Context, root string, env, command []string) (string, error) {
	if len(command) == 0 {
		return "", errors.New("command is required")
	}
	executable, err := lookPathEnv(command[0], env)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.Join(command, " "), err)
	}
	// #nosec G204 -- VCS inspector executes fixed read-only commands.
	cmd := exec.CommandContext(ctx, executable, command[1:]...)
	cmd.Dir = root
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("%s: %w", strings.Join(command, " "), ctxErr)
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", commandError{
			command: strings.Join(command, " "),
			err:     err,
			message: message,
		}
	}
	return string(out), nil
}

type commandError struct {
	command string
	err     error
	message string
}

func (err commandError) Error() string {
	if err.message == "" {
		return err.command + ": " + err.err.Error()
	}
	return err.command + ": " + err.message
}

func (err commandError) Unwrap() error {
	return err.err
}

func vcsProbeUnavailable(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) {
		return false
	}
	message := strings.ToLower(cmdErr.message)
	if message == "" {
		return false
	}
	return strings.Contains(message, "not a git repository") ||
		strings.Contains(message, "no jj repo") ||
		strings.Contains(message, "no jj repository") ||
		strings.Contains(message, "not a jj repo") ||
		strings.Contains(message, "not a jj repository")
}

func lookPathEnv(file string, env []string) (string, error) {
	if strings.Contains(file, string(filepath.Separator)) {
		return file, nil
	}
	pathValue := os.Getenv("PATH")
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "PATH" {
			pathValue = value
			break
		}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, file)
		info, err := os.Stat(candidate) // #nosec G304,G703 -- PATH entries come from the caller's execution environment for fixed jj/git command names.
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", exec.ErrNotFound
}

func parseJJChangedPaths(output string) []string {
	var paths []string
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			trimmed == "Working copy changes:" ||
			strings.HasPrefix(trimmed, "Working copy ") ||
			strings.HasPrefix(trimmed, "Parent commit") ||
			strings.HasPrefix(trimmed, "The working copy has no changes.") {
			continue
		}
		if path, ok := jjChangedPath(trimmed); ok {
			paths = append(paths, path)
		}
	}
	return uniqueSorted(paths)
}

func jjChangedPath(line string) (string, bool) {
	for _, prefix := range []string{"M ", "A ", "D ", "R ", "C ", "? ", "! "} {
		if path, ok := strings.CutPrefix(line, prefix); ok {
			path = strings.TrimSpace(path)
			return path, path != ""
		}
	}
	return "", false
}

func parseGitChangedPathsZ(output string) []string {
	var paths []string
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		path := record[3:]
		if record[0] == 'R' || record[1] == 'R' || record[0] == 'C' || record[1] == 'C' {
			paths = append(paths, path)
			if i+1 < len(records) && records[i+1] != "" {
				paths = append(paths, records[i+1])
				i++
			}
			continue
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return uniqueSorted(paths)
}

func gitSummary(changed []string) string {
	if len(changed) == 0 {
		return "Git working copy has no changes."
	}
	if len(changed) == 1 {
		return "Git working copy has 1 changed path."
	}
	return fmt.Sprintf("Git working copy has %d changed paths.", len(changed))
}

func uniqueSorted(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.ToSlash(strings.TrimSpace(path))
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}
