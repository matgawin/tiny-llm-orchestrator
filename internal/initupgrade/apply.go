package initupgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"tiny-llm-orchestrator/orc/internal/initconfig"
	"tiny-llm-orchestrator/orc/internal/stableerr"
	"tiny-llm-orchestrator/orc/internal/vcs"
)

const (
	filePermPrivate = 0o600
	dirPermPrivate  = 0o750

	yamlChildIndent           = 2
	yamlGrandchildIndent      = 4
	dependencySkippedCode     = "dependency-skipped"
	dependencySkippedGuidance = "resolve the dependency conflict and rerun orc init upgrade"
	setupVersionField         = "setup_version"
)

// ApplyOptions controls upgrade plan application.
type ApplyOptions struct {
	Env []string
}

// ApplyResult describes files written and non-blocking apply guidance.
type ApplyResult struct {
	ProjectRoot          string          `json:"project_root"`
	ConfigSchemaVersion  int             `json:"config_schema_version"`
	PreviousSetupVersion int             `json:"previous_setup_version"`
	TargetSetupVersion   int             `json:"target_setup_version"`
	CreatedPaths         []string        `json:"created_paths"`
	ModifiedPaths        []string        `json:"modified_paths"`
	Warnings             []Warning       `json:"warnings"`
	Conflicts            []Conflict      `json:"conflicts,omitempty"`
	SkippedActions       []SkippedAction `json:"skipped_actions,omitempty"`
	StaleFiles           []StaleFile     `json:"stale_files"`
	FollowUps            []FollowUp      `json:"follow_ups"`
}

// PlanSkippedActions returns explicit skipped actions for a no-write upgrade plan.
func PlanSkippedActions(plan *Result) []SkippedAction {
	if plan == nil {
		return nil
	}

	skipped := append([]SkippedAction(nil), plan.SkippedActions...)
	blocked := blockedPaths(plan.Conflicts, skipped)

	for {
		added := false

		for _, action := range plan.Actions {
			rel, err := cleanPlanPath(action.Path)
			if err != nil {
				continue
			}

			if _, ok := blocked[rel]; ok {
				continue
			}

			for _, dep := range action.DependsOn {
				cleanDep, err := cleanPlanPath(dep)
				if err != nil {
					continue
				}

				if _, ok := blocked[cleanDep]; !ok {
					continue
				}

				skippedAction := dependencySkippedActionFor(action, cleanDep)
				skipped = append(skipped, skippedAction)
				addBlockedSkippedAction(blocked, skippedAction)

				added = true

				break
			}
		}

		if !added {
			break
		}
	}

	return orderedSkippedActions(skipped)
}

// Apply writes the safe actions from a previously generated upgrade plan.
func Apply(ctx context.Context, plan *Result, opts ApplyOptions) (*ApplyResult, error) {
	if plan == nil {
		return nil, stableerr.New("init upgrade plan is required")
	}

	if plan.ProjectRoot == "" {
		return nil, stableerr.New("project root is required")
	}

	root, err := filepath.Abs(plan.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("apply init upgrade: %w", err)
	}

	warnings := append([]Warning(nil), plan.Warnings...)
	conflicts := append([]Conflict(nil), plan.Conflicts...)
	skipped := append([]SkippedAction(nil), plan.SkippedActions...)
	blocked := blockedPaths(conflicts, skipped)

	actions, depSkipped, err := actionableSubset(plan.Actions, blocked)
	if err != nil {
		return nil, err
	}

	skipped = append(skipped, depSkipped...)
	addBlockedSkippedActions(blocked, depSkipped)

	warning, hasWarning, dirtyConflicts, err := checkAffectedPathDirtiness(ctx, root, affectedPathsForActions(actions), opts)
	if err != nil {
		return nil, err
	}

	if hasWarning {
		warnings = append(warnings, warning)
	}

	conflicts = append(conflicts, dirtyConflicts...)
	addBlockedConflicts(blocked, dirtyConflicts)

	actions, depSkipped, err = actionableSubset(actions, blocked)
	if err != nil {
		return nil, err
	}

	skipped = append(skipped, depSkipped...)
	addBlockedSkippedActions(blocked, depSkipped)

	writes, prepareConflicts, err := prepareWritesPartial(root, actions)
	if err != nil {
		return nil, err
	}

	conflicts = append(conflicts, prepareConflicts...)
	addBlockedConflicts(blocked, prepareConflicts)

	writes, depSkipped = filterPreparedWritesByDependencies(writes, actions, blocked)
	skipped = append(skipped, depSkipped...)
	addBlockedSkippedActions(blocked, depSkipped)

	writes = orderPreparedWritesForApply(writes, actions)

	result := &ApplyResult{
		ProjectRoot:          root,
		ConfigSchemaVersion:  plan.ConfigSchemaVersion,
		PreviousSetupVersion: plan.CurrentSetupVersion,
		TargetSetupVersion:   plan.TargetSetupVersion,
		Warnings:             warnings,
		SkippedActions:       skipped,
		StaleFiles:           append([]StaleFile(nil), plan.StaleFiles...),
		FollowUps:            append([]FollowUp(nil), plan.FollowUps...),
	}

	for i, write := range writes {
		if _, ok := blocked[write.relPath]; ok {
			continue
		}

		if blockedByDependency(write.relPath, actions, blocked) {
			skippedAction := dependencySkippedAction(write.relPath, actions)
			result.SkippedActions = append(result.SkippedActions, skippedAction)
			addBlockedSkippedAction(blocked, skippedAction)

			continue
		}

		if err := writePreparedFile(write); err != nil {
			rolledBack, rollbackErr := rollbackManifestRefreshGroupAfterFailure(write, actions, writesAppliedBefore(writes, result))
			if rollbackErr != nil {
				return nil, rollbackErr
			}

			removeModifiedPaths(result, rolledBack)

			conflict, ok := actionConflict(write.relPath, err)
			if !ok {
				return nil, err
			}

			conflicts = append(conflicts, conflict)
			addBlockedConflict(blocked, conflict)

			groupSkipped := skippedRemainingManifestRefreshGroupAfterFailure(write, actions, writes[i+1:], blocked)
			result.SkippedActions = append(result.SkippedActions, groupSkipped...)
			addBlockedSkippedActions(blocked, groupSkipped)

			continue
		}

		switch write.kind {
		case ActionCreate:
			result.CreatedPaths = append(result.CreatedPaths, write.relPath)
		case ActionModify:
			result.ModifiedPaths = append(result.ModifiedPaths, write.relPath)
		}
	}

	result.Conflicts = orderedConflicts(conflicts)

	result.SkippedActions = orderedSkippedActions(result.SkippedActions)
	if len(result.Conflicts) > 0 {
		return result, conflictsError(result.Conflicts)
	}

	return result, nil
}

func writesAppliedBefore(writes []preparedWrite, result *ApplyResult) []preparedWrite {
	applied := make([]preparedWrite, 0, len(result.CreatedPaths)+len(result.ModifiedPaths))
	for _, write := range writes {
		if slices.Contains(result.CreatedPaths, write.relPath) || slices.Contains(result.ModifiedPaths, write.relPath) {
			applied = append(applied, write)
		}
	}

	return applied
}

func removeModifiedPaths(result *ApplyResult, paths []string) {
	for _, path := range paths {
		result.ModifiedPaths = slices.DeleteFunc(result.ModifiedPaths, func(modified string) bool {
			return modified == path
		})
	}
}

type preparedWrite struct {
	kind     ActionKind
	relPath  string
	absPath  string
	root     string
	expected []byte
	next     []byte
}

func checkAffectedPathDirtiness(ctx context.Context, root string, affected []AffectedPath, opts ApplyOptions) (Warning, bool, []Conflict, error) {
	existing, err := existingAffectedPlanPaths(affected)
	if err != nil {
		return Warning{}, false, nil, err
	}

	if len(existing) == 0 {
		return Warning{}, false, nil, nil
	}

	snapshot, err := vcs.InspectPreRun(ctx, vcs.Options{Root: root, Env: opts.Env})
	if err != nil {
		return Warning{}, false, nil, fmt.Errorf("check affected path dirtiness: %w", err)
	}

	if snapshot.Kind == vcs.KindNone {
		return Warning{
			Code:    "no-vcs-dirty-check",
			Message: "no supported VCS detected; skipped affected-file dirty precheck before applying upgrade",
		}, true, nil, nil
	}

	changedKeys, err := changedPathKeys(root, snapshot.RepositoryRoot, existing)
	if err != nil {
		return Warning{}, false, nil, err
	}

	var dirty []Conflict

	for _, changed := range snapshot.ChangedPaths {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(changed)))
		if planPath, ok := changedKeys[clean]; ok {
			dirty = append(dirty, Conflict{
				Path:     planPath,
				Code:     "dirty-affected-path",
				Message:  "affected path has uncommitted VCS changes",
				Guidance: "commit, shelve, or otherwise resolve this path before applying the setup upgrade",
			})
		}
	}

	return Warning{}, false, dirty, nil
}

func affectedPathsForActions(actions []Action) []AffectedPath {
	affected := make([]AffectedPath, 0, len(actions))
	for _, action := range actions {
		if action.Kind != ActionModify {
			continue
		}

		affected = append(affected, AffectedPath{Path: action.Path, Exists: true, FileIdentity: action.FileIdentity})
	}

	return affected
}

func actionableSubset(actions []Action, blocked map[string]struct{}) ([]Action, []SkippedAction, error) {
	var (
		out     []Action
		skipped []SkippedAction
	)

	for _, action := range actions {
		rel, err := cleanPlanPath(action.Path)
		if err != nil {
			return nil, nil, err
		}

		if _, ok := blocked[rel]; ok {
			continue
		}

		var blockedDep string

		for _, dep := range action.DependsOn {
			cleanDep, err := cleanPlanPath(dep)
			if err != nil {
				return nil, nil, err
			}

			if _, ok := blocked[cleanDep]; ok {
				blockedDep = cleanDep
				break
			}
		}

		if blockedDep != "" {
			skipped = append(skipped, dependencySkippedActionFor(action, blockedDep))

			continue
		}

		copyAction := action
		copyAction.Path = rel
		out = append(out, copyAction)
	}

	slices.SortFunc(out, func(a, b Action) int { return strings.Compare(a.Path, b.Path) })

	return out, orderedSkippedActions(skipped), nil
}

func prepareWritesPartial(root string, actions []Action) ([]preparedWrite, []Conflict, error) {
	ordered := append([]Action(nil), actions...)
	slices.SortFunc(ordered, func(a, b Action) int { return strings.Compare(a.Path, b.Path) })

	var (
		writes    = make([]preparedWrite, 0, len(ordered))
		conflicts []Conflict
	)

	for _, action := range ordered {
		rel, err := cleanPlanPath(action.Path)
		if err != nil {
			return nil, nil, err
		}

		if isRunsPath(rel) {
			conflicts = append(conflicts, Conflict{
				Path:     rel,
				Code:     "runs-path-excluded",
				Message:  ".orc/runs is excluded from setup upgrade apply",
				Guidance: "do not plan setup upgrades under .orc/runs",
			})

			continue
		}

		abs := filepath.Join(root, filepath.FromSlash(rel))

		switch action.Kind {
		case ActionCreate:
			write, err := prepareCreateWrite(root, rel, abs, action)
			if err != nil {
				conflict, ok := actionConflict(rel, err)
				if !ok {
					return nil, nil, err
				}

				conflicts = append(conflicts, conflict)

				continue
			}

			writes = append(writes, write)
		case ActionModify:
			write, err := prepareModifyWrite(root, rel, abs, action)
			if err != nil {
				conflict, ok := actionConflict(rel, err)
				if !ok {
					return nil, nil, err
				}

				conflicts = append(conflicts, conflict)

				continue
			}

			writes = append(writes, write)
		default:
			return nil, nil, stableerr.Errorf("%s has unsupported init upgrade action kind %q", rel, action.Kind)
		}
	}

	return writes, orderedConflicts(conflicts), nil
}

func filterPreparedWritesByDependencies(writes []preparedWrite, actions []Action, blocked map[string]struct{}) ([]preparedWrite, []SkippedAction) {
	actionByPath := make(map[string]Action, len(actions))
	for _, action := range actions {
		actionByPath[action.Path] = action
	}

	var (
		out     []preparedWrite
		skipped []SkippedAction
	)

	for _, write := range writes {
		action := actionByPath[write.relPath]

		var blockedDep string

		for _, dep := range action.DependsOn {
			cleanDep, err := cleanPlanPath(dep)
			if err != nil {
				continue
			}

			if _, ok := blocked[cleanDep]; ok {
				blockedDep = cleanDep
				break
			}
		}

		if blockedDep != "" {
			skipped = append(skipped, dependencySkippedActionFor(action, blockedDep))
			continue
		}

		out = append(out, write)
	}

	return out, orderedSkippedActions(skipped)
}

func orderPreparedWritesForApply(writes []preparedWrite, actions []Action) []preparedWrite {
	ordered := append([]preparedWrite(nil), writes...)

	slices.SortStableFunc(ordered, func(a, b preparedWrite) int {
		aManifestGroup := isManifestRefreshGroupPath(a.relPath, actions)

		bManifestGroup := isManifestRefreshGroupPath(b.relPath, actions)
		if aManifestGroup && bManifestGroup {
			if a.relPath == initconfig.ScaffoldManifestPath() {
				return -1
			}

			if b.relPath == initconfig.ScaffoldManifestPath() {
				return 1
			}

			return strings.Compare(a.relPath, b.relPath)
		}

		aDependsOnB := actionDependsOn(actions, a.relPath, b.relPath)
		bDependsOnA := actionDependsOn(actions, b.relPath, a.relPath)

		switch {
		case aDependsOnB && !bDependsOnA:
			return 1
		case bDependsOnA && !aDependsOnB:
			return -1
		}

		if aDependsOnB && bDependsOnA {
			switch {
			case a.relPath == initconfig.ScaffoldManifestPath():
				return -1
			case b.relPath == initconfig.ScaffoldManifestPath():
				return 1
			}
		}

		return strings.Compare(a.relPath, b.relPath)
	})

	return ordered
}

func blockedByDependency(path string, actions []Action, blocked map[string]struct{}) bool {
	for _, action := range actions {
		if action.Path != path {
			continue
		}

		for _, dep := range action.DependsOn {
			cleanDep, err := cleanPlanPath(dep)
			if err != nil {
				continue
			}

			if _, ok := blocked[cleanDep]; ok {
				return true
			}
		}
	}

	return false
}

func dependencySkippedAction(path string, actions []Action) SkippedAction {
	for _, action := range actions {
		if action.Path != path {
			continue
		}

		for _, dep := range action.DependsOn {
			cleanDep, err := cleanPlanPath(dep)
			if err == nil {
				return dependencySkippedActionFor(action, cleanDep)
			}
		}
	}

	return SkippedAction{
		Path:       path,
		Code:       dependencySkippedCode,
		Message:    "planned action depends on a prerequisite action, which was not safe to apply",
		Guidance:   dependencySkippedGuidance,
		ActionKind: ActionModify,
	}
}

func dependencySkippedActionFor(action Action, dep string) SkippedAction {
	rel, err := cleanPlanPath(action.Path)
	if err != nil {
		rel = action.Path
	}

	return SkippedAction{
		Path:       rel,
		Code:       dependencySkippedCode,
		Message:    fmt.Sprintf("planned action depends on %s, which was not safe to apply", dep),
		Guidance:   dependencySkippedGuidance,
		ActionKind: action.Kind,
		DependsOn:  []string{dep},
	}
}

func rollbackManifestRefreshGroupAfterFailure(failed preparedWrite, actions []Action, applied []preparedWrite) ([]string, error) {
	if !actionDependsOn(actions, failed.relPath, initconfig.ScaffoldManifestPath()) {
		return nil, nil
	}

	var rolledBack []string

	for i := len(applied) - 1; i >= 0; i-- {
		write := applied[i]
		if write.kind != ActionModify {
			continue
		}

		if write.relPath != initconfig.ScaffoldManifestPath() && !actionDependsOn(actions, write.relPath, initconfig.ScaffoldManifestPath()) {
			continue
		}

		if err := rollbackModifyWrite(write); err != nil {
			return rolledBack, fmt.Errorf("rollback %s after %s failed: %w", write.relPath, failed.relPath, err)
		}

		rolledBack = append(rolledBack, write.relPath)
	}

	return rolledBack, nil
}

func skippedRemainingManifestRefreshGroupAfterFailure(failed preparedWrite, actions []Action, writes []preparedWrite, blocked map[string]struct{}) []SkippedAction {
	if !isManifestRefreshGroupWrite(failed, actions) {
		return nil
	}

	var skipped []SkippedAction

	for _, write := range writes {
		if write.relPath == failed.relPath {
			continue
		}

		if _, ok := blocked[write.relPath]; ok {
			continue
		}

		if !isManifestRefreshGroupWrite(write, actions) {
			continue
		}

		if action, ok := actionForPath(actions, write.relPath); ok {
			skipped = append(skipped, dependencySkippedActionFor(action, failed.relPath))
			continue
		}

		skipped = append(skipped, SkippedAction{
			Path:       write.relPath,
			Code:       dependencySkippedCode,
			Message:    fmt.Sprintf("planned action depends on %s, which was not safe to apply", failed.relPath),
			Guidance:   dependencySkippedGuidance,
			ActionKind: write.kind,
			DependsOn:  []string{failed.relPath},
		})
	}

	return orderedSkippedActions(skipped)
}

func isManifestRefreshGroupWrite(write preparedWrite, actions []Action) bool {
	return isManifestRefreshGroupPath(write.relPath, actions)
}

func isManifestRefreshGroupPath(path string, actions []Action) bool {
	manifestPath := initconfig.ScaffoldManifestPath()
	if path == manifestPath {
		for _, action := range actions {
			if action.Path != manifestPath && actionDependsOn(actions, action.Path, manifestPath) && actionDependsOn(actions, manifestPath, action.Path) {
				return true
			}
		}

		return false
	}

	return actionDependsOn(actions, path, manifestPath) && actionDependsOn(actions, manifestPath, path)
}

func actionForPath(actions []Action, path string) (Action, bool) {
	for _, action := range actions {
		if action.Path == path {
			return action, true
		}
	}

	return Action{}, false
}

func rollbackModifyWrite(write preparedWrite) error {
	if err := validateSafeExistingFile(write.root, write.relPath); err != nil {
		return err
	}

	current, err := os.ReadFile(write.absPath) // #nosec G304 -- path is constrained to a planned project-local target.
	if err != nil {
		return fmt.Errorf("read %s: %w", write.relPath, err)
	}

	if !bytes.Equal(current, write.next) {
		return stableerr.Errorf("%s changed before init upgrade rollback; leaving it unchanged", write.relPath)
	}

	if err := writeExistingAtomic(write.absPath, write.expected); err != nil {
		return fmt.Errorf("restore %s: %w", write.relPath, err)
	}

	return nil
}

func actionDependsOn(actions []Action, path, dep string) bool {
	for _, action := range actions {
		if action.Path != path {
			continue
		}

		return slices.Contains(action.DependsOn, dep)
	}

	return false
}

func blockedPaths(conflicts []Conflict, skipped []SkippedAction) map[string]struct{} {
	blocked := make(map[string]struct{}, len(conflicts)+len(skipped))
	addBlockedConflicts(blocked, conflicts)
	addBlockedSkippedActions(blocked, skipped)

	return blocked
}

func addBlockedConflicts(blocked map[string]struct{}, conflicts []Conflict) {
	for _, conflict := range conflicts {
		addBlockedConflict(blocked, conflict)
	}
}

func addBlockedConflict(blocked map[string]struct{}, conflict Conflict) {
	if conflict.Path == "" {
		return
	}

	if rel, err := cleanPlanPath(conflict.Path); err == nil {
		blocked[rel] = struct{}{}
	}
}

func addBlockedSkippedActions(blocked map[string]struct{}, skipped []SkippedAction) {
	for _, item := range skipped {
		addBlockedSkippedAction(blocked, item)
	}
}

func addBlockedSkippedAction(blocked map[string]struct{}, skipped SkippedAction) {
	if skipped.Path == "" {
		return
	}

	if rel, err := cleanPlanPath(skipped.Path); err == nil {
		blocked[rel] = struct{}{}
	}
}

func actionConflict(path string, err error) (Conflict, bool) {
	message := err.Error()

	var (
		code     string
		guidance string
	)

	switch {
	case strings.Contains(message, "changed during init upgrade apply"):
		code = "changed-during-apply"
		guidance = "rerun orc init upgrade after resolving concurrent changes to this path"
	case strings.Contains(message, "unsafe symlink parent"):
		code = "unsafe-symlink-parent"
		guidance = "replace the symlink parent with a real project-local directory before applying the upgrade"
	case strings.Contains(message, "unsafe symlink target"):
		code = "unsafe-symlink-target"
		guidance = "replace the symlink target with a regular project-local file before applying the upgrade"
	case strings.Contains(message, "not a regular file"):
		code = "non-regular-file"
		guidance = "replace the target with a regular file before applying the upgrade"
	case strings.Contains(message, "non-directory parent"):
		code = "non-directory-parent"
		guidance = "replace the parent path with a directory before applying the upgrade"
	case errors.Is(err, os.ErrPermission):
		code = "write-permission-denied"
		guidance = "fix path permissions before applying the upgrade"
	case strings.Contains(message, "excluded from setup upgrade apply"):
		code = "runs-path-excluded"
		guidance = "do not plan setup upgrades under .orc/runs"
	default:
		return Conflict{}, false
	}

	return Conflict{Path: path, Code: code, Message: message, Guidance: guidance}, true
}

func existingAffectedPlanPaths(affected []AffectedPath) ([]string, error) {
	var existing []string

	for _, path := range affected {
		if !path.Exists {
			continue
		}

		clean, err := cleanPlanPath(path.Path)
		if err != nil {
			return nil, err
		}

		existing = append(existing, clean)
	}

	slices.Sort(existing)

	return slices.Compact(existing), nil
}

func changedPathKeys(projectRoot, repositoryRoot string, planPaths []string) (map[string]string, error) {
	keys := make(map[string]string, len(planPaths))

	for _, planPath := range planPaths {
		keys[planPath] = planPath
	}

	if repositoryRoot == "" {
		return keys, nil
	}

	repoRoot, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve VCS root: %w", err)
	}

	for _, planPath := range planPaths {
		absPath := filepath.Join(projectRoot, filepath.FromSlash(planPath))

		rel, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			return nil, fmt.Errorf("relativize %s to VCS root: %w", planPath, err)
		}

		clean := filepath.ToSlash(filepath.Clean(rel))
		if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
			continue
		}

		keys[clean] = planPath
	}

	return keys, nil
}

func prepareCreateWrite(root, rel, abs string, action Action) (preparedWrite, error) {
	if len(action.Edits) > 0 {
		return preparedWrite{}, stableerr.Errorf("%s create action cannot contain surgical edits", rel)
	}

	if _, err := os.Lstat(abs); err == nil {
		return preparedWrite{}, stableerr.Errorf("%s changed during init upgrade apply; target already exists", rel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return preparedWrite{}, fmt.Errorf("inspect %s: %w", rel, err)
	}

	if err := validateSafeParentDirs(root, rel); err != nil {
		return preparedWrite{}, err
	}

	return preparedWrite{kind: action.Kind, relPath: rel, absPath: abs, root: root, next: append([]byte(nil), action.Content...)}, nil
}

func prepareModifyWrite(root, rel, abs string, action Action) (preparedWrite, error) {
	if action.FileIdentity == nil {
		return preparedWrite{}, stableerr.Errorf("%s modify action is missing file identity", rel)
	}

	if err := validateSafeExistingFile(root, rel); err != nil {
		return preparedWrite{}, err
	}

	current, err := os.ReadFile(abs) // #nosec G304 -- path is constrained to a planned project-local target.
	if err != nil {
		return preparedWrite{}, fmt.Errorf("read %s: %w", rel, err)
	}

	if !identityMatches(current, *action.FileIdentity) {
		return preparedWrite{}, stableerr.Errorf("%s changed during init upgrade apply; rerun orc init upgrade", rel)
	}

	next, err := applyEditsForPath(rel, current, action.Edits)
	if err != nil {
		return preparedWrite{}, fmt.Errorf("edit %s: %w", rel, err)
	}

	return preparedWrite{kind: action.Kind, relPath: rel, absPath: abs, root: root, expected: current, next: next}, nil
}

func writePreparedFile(write preparedWrite) error {
	if err := validateSafeParentDirs(write.root, write.relPath); err != nil {
		return err
	}

	switch write.kind {
	case ActionCreate:
		if err := os.MkdirAll(filepath.Dir(write.absPath), dirPermPrivate); err != nil {
			return fmt.Errorf("create parent for %s: %w", write.relPath, err)
		}

		if err := validateSafeParentDirs(write.root, write.relPath); err != nil {
			return err
		}

		file, err := os.OpenFile(write.absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePermPrivate) // #nosec G304 -- path is constrained to a planned project-local target.
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return stableerr.Errorf("%s changed during init upgrade apply; target already exists", write.relPath)
			}

			return fmt.Errorf("create %s: %w", write.relPath, err)
		}

		if _, err := file.Write(write.next); err != nil {
			_ = file.Close()
			return fmt.Errorf("create %s: %w", write.relPath, err)
		}

		if err := file.Close(); err != nil {
			return fmt.Errorf("create %s: %w", write.relPath, err)
		}
	case ActionModify:
		if err := validateSafeExistingFile(write.root, write.relPath); err != nil {
			return err
		}

		current, err := os.ReadFile(write.absPath) // #nosec G304 -- path is constrained to a planned project-local target.
		if err != nil {
			return fmt.Errorf("read %s: %w", write.relPath, err)
		}

		if !bytes.Equal(current, write.expected) {
			return stableerr.Errorf("%s changed during init upgrade apply; rerun orc init upgrade", write.relPath)
		}

		if err := writeExistingAtomic(write.absPath, write.next); err != nil {
			return fmt.Errorf("write %s: %w", write.relPath, err)
		}
	}

	return nil
}

func validateSafeParentDirs(root, rel string) error {
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return nil
	}

	current, currentRel := root, ""

	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		if currentRel == "" {
			currentRel = part
		} else {
			currentRel += "/" + part
		}

		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("inspect parent %s: %w", currentRel, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return stableerr.Errorf("%s has unsafe symlink parent %s", rel, currentRel)
		}

		if !info.IsDir() {
			return stableerr.Errorf("%s has non-directory parent %s", rel, currentRel)
		}
	}

	return nil
}

func validateSafeExistingFile(root, rel string) error {
	if err := validateSafeParentDirs(root, rel); err != nil {
		return err
	}

	path := filepath.Join(root, filepath.FromSlash(rel))

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", rel, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return stableerr.Errorf("%s is an unsafe symlink target", rel)
	}

	if !info.Mode().IsRegular() {
		return stableerr.Errorf("%s is not a regular file", rel)
	}

	return nil
}

func writeExistingAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".orc-init-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpPath := tmp.Name()

	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmp.Chmod(filePermPrivate); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

func applyEditsForPath(path string, content []byte, edits []SurgicalEdit) ([]byte, error) {
	if strings.HasSuffix(path, ".md") && hasYAMLEdit(edits) {
		frontmatter, body, ok, err := splitMarkdownFrontmatter(content)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, stableerr.Errorf("%s is missing YAML frontmatter", path)
		}

		nextFrontmatter, err := applyEdits(frontmatter, edits)
		if err != nil {
			return nil, err
		}

		return joinMarkdownFrontmatter(nextFrontmatter, body), nil
	}

	return applyEdits(content, edits)
}

func hasYAMLEdit(edits []SurgicalEdit) bool {
	for _, edit := range edits {
		switch edit.Kind {
		case EditAddYAMLField, EditSetYAMLField, EditRemoveYAMLField, EditAddYAMLMapEntry:
			return true
		case EditAppendLine, EditAppendSection, EditReplaceIfBaseline:
			continue
		}
	}

	return false
}

func splitMarkdownFrontmatter(content []byte) ([]byte, []byte, bool, error) {
	text := string(content)

	var firstEnd int

	switch {
	case strings.HasPrefix(text, "---\n"):
		firstEnd = len("---\n")
	case strings.HasPrefix(text, "---\r\n"):
		firstEnd = len("---\r\n")
	default:
		return nil, content, false, nil
	}

	for _, line := range splitLinesWithOffsets(text[firstEnd:]) {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(line.text, "\n"), "\r")
		if trimmed != "---" {
			continue
		}

		start := firstEnd + line.offset
		end := start + len(line.text)

		return []byte(text[firstEnd:start]), []byte(text[end:]), true, nil
	}

	return nil, nil, true, stableerr.New("YAML frontmatter is missing a closing delimiter")
}

type offsetLine struct {
	offset int
	text   string
}

func splitLinesWithOffsets(text string) []offsetLine {
	if text == "" {
		return nil
	}

	raw := strings.SplitAfter(text, "\n")
	if raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}

	out := make([]offsetLine, 0, len(raw))

	offset := 0

	for _, line := range raw {
		out = append(out, offsetLine{offset: offset, text: line})
		offset += len(line)
	}

	return out
}

func joinMarkdownFrontmatter(frontmatter, body []byte) []byte {
	next := []byte("---\n")
	next = append(next, ensureTrailingNewline(frontmatter)...)
	next = append(next, []byte("---\n")...)
	next = append(next, body...)

	return next
}

func applyEdits(content []byte, edits []SurgicalEdit) ([]byte, error) {
	next := append([]byte(nil), content...)

	for _, edit := range edits {
		var err error

		switch edit.Kind {
		case EditAppendLine:
			next = appendLine(next, edit.Value)
		case EditAppendSection:
			next = appendSection(next, edit.Value)
		case EditReplaceIfBaseline:
			next = []byte(edit.Value)
		case EditAddYAMLField:
			next, err = addYAMLField(next, edit.Path, edit.Value)
		case EditSetYAMLField:
			next, err = setYAMLField(next, edit.Path, edit.Value)
		case EditRemoveYAMLField:
			next, err = removeYAMLField(next, edit.Path)
		case EditAddYAMLMapEntry:
			next, err = addYAMLMapEntry(next, edit.Path, edit.Key, edit.Value)
		default:
			err = stableerr.Errorf("unsupported surgical edit kind %q", edit.Kind)
		}

		if err != nil {
			return nil, err
		}
	}

	return next, nil
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

func addYAMLField(content []byte, path, value string) ([]byte, error) {
	if strings.Contains(path, ".") {
		parent, key, _ := strings.Cut(path, ".")
		return addNestedYAMLField(content, parent, key, value)
	}

	lines := splitLines(content)
	if yamlTopLevelKeyLine(lines, path) >= 0 {
		return nil, stableerr.Errorf("%s already exists", path)
	}

	insert := len(lines)
	if path == setupVersionField {
		if versionLine := yamlTopLevelKeyLine(lines, "version"); versionLine >= 0 {
			insert = versionLine + 1
		}
	}

	line := path + ": " + strings.TrimSpace(value) + "\n"
	lines = slices.Insert(lines, insert, line)

	return []byte(strings.Join(lines, "")), nil
}

func setYAMLField(content []byte, path, value string) ([]byte, error) {
	if strings.Contains(path, ".") {
		return nil, stableerr.Errorf("setting nested YAML field %s is not supported", path)
	}

	lines := splitLines(content)

	idx := yamlTopLevelKeyLine(lines, path)
	if idx < 0 {
		return nil, stableerr.Errorf("%s is missing", path)
	}

	if !isScalarLine(lines[idx]) {
		return nil, stableerr.Errorf("%s is not a scalar field", path)
	}

	line, err := setYAMLScalarLineValue(lines[idx], path, strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}

	lines[idx] = line

	return []byte(strings.Join(lines, "")), nil
}

func setYAMLScalarLineValue(line, path, value string) (string, error) {
	body, lineEnding := trimYAMLLineEnding(line)

	key, rest, ok := strings.Cut(body, ":")
	if !ok || key != path {
		return "", stableerr.Errorf("%s is not a scalar field", path)
	}

	comment := ""
	valuePart := rest

	if commentIdx := yamlInlineCommentIndex(rest); commentIdx >= 0 {
		comment = rest[commentIdx:]
		valuePart = rest[:commentIdx]
	}

	if strings.TrimSpace(valuePart) == "" {
		return "", stableerr.Errorf("%s is not a scalar field", path)
	}

	separator := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
	if separator == "" {
		separator = " "
	}

	return key + ":" + separator + value + comment + lineEnding, nil
}

func trimYAMLLineEnding(line string) (string, string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	case strings.HasSuffix(line, "\n"):
		return strings.TrimSuffix(line, "\n"), "\n"
	default:
		return line, ""
	}
}

func yamlInlineCommentIndex(text string) int {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := range len(text) {
		char := text[i]

		switch {
		case escaped:
			escaped = false
		case inDoubleQuote && char == '\\':
			escaped = true
		case char == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
		case char == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
		case char == '#' && !inSingleQuote && !inDoubleQuote && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t'):
			start := i
			for start > 0 && (text[start-1] == ' ' || text[start-1] == '\t') {
				start--
			}

			return start
		}
	}

	return -1
}

func removeYAMLField(content []byte, path string) ([]byte, error) {
	parent, key, ok := strings.Cut(path, ".")
	if !ok {
		return removeTopLevelYAMLField(content, path)
	}

	lines := splitLines(content)

	parentIdx, end, err := yamlTopLevelSection(lines, parent)
	if err != nil {
		return nil, err
	}

	for i := parentIdx + 1; i < end; i++ {
		if yamlIndentedKey(lines[i], yamlChildIndent) == key {
			removeEnd := i + 1
			for removeEnd < end && leadingSpaces(lines[removeEnd]) > yamlChildIndent {
				removeEnd++
			}

			lines = slices.Delete(lines, i, removeEnd)

			return []byte(strings.Join(lines, "")), nil
		}
	}

	return nil, stableerr.Errorf("%s is missing", path)
}

func removeTopLevelYAMLField(content []byte, key string) ([]byte, error) {
	lines := splitLines(content)

	idx := yamlTopLevelKeyLine(lines, key)
	if idx < 0 {
		return nil, stableerr.Errorf("%s is missing", key)
	}

	end := idx + 1
	for end < len(lines) && !isTopLevelYAMLLine(lines[end]) {
		end++
	}

	lines = slices.Delete(lines, idx, end)

	return []byte(strings.Join(lines, "")), nil
}

func addYAMLMapEntry(content []byte, path, key, value string) ([]byte, error) {
	if key == "" {
		return nil, stableerr.Errorf("%s map entry key is required", path)
	}

	lines := splitLines(content)

	parentIdx, end, err := yamlTopLevelSection(lines, path)
	if err != nil {
		if !errors.Is(err, errYAMLSectionMissing) {
			return nil, err
		}

		next := ensureTrailingNewline(content)
		if len(next) > 0 {
			next = append(next, []byte(path+":\n")...)
		} else {
			next = []byte(path + ":\n")
		}

		next = append(next, []byte("  "+key+": "+value+"\n")...)

		return next, nil
	}

	for i := parentIdx + 1; i < end; i++ {
		if yamlIndentedKey(lines[i], yamlChildIndent) == key {
			return nil, stableerr.Errorf("%s.%s already exists", path, key)
		}
	}

	lines = slices.Insert(lines, end, "  "+key+": "+value+"\n")

	return []byte(strings.Join(lines, "")), nil
}

func addNestedYAMLField(content []byte, parent, key, value string) ([]byte, error) {
	lines := splitLines(content)

	parentIdx, end, err := yamlTopLevelSection(lines, parent)
	if err != nil {
		if !errors.Is(err, errYAMLSectionMissing) {
			return nil, err
		}

		next := ensureTrailingNewline(content)
		next = append(next, []byte(parent+":\n"+nestedYAMLBlock(key, value))...)

		return next, nil
	}

	for i := parentIdx + 1; i < end; i++ {
		if yamlIndentedKey(lines[i], yamlChildIndent) == key {
			return nil, stableerr.Errorf("%s.%s already exists", parent, key)
		}
	}

	insert := nestedYAMLBlock(key, value)
	lines = slices.Insert(lines, end, insert)

	return []byte(strings.Join(lines, "")), nil
}

var errYAMLSectionMissing = errors.New("YAML section missing")

func yamlTopLevelSection(lines []string, key string) (int, int, error) {
	idx := yamlTopLevelKeyLine(lines, key)
	if idx < 0 {
		return -1, -1, errYAMLSectionMissing
	}

	if isScalarLine(lines[idx]) {
		return -1, -1, stableerr.Errorf("%s is not a YAML mapping", key)
	}

	end := idx + 1
	for end < len(lines) && !isTopLevelYAMLLine(lines[end]) {
		end++
	}

	return idx, end, nil
}

func yamlTopLevelKeyLine(lines []string, key string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		if leadingSpaces(line) != 0 {
			continue
		}

		if strings.HasPrefix(line, key+":") {
			return i
		}
	}

	return -1
}

func yamlIndentedKey(line string, indent int) string {
	if leadingSpaces(line) != indent || strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}

	trimmed := strings.TrimSpace(line)

	key, _, ok := strings.Cut(trimmed, ":")
	if !ok || key == "" || strings.Contains(key, " ") {
		return ""
	}

	return key
}

func isTopLevelYAMLLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && !strings.HasPrefix(trimmed, "#") && leadingSpaces(line) == 0
}

func isScalarLine(line string) bool {
	trimmed := strings.TrimSpace(line)

	_, value, ok := strings.Cut(trimmed, ":")

	return ok && strings.TrimSpace(value) != ""
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func splitLines(content []byte) []string {
	text := string(content)
	if text == "" {
		return nil
	}

	raw := strings.SplitAfter(text, "\n")
	if raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}

	return raw
}

func indentBlock(block string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)

	var out strings.Builder

	for line := range strings.SplitSeq(strings.TrimRight(block, "\n"), "\n") {
		out.WriteString(prefix)
		out.WriteString(line)
		out.WriteByte('\n')
	}

	return out.String()
}

func nestedYAMLBlock(key, value string) string {
	var out strings.Builder
	out.WriteString("  ")
	out.WriteString(key)
	out.WriteString(":\n")
	out.WriteString(indentBlock(value, yamlGrandchildIndent))

	return out.String()
}

func cleanPlanPath(path string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." || filepath.IsAbs(path) {
		return "", stableerr.Errorf("unsafe init upgrade path %q", path)
	}

	return clean, nil
}

func identityMatches(content []byte, want FileIdentity) bool {
	got := identity(content)
	return got.Size == want.Size && got.SHA256 == want.SHA256
}

func sameIdentity(a, b FileIdentity) bool {
	return a.Size == b.Size && a.SHA256 == b.SHA256
}

func compatibleSurgicalEdits(existing, next []SurgicalEdit) bool {
	seen := make(map[string]struct{}, len(existing))
	for _, edit := range existing {
		if edit.Kind == EditReplaceIfBaseline {
			return false
		}

		key := surgicalEditKey(edit)

		if key != "" {
			seen[key] = struct{}{}
		}
	}

	for _, edit := range next {
		if edit.Kind == EditReplaceIfBaseline {
			return false
		}

		key := surgicalEditKey(edit)

		if key == "" {
			continue
		}

		if _, ok := seen[key]; ok {
			return false
		}

		seen[key] = struct{}{}
	}

	return true
}

func surgicalEditKey(edit SurgicalEdit) string {
	switch edit.Kind {
	case EditAddYAMLField, EditSetYAMLField, EditRemoveYAMLField:
		return edit.Path
	case EditAddYAMLMapEntry:
		return edit.Path + "." + edit.Key
	case EditAppendLine, EditAppendSection, EditReplaceIfBaseline:
		return ""
	default:
		return ""
	}
}

// ConflictError reports conflicts that prevented an upgrade apply from writing.
type ConflictError struct {
	conflicts []Conflict
}

// Conflicts returns the apply conflicts in stable display order.
func (err *ConflictError) Conflicts() []Conflict {
	if err == nil {
		return nil
	}

	return append([]Conflict(nil), err.conflicts...)
}

func (err *ConflictError) Error() string {
	if err == nil {
		return ""
	}

	parts := make([]string, 0, len(err.conflicts))
	for _, conflict := range err.conflicts {
		parts = append(parts, conflict.Path+" "+conflict.Code+": "+conflict.Message)
	}

	return fmt.Sprintf("init upgrade apply refused due to %d conflict(s): %s", len(err.conflicts), strings.Join(parts, "; "))
}

func conflictsError(conflicts []Conflict) error {
	return &ConflictError{conflicts: orderedConflicts(conflicts)}
}

func orderedConflicts(conflicts []Conflict) []Conflict {
	ordered := append([]Conflict(nil), conflicts...)
	slices.SortFunc(ordered, func(a, b Conflict) int { return strings.Compare(a.Path+a.Code, b.Path+b.Code) })

	return ordered
}

func orderedSkippedActions(skipped []SkippedAction) []SkippedAction {
	ordered := append([]SkippedAction(nil), skipped...)
	slices.SortFunc(ordered, func(a, b SkippedAction) int { return strings.Compare(a.Path+a.Code, b.Path+b.Code) })

	return ordered
}
