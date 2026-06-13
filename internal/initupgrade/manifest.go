package initupgrade

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"

	"tiny-llm-orchestrator/orc/internal/initconfig"
)

type scaffoldManifestState struct {
	exists     bool
	valid      bool
	content    []byte
	identity   FileIdentity
	hashByPath map[string]string
}

func (p *planner) readManifest() scaffoldManifestState {
	manifestPath := initconfig.ScaffoldManifestPath()

	content, err := p.read(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return scaffoldManifestState{}
	}

	if err != nil {
		p.conflict(manifestPath, "read-error", err.Error(), "fix the file permission or filesystem error and rerun orc init upgrade")
		return scaffoldManifestState{exists: true}
	}

	state := scaffoldManifestState{
		exists:     true,
		content:    content,
		identity:   identity(content),
		hashByPath: make(map[string]string),
	}

	_, hashes, err := initconfig.ParseScaffoldManifest(content)
	if err != nil {
		p.conflict(manifestPath, "invalid-scaffold-manifest", fmt.Sprintf("parse %s: %v", manifestPath, err), "leave the existing manifest unchanged; Orc will fall back to exact scaffold baselines for upgrade planning")
		return state
	}

	state.valid = true
	state.hashByPath = hashes

	return state
}

func (p *planner) planScaffoldManifest() {
	manifestPath := initconfig.ScaffoldManifestPath()
	if p.hasConflict(manifestPath, "read-error") || p.hasConflict(manifestPath, "invalid-scaffold-manifest") {
		return
	}

	entries, deps := p.safeManifestEntries()
	content := initconfig.ScaffoldManifestContent(entries)

	if p.manifest.exists {
		if p.manifest.valid && bytes.Equal(p.manifest.content, content) {
			return
		}

		if !p.manifest.valid {
			return
		}

		p.modify(manifestPath, "update scaffold ownership manifest", p.manifest.identity, []SurgicalEdit{{Kind: EditReplaceIfBaseline, Value: string(content)}})
		p.result.Actions[len(p.result.Actions)-1].DependsOn = append(p.result.Actions[len(p.result.Actions)-1].DependsOn, deps...)

		return
	}

	action := p.create(manifestPath, "create scaffold ownership manifest", content)
	if action != nil {
		action.DependsOn = append(action.DependsOn, deps...)
	}
}

func (p *planner) safeManifestEntries() ([]initconfig.ScaffoldManifestFile, []string) {
	actionByPath := make(map[string]Action, len(p.result.Actions))
	for _, action := range p.result.Actions {
		actionByPath[action.Path] = action
	}

	skipped := make(map[string]struct{}, len(p.result.SkippedActions))
	for _, skip := range p.result.SkippedActions {
		skipped[skip.Path] = struct{}{}
	}

	var (
		entries []initconfig.ScaffoldManifestFile
		deps    []string
	)

	for _, path := range managedScaffoldPaths(p.scaffold) {
		if _, ok := skipped[path]; ok {
			continue
		}

		content := p.scaffold[path]
		if action, ok := actionByPath[path]; ok {
			switch action.Kind {
			case ActionCreate:
				entries = append(entries, initconfig.ScaffoldManifestFile{Path: path, SHA256: initconfig.SHA256Hex(content)})
				deps = append(deps, path)
			case ActionModify:
				if actionReplacesWithCurrentScaffold(action, content) {
					entries = append(entries, initconfig.ScaffoldManifestFile{Path: path, SHA256: initconfig.SHA256Hex(content)})
					deps = append(deps, path)
				}
			}

			continue
		}

		existing, err := p.read(path)
		if err == nil && bytesEqualSHA(existing, content) {
			entries = append(entries, initconfig.ScaffoldManifestFile{Path: path, SHA256: initconfig.SHA256Hex(content)})
		}
	}

	slices.Sort(deps)
	deps = slices.Compact(deps)

	return entries, deps
}

func (p *planner) manifestProvesManaged(path string, content []byte) bool {
	if !p.manifest.valid {
		return false
	}

	want := p.manifest.hashByPath[path]

	return want != "" && initconfig.SHA256Hex(content) == want
}

func managedScaffoldPaths(scaffold map[string][]byte) []string {
	var paths []string

	for path := range scaffold {
		if initconfig.IsManagedScaffoldManifestPath(path) && !isRunsPath(path) {
			paths = append(paths, path)
		}
	}

	slices.Sort(paths)

	return paths
}

func actionReplacesWithCurrentScaffold(action Action, content []byte) bool {
	if action.Kind != ActionModify || len(action.Edits) != 1 {
		return false
	}

	edit := action.Edits[0]

	return edit.Kind == EditReplaceIfBaseline && edit.Value == string(content)
}

func bytesEqualSHA(a, b []byte) bool {
	return initconfig.SHA256Hex(a) == initconfig.SHA256Hex(b)
}
