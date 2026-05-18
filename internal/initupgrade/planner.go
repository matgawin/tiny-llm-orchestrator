package initupgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"tiny-llm-orchestrator/orc/internal/config"
	"tiny-llm-orchestrator/orc/internal/initconfig"
	"tiny-llm-orchestrator/orc/internal/stableerr"

	"github.com/goccy/go-yaml"
)

const (
	configPath    = ".orc/config.yaml"
	gitignorePath = ".gitignore"
	agentsPath    = "AGENTS.md"
	runsDirPath   = ".orc/runs"

	defaultLoopSoftCap = 2
	defaultLoopHardCap = 4
)

// Plan reads the live project setup and returns a no-write upgrade plan.
func Plan(root string) (*Result, error) {
	if root == "" {
		return nil, stableerr.New("project root is required")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("plan init upgrade: %w", err)
	}

	planner := planner{
		root:     absRoot,
		scaffold: scaffoldByPath(),
		result: Result{
			ProjectRoot:         absRoot,
			TargetSetupVersion:  config.CurrentSetupVersion,
			CurrentSetupVersion: 0,
		},
	}

	if err := planner.plan(); err != nil {
		return nil, err
	}

	return &planner.result, nil
}

type planner struct {
	root     string
	scaffold map[string][]byte
	result   Result
	config   configFile
}

func (p *planner) plan() error {
	cfg, err := p.readConfig()
	if err != nil {
		return err
	}

	p.config = cfg
	p.result.ConfigSchemaVersion = cfg.schemaVersion
	p.result.CurrentSetupVersion = cfg.setupVersion

	if warning, ok := config.OlderSetupWarning(cfg.data); ok {
		p.warn("", "older-setup", warning, "")
	}

	if cfg.setupVersion == 0 {
		p.planMigration0To1()
	}

	p.planRunsFollowUp()
	p.sortResult()

	return nil
}

func (p *planner) readConfig() (configFile, error) {
	content, err := p.read(configPath)
	if err != nil {
		return configFile{}, err
	}

	var raw yaml.MapSlice
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return configFile{}, fmt.Errorf("parse %s: %w", configPath, err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return configFile{}, fmt.Errorf("parse %s: %w", configPath, err)
	}

	return configFile{
		content:       content,
		identity:      identity(content),
		schemaVersion: cfg.Version,
		setupVersion:  cfg.SetupVersion,
		data:          cfg,
		doc:           raw,
	}, nil
}

func (p *planner) planMigration0To1() {
	p.planConfigMigration0To1()
	p.planGitignore()
	p.planInstructions()
	p.planRequiredScaffoldFiles()
	p.planStaleFiles()
}

func (p *planner) planConfigMigration0To1() {
	var edits []SurgicalEdit
	if !p.config.has("setup_version") {
		edits = append(edits, SurgicalEdit{Kind: EditAddYAMLField, Path: "setup_version", Value: fmt.Sprint(config.CurrentSetupVersion)})
	} else if p.config.setupVersion < config.CurrentSetupVersion {
		edits = append(edits, SurgicalEdit{Kind: EditSetYAMLField, Path: "setup_version", Value: fmt.Sprint(config.CurrentSetupVersion)})
	}

	if p.config.hasNested("defaults", "max_loops") && !p.config.hasNested("defaults", "loop_caps") {
		value := strings.TrimSpace(p.config.scalarNested("defaults", "max_loops"))
		if value == "" {
			value = strconv.Itoa(defaultLoopSoftCap)
		}

		hard := strconv.Itoa(defaultLoopHardCap)
		if parsed, err := strconv.Atoi(value); err == nil {
			hard = strconv.Itoa(parsed + 1)
		}

		edits = append(edits,
			SurgicalEdit{Kind: EditRemoveYAMLField, Path: "defaults.max_loops"},
			SurgicalEdit{Kind: EditAddYAMLField, Path: "defaults.loop_caps", Value: "enabled: true\nsoft: " + value + "\nhard: " + hard},
		)
	} else if !p.config.hasNested("defaults", "loop_caps") {
		edits = append(edits, SurgicalEdit{Kind: EditAddYAMLField, Path: "defaults.loop_caps", Value: "enabled: true\nsoft: " + strconv.Itoa(defaultLoopSoftCap) + "\nhard: " + strconv.Itoa(defaultLoopHardCap)})
	}

	if p.config.hasNested("defaults", "legacy_runtime") {
		p.conflict(configPath, "deprecated-field", "defaults.legacy_runtime has no unambiguous setup v1 replacement", "remove defaults.legacy_runtime or migrate it to explicit workflow defaults before applying an upgrade")
	}

	if p.config.runtimePath("codex") == "" {
		edits = append(edits, SurgicalEdit{Kind: EditAddYAMLMapEntry, Path: "runtimes", Key: "codex", Value: "runtimes/codex.yaml"})
	} else if p.config.runtimePath("codex") != "runtimes/codex.yaml" {
		p.conflict(configPath, "runtime-reference-conflict", `runtimes.codex does not point at "runtimes/codex.yaml"`, "review the existing Codex runtime reference before applying the setup migration")
	}

	for _, path := range scaffoldConfigEntries(p.scaffold, ".orc/workflows/") {
		name := strings.TrimSuffix(strings.TrimPrefix(path, ".orc/workflows/"), ".yaml")
		if p.config.workflowPath(name) == "" {
			edits = append(edits, SurgicalEdit{Kind: EditAddYAMLMapEntry, Path: "workflows", Key: name, Value: "workflows/" + name + ".yaml"})
		}
	}

	for _, path := range scaffoldConfigEntries(p.scaffold, ".orc/agents/") {
		name := strings.TrimSuffix(strings.TrimPrefix(path, ".orc/agents/"), ".md")
		if p.config.agentPath(name) == "" {
			edits = append(edits, SurgicalEdit{Kind: EditAddYAMLMapEntry, Path: "agents", Key: name, Value: "agents/" + name + ".md"})
		}
	}

	if len(edits) > 0 {
		p.modify(configPath, "migrate project setup metadata and scaffold references to setup version 1", p.config.identity, edits)
	}
}

func (p *planner) planGitignore() {
	content, err := p.read(gitignorePath)
	if errors.Is(err, os.ErrNotExist) {
		p.create(gitignorePath, "create .gitignore with .orc/runs/ ignore entry", []byte(initconfig.RunsIgnoreEntry()+"\n"))
		return
	}

	if err != nil {
		p.conflict(gitignorePath, "read-error", err.Error(), "fix the file permission or filesystem error and rerun orc init upgrade")
		return
	}

	hasRuns, hasBroad, broad := initconfig.AnalyzeRunsIgnoreContent(string(content))
	if hasBroad {
		p.conflict(gitignorePath, "broad-orc-ignore", fmt.Sprintf("%s ignores all persistent .orc config with %q", gitignorePath, broad), "replace the broad .orc ignore pattern with .orc/runs/ and rerun orc init upgrade")
		return
	}

	if hasRuns {
		return
	}

	p.modify(gitignorePath, "append .orc/runs/ ignore entry", identity(content), []SurgicalEdit{{Kind: EditAppendLine, Value: initconfig.RunsIgnoreEntry()}})
}

func (p *planner) planInstructions() {
	content, err := p.read(agentsPath)
	if errors.Is(err, os.ErrNotExist) {
		p.create(agentsPath, "create Tiny Orc guidance", []byte(initconfig.InstructionsContent()))
		return
	}

	if err != nil {
		p.conflict(agentsPath, "read-error", err.Error(), "fix the file permission or filesystem error and rerun orc init upgrade")
		return
	}

	if strings.Contains(string(content), initconfig.InstructionsHeading()) {
		p.warn(agentsPath, "tiny-orc-section-present", "AGENTS.md already contains a Tiny Orc section; v1 will not rewrite or merge it", "leave the section as-is or edit it manually")
		return
	}

	p.modify(agentsPath, "append Tiny Orc guidance section", identity(content), []SurgicalEdit{{Kind: EditAppendSection, Value: initconfig.InstructionsContent()}})
}

func (p *planner) planRequiredScaffoldFiles() {
	for path, content := range p.scaffold {
		if path == configPath || isRunsPath(path) {
			continue
		}

		existing, err := p.read(path)
		if errors.Is(err, os.ErrNotExist) {
			p.create(path, "create missing setup v1 scaffold file", content)
			continue
		}

		if err != nil {
			p.conflict(path, "read-error", err.Error(), "fix the file permission or filesystem error and rerun orc init upgrade")
			continue
		}

		if bytes.Equal(existing, content) {
			continue
		}

		if replacementBaselineMatches(path, existing) {
			p.modify(path, "replace known setup v0 scaffold baseline with setup v1 scaffold content", identity(existing), []SurgicalEdit{{Kind: EditReplaceIfBaseline, Value: string(content)}})
			continue
		}

		p.conflict(path, "customized-scaffold-file", "existing scaffold file differs from known safe setup migration baselines", "review the file manually; the planner will not overwrite customized content")
	}
}

func (p *planner) planStaleFiles() {
	for _, removed := range removedManagedScaffoldFiles {
		path := filepath.Join(p.root, filepath.FromSlash(removed.Path))

		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			p.result.StaleFiles = append(p.result.StaleFiles, StaleFile(removed))
		}
	}
}

func (p *planner) planRunsFollowUp() {
	info, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(runsDirPath)))
	if err == nil && info.IsDir() {
		p.result.FollowUps = append(p.result.FollowUps, FollowUp{
			Code:    "active-runs",
			Message: "existing runs keep pinned config snapshots; after applying live setup changes, run orc run refresh-config <run-id> for runs that should use the new setup",
		})
	}
}

func (p *planner) create(path, reason string, content []byte) {
	if isRunsPath(path) {
		p.conflict(path, "runs-path-excluded", ".orc/runs is excluded from setup upgrade planning", "do not plan setup upgrades under .orc/runs")
		return
	}

	if info, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(path))); err == nil && info.IsDir() {
		p.conflict(path, "path-conflict", "target path exists as a directory", "move or remove the conflicting directory before applying the upgrade")
		return
	}

	p.result.Actions = append(p.result.Actions, Action{Kind: ActionCreate, Path: path, Reason: reason, Content: append([]byte(nil), content...)})
	p.result.AffectedPaths = append(p.result.AffectedPaths, AffectedPath{Path: path, Exists: false})
}

func (p *planner) modify(path, reason string, fileID FileIdentity, edits []SurgicalEdit) {
	if isRunsPath(path) {
		p.conflict(path, "runs-path-excluded", ".orc/runs is excluded from setup upgrade planning", "do not plan setup upgrades under .orc/runs")
		return
	}

	id := fileID
	p.result.Actions = append(p.result.Actions, Action{Kind: ActionModify, Path: path, Reason: reason, Edits: edits, FileIdentity: &id})
	p.result.AffectedPaths = append(p.result.AffectedPaths, AffectedPath{Path: path, Exists: true, FileIdentity: &id})
}

func (p *planner) warn(path, code, message, guidance string) {
	p.result.Warnings = append(p.result.Warnings, Warning{Path: path, Code: code, Message: message, Guidance: guidance})
}

func (p *planner) conflict(path, code, message, guidance string) {
	p.result.Conflicts = append(p.result.Conflicts, Conflict{Path: path, Code: code, Message: message, Guidance: guidance})
}

func (p *planner) read(path string) ([]byte, error) {
	if isRunsPath(path) {
		return nil, stableerr.Errorf("%s is excluded from setup upgrade planning", path)
	}

	content, err := os.ReadFile(filepath.Join(p.root, filepath.FromSlash(path))) // #nosec G304 -- planner reads project-local paths selected by explicit migrations.
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return content, nil
}

func (p *planner) sortResult() {
	slices.SortFunc(p.result.Actions, func(a, b Action) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(p.result.AffectedPaths, func(a, b AffectedPath) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(p.result.Conflicts, func(a, b Conflict) int { return strings.Compare(a.Path+a.Code, b.Path+b.Code) })
	slices.SortFunc(p.result.Warnings, func(a, b Warning) int { return strings.Compare(a.Path+a.Code, b.Path+b.Code) })
	slices.SortFunc(p.result.StaleFiles, func(a, b StaleFile) int { return strings.Compare(a.Path, b.Path) })
}

func identity(content []byte) FileIdentity {
	sum := sha256.Sum256(content)
	return FileIdentity{Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

func isRunsPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == runsDirPath || strings.HasPrefix(clean, runsDirPath+"/")
}

func scaffoldByPath() map[string][]byte {
	files := initconfig.ScaffoldFiles()

	out := make(map[string][]byte, len(files))
	for _, file := range files {
		out[file.Path] = file.Content
	}

	return out
}

func scaffoldConfigEntries(scaffold map[string][]byte, prefix string) []string {
	var paths []string

	for path := range scaffold {
		if strings.HasPrefix(path, prefix) {
			paths = append(paths, path)
		}
	}

	slices.Sort(paths)

	return paths
}
