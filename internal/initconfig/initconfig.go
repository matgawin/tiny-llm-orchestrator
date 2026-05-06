// Package initconfig plans and writes the project-local Tiny Orc scaffold.
package initconfig

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	orcDirName          = ".orc"
	gitignoreName       = ".gitignore"
	instructionsName    = "AGENTS.md"
	instructionsHeading = "## Tiny Orc"
	runsDirPath         = ".orc/runs"
	runsIgnoreEntry     = ".orc/runs/"
)

var errUserDeclined = errors.New("user declined")

//go:embed all:scaffold/.orc
var scaffoldTemplates embed.FS

// Options controls scaffold execution.
type Options struct {
	Root   string
	DryRun bool
	Yes    bool
	Stdin  io.Reader
	Stdout io.Writer
}

// Run creates or previews the Tiny Orc project scaffold.
func Run(opts Options) error {
	if opts.Root == "" {
		return errors.New("project root is required")
	}
	if opts.DryRun && opts.Yes {
		return errors.New("--dry-run and --yes cannot be used together")
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader("")
	}

	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return err
	}
	realRoot, err := resolveProjectRoot(root)
	if err != nil {
		return err
	}
	runner := runner{
		root:     root,
		realRoot: realRoot,
		dryRun:   opts.DryRun,
		yes:      opts.Yes,
		stdout:   opts.Stdout,
		prompts:  bufio.NewReader(opts.Stdin),
	}
	return runner.run()
}

type runner struct {
	root     string
	realRoot string
	dryRun   bool
	yes      bool
	stdout   io.Writer
	prompts  *bufio.Reader
}

func (r runner) run() error {
	if r.dryRun {
		if _, err := fmt.Fprintln(r.stdout, "orc init dry-run:"); err != nil {
			return err
		}
	}

	actions, err := r.plan()
	if err != nil {
		return err
	}
	for _, action := range actions {
		if err := action.apply(); err != nil {
			return err
		}
		if err := r.report(action.reportAction, action.target); err != nil {
			return err
		}
	}
	return nil
}

type plannedAction struct {
	reportAction string
	target       string
	apply        func() error
}

func (r runner) plan() ([]plannedAction, error) {
	files := scaffoldFiles()
	actions := make([]plannedAction, 0, len(files)+3)
	for _, item := range files {
		action, err := r.planFile(item)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}

	action, err := r.planRuntimeDir()
	if err != nil {
		return nil, err
	}
	actions = append(actions, action)

	action, err = r.planGitignore()
	if err != nil {
		return nil, err
	}
	actions = append(actions, action)

	action, err = r.planInstructions()
	if err != nil {
		return nil, err
	}
	actions = append(actions, action)

	return actions, nil
}

type scaffoldFile struct {
	path    string
	content []byte
}

type targetKind int

const (
	targetExists targetKind = iota
	targetMissing
	targetReadError
)

func (r runner) planFile(item scaffoldFile) (plannedAction, error) {
	target := r.inspectTargetState(item.path)
	switch target.kind {
	case targetExists:
		if bytes.Equal(target.content, item.content) {
			return noopAction("exists", item.path), nil
		}
		if r.dryRun {
			return noopAction("would prompt before overwriting", item.path), nil
		}
		if r.yes {
			return plannedAction{}, fmt.Errorf("%s already exists with different content; rerun without --yes to review the overwrite prompt", item.path)
		}
		ok, err := r.confirm("Overwrite " + item.path + "?")
		if err != nil {
			return plannedAction{}, err
		}
		if !ok {
			return plannedAction{}, fmt.Errorf("%s: %w", item.path, errUserDeclined)
		}
		return plannedAction{
			reportAction: "updated",
			target:       item.path,
			apply: func() error {
				return writeFileIfUnchanged(target.path, target.content, item.content)
			},
		}, nil
	case targetMissing:
		if r.dryRun {
			return noopAction("would create", item.path), nil
		}
		return createdAction(item.path, func() error {
			if err := os.MkdirAll(filepath.Dir(target.path), 0o750); err != nil {
				return err
			}
			return writeNewFile(target.path, item.content)
		}), nil
	default:
		return plannedAction{}, target.err
	}
}

func (r runner) planGitignore() (plannedAction, error) {
	entryTarget := gitignoreName + " entry " + runsIgnoreEntry
	target := r.inspectTargetState(gitignoreName)
	switch target.kind {
	case targetExists:
		analysis := analyzeIgnoreContent(string(target.content), runsIgnoreEntry)
		if analysis.hasBroadOrcIgnore {
			return plannedAction{}, fmt.Errorf("%s ignores all persistent .orc config with %q; replace it with %s and rerun init", gitignoreName, analysis.broadPattern, runsIgnoreEntry)
		}
		if analysis.hasRunsEntry {
			return noopAction("exists", entryTarget), nil
		}
		if r.dryRun {
			return noopAction("would append", entryTarget), nil
		}
		return updatedAction(entryTarget, func() error {
			return writeFileIfUnchanged(target.path, target.content, appendLine(target.content, runsIgnoreEntry))
		}), nil
	case targetMissing:
		if r.dryRun {
			return noopAction("would create", gitignoreName+" with "+runsIgnoreEntry), nil
		}
		if !r.yes {
			ok, err := r.confirm("Create .gitignore with " + runsIgnoreEntry + "?")
			if err != nil {
				return plannedAction{}, err
			}
			if !ok {
				return plannedAction{}, fmt.Errorf("%s: %w", gitignoreName, errUserDeclined)
			}
		}
		return createdAction(gitignoreName, func() error {
			return writeNewFile(target.path, []byte(runsIgnoreEntry+"\n"))
		}), nil
	default:
		return plannedAction{}, target.err
	}
}

func (r runner) planRuntimeDir() (plannedAction, error) {
	path, err := r.targetPath(runsDirPath)
	if err != nil {
		return plannedAction{}, err
	}
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return plannedAction{}, fmt.Errorf("%s already exists and is not a directory", runsDirPath)
		}
		return noopAction("exists", runsDirPath), nil
	case errors.Is(err, os.ErrNotExist):
		if r.dryRun {
			return noopAction("would create", runsDirPath), nil
		}
	default:
		return plannedAction{}, err
	}
	return createdAction(runsDirPath, func() error {
		return os.MkdirAll(path, 0o750)
	}), nil
}

func (r runner) planInstructions() (plannedAction, error) {
	target := r.inspectTargetState(instructionsName)
	switch target.kind {
	case targetExists:
		if strings.Contains(string(target.content), instructionsHeading) {
			return noopAction("exists", instructionsName+" Tiny Orc section"), nil
		}
		action := updatedAction(instructionsName, func() error {
			return writeFileIfUnchanged(target.path, target.content, appendSection(target.content, instructionsContent()))
		})
		return r.planInstructionChange("would prompt before updating", instructionsName+" update", "Append Tiny Orc guidance to AGENTS.md?", action)
	case targetMissing:
		action := createdAction(instructionsName, func() error {
			return writeNewFile(target.path, []byte(instructionsContent()))
		})
		return r.planInstructionChange("would prompt before creating", instructionsName+" creation", "Create AGENTS.md with Tiny Orc guidance?", action)
	default:
		return plannedAction{}, target.err
	}
}

func (r runner) planInstructionChange(dryRunAction, skipTarget, prompt string, action plannedAction) (plannedAction, error) {
	if r.dryRun {
		return noopAction(dryRunAction, instructionsName), nil
	}
	if r.yes {
		return noopAction("skipped", skipTarget), nil
	}
	ok, err := r.confirm(prompt)
	if err != nil {
		return plannedAction{}, err
	}
	if !ok {
		return noopAction("skipped", instructionsName), nil
	}
	return action, nil
}

func createdAction(target string, apply func() error) plannedAction {
	return plannedAction{reportAction: "created", target: target, apply: apply}
}

func updatedAction(target string, apply func() error) plannedAction {
	return plannedAction{reportAction: "updated", target: target, apply: apply}
}

func appendLine(content []byte, line string) []byte {
	next := ensureTrailingNewline(content)
	return append(next, (line + "\n")...)
}

func appendSection(content []byte, section string) []byte {
	next := ensureTrailingNewline(content)
	if len(next) > 0 {
		next = append(next, '\n')
	}
	return append(next, section...)
}

func ensureTrailingNewline(content []byte) []byte {
	next := append([]byte(nil), content...)
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	return next
}

func writeNewFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is resolved under the selected project root.
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s changed during init; rerun init", path)
		}
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeFileIfUnchanged(path string, expected, next []byte) error {
	current, err := os.ReadFile(path) // #nosec G304 -- path is resolved under the selected project root.
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("%s changed during init; rerun init", path)
	}
	return os.WriteFile(path, next, 0o600) // #nosec G703 -- path is resolved under the selected project root.
}

func (r runner) confirm(prompt string) (bool, error) {
	if _, err := fmt.Fprintf(r.stdout, "%s [y/N] ", prompt); err != nil {
		return false, err
	}
	answer, err := r.prompts.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	normalized := strings.ToLower(strings.TrimSpace(answer))
	return normalized == "y" || normalized == "yes", nil
}

func (r runner) report(action, target string) error {
	_, err := fmt.Fprintf(r.stdout, "%s %s\n", action, target)
	return err
}

func noopAction(action, target string) plannedAction {
	return plannedAction{
		reportAction: action,
		target:       target,
		apply:        func() error { return nil },
	}
}

type targetState struct {
	path    string
	content []byte
	kind    targetKind
	err     error
}

func (r runner) inspectTargetState(relPath string) targetState {
	path, err := r.targetPath(relPath)
	if err != nil {
		return targetState{kind: targetReadError, err: err}
	}
	content, readErr := os.ReadFile(path) // #nosec G304 -- path is resolved under the selected project root.
	kind := targetReadError
	if readErr == nil {
		kind = targetExists
	} else if errors.Is(readErr, os.ErrNotExist) {
		kind = targetMissing
	}
	return targetState{
		path:    path,
		content: content,
		kind:    kind,
		err:     readErr,
	}
}

func (r runner) targetPath(relPath string) (string, error) {
	clean, err := cleanScaffoldPath(relPath)
	if err != nil {
		return "", err
	}

	// All paths must stay under the real project root. Scaffold files under
	// .orc also stay under the real .orc subtree once that subtree exists.
	containmentRoot := containmentRootForScaffoldPath(r.realRoot, clean)
	target := filepath.Join(r.root, clean)
	if err := validateExistingAncestor(r.realRoot, containmentRoot, target); err != nil {
		return "", err
	}
	if err := validateExistingTarget(relPath, containmentRoot, target); err != nil {
		return "", err
	}
	return target, nil
}

func cleanScaffoldPath(relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("scaffold path %q must stay under project root", relPath)
	}
	return clean, nil
}

func resolveProjectRoot(root string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return realRoot, nil
}

func validateExistingTarget(relPath, containmentRoot, target string) error {
	info, lstatErr := os.Lstat(target)
	if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return validateResolvedSymlinkTarget(relPath, containmentRoot, target)
	}
	if errors.Is(lstatErr, os.ErrNotExist) {
		return nil
	}
	if lstatErr != nil {
		return lstatErr
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if err := validateUnderRoot(containmentRoot, realTarget); err != nil {
		return fmt.Errorf("%s: %w", relPath, err)
	}
	return nil
}

func validateResolvedSymlinkTarget(relPath, containmentRoot, target string) error {
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("%s: resolve symlink: %w", relPath, err)
	}
	if err := validateUnderRoot(containmentRoot, realTarget); err != nil {
		return fmt.Errorf("%s: %w", relPath, err)
	}
	return nil
}

func containmentRootForScaffoldPath(realRoot, cleanPath string) string {
	if cleanPath == orcDirName || strings.HasPrefix(cleanPath, orcDirName+string(filepath.Separator)) {
		return filepath.Join(realRoot, orcDirName)
	}
	return realRoot
}

func validateExistingAncestor(realRoot, containmentRoot, target string) error {
	ancestor := filepath.Dir(target)
	for {
		if _, err := os.Stat(ancestor); err == nil {
			realAncestor, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return err
			}
			if err := validateUnderRoot(realRoot, realAncestor); err != nil {
				return err
			}
			if containmentRoot == realRoot {
				return nil
			}
			resolvedContainmentRoot, err := filepath.EvalSymlinks(containmentRoot)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			return validateUnderRoot(resolvedContainmentRoot, realAncestor)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return fmt.Errorf("no existing ancestor for %s", target)
		}
		ancestor = next
	}
}

func validateUnderRoot(realRoot, path string) error {
	rel, err := filepath.Rel(realRoot, path)
	if err != nil {
		return fmt.Errorf("resolve path relative to project root: %w", err)
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path must not escape project root")
	}
	return nil
}

type ignoreAnalysis struct {
	hasBroadOrcIgnore bool
	broadPattern      string
	hasRunsEntry      bool
}

func analyzeIgnoreContent(content, runsEntry string) ignoreAnalysis {
	wantRuns := mustNormalizeIgnorePattern(runsEntry)
	var analysis ignoreAnalysis
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		normalized, ok := normalizeIgnorePattern(line)
		if !ok {
			continue
		}
		if normalized == ".orc" && !analysis.hasBroadOrcIgnore {
			analysis.hasBroadOrcIgnore = true
			analysis.broadPattern = strings.TrimSpace(line)
		}
		if normalized == wantRuns {
			analysis.hasRunsEntry = true
		}
	}
	return analysis
}

func mustNormalizeIgnorePattern(pattern string) string {
	normalized, ok := normalizeIgnorePattern(pattern)
	if !ok {
		panic("invalid .gitignore pattern constant: " + pattern)
	}
	return normalized
}

func scaffoldPaths() []string {
	return []string{
		".orc/config.yaml",
		".orc/workflows/implementation.yaml",
		".orc/agents/planner.md",
		".orc/agents/coder.md",
		".orc/agents/tester.md",
		".orc/agents/reviewer.md",
	}
}

func normalizeIgnorePattern(pattern string) (string, bool) {
	normalized := strings.TrimSpace(pattern)
	if normalized == "" || strings.HasPrefix(normalized, "#") {
		return "", false
	}
	normalized = strings.TrimPrefix(normalized, "/")
	normalized = strings.TrimSuffix(normalized, "/**")
	normalized = strings.TrimSuffix(normalized, "/*")
	normalized = strings.TrimSuffix(normalized, "/")
	return normalized, true
}

func scaffoldFiles() []scaffoldFile {
	paths := scaffoldPaths()
	files := make([]scaffoldFile, 0, len(paths))
	for _, path := range paths {
		templatePath := "scaffold/" + path
		content, err := scaffoldTemplates.ReadFile(templatePath)
		if err != nil {
			panic(fmt.Sprintf("read embedded scaffold template %s: %v", templatePath, err))
		}
		files = append(files, scaffoldFile{path: path, content: content})
	}
	return files
}

func instructionsContent() string {
	return instructionsHeading + "\n\n" +
		"- Project-local orchestration config lives under `.orc/`.\n" +
		"- Persistent workflow and role descriptor files are user-owned and reviewable.\n" +
		"- Runtime run state belongs under `.orc/runs/`, which should stay ignored by VCS.\n" +
		"- Use `orc init --dry-run` before changing an existing scaffold.\n"
}
