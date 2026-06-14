package initupgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"tiny-llm-orchestrator/orc/internal/initconfig"

	"github.com/goccy/go-yaml/ast"
)

const (
	schemaMigrationConflictCode = "schema-migration-conflict"
)

var (
	configDefaultsYAMLPath         = mustYAMLPath("defaults")
	configDefaultsLoopCapsYAMLPath = mustYAMLPath("defaults.loop_caps")
)

var errSchemaMigrationOutsideOrc = errors.New("schema migration path is outside .orc")

type planOptions struct {
	schemaMigrations []schemaMigration
}

type schemaMigration struct {
	ID      string
	Summary string
	Target  func(path string) bool
	Plan    func(schemaMigrationFile) schemaMigrationDecision
}

type schemaMigrationFile struct {
	Path            string
	Content         []byte
	Doc             *yamlASTDocument
	Markdown        bool
	Frontmatter     *yamlASTDocument
	HasFrontmatter  bool
	InvalidYAML     error
	InvalidMarkdown error
}

type schemaMigrationDecision struct {
	Edits    []SurgicalEdit
	Conflict string
	Skipped  string
	Guidance string
}

func productionSchemaMigrations() []schemaMigration {
	return []schemaMigration{
		configDefaultsMaxLoopsToLoopCapsMigration(),
	}
}

const configDefaultsMaxLoopsToLoopCapsMigrationID = "config-defaults-max-loops-to-loop-caps"

func configDefaultsMaxLoopsToLoopCapsMigration() schemaMigration {
	return schemaMigration{
		ID:      configDefaultsMaxLoopsToLoopCapsMigrationID,
		Summary: "migrate defaults.max_loops to defaults.loop_caps",
		Target:  exactSchemaMigrationTarget(configPath),
		Plan: func(file schemaMigrationFile) schemaMigrationDecision {
			if file.InvalidYAML != nil {
				return schemaMigrationDecision{Skipped: "targeted YAML is invalid"}
			}

			defaults, _ := file.Doc.Map(configDefaultsYAMLPath)
			hasMaxLoops := hasYAMLFieldInMap(defaults, "max_loops")
			hasLoopCaps := hasYAMLFieldInMap(defaults, "loop_caps")

			switch {
			case hasMaxLoops && hasLoopCaps:
				return schemaMigrationDecision{
					Conflict: "defaults.max_loops and defaults.loop_caps both exist",
					Guidance: "remove defaults.max_loops after confirming defaults.loop_caps is correct, or remove defaults.loop_caps before rerunning this schema migration",
				}
			case hasMaxLoops:
				value := strings.TrimSpace(yamlScalarString(yamlMapValue(defaults, "max_loops")))
				if value == "" {
					value = strconv.Itoa(defaultLoopSoftCap)
					return schemaMigrationDecision{Edits: maxLoopsToLoopCapsEdits(value, strconv.Itoa(defaultLoopHardCap))}
				}

				soft, err := strconv.Atoi(value)
				if err != nil {
					return schemaMigrationDecision{
						Conflict: "defaults.max_loops is not an integer",
						Guidance: "replace defaults.max_loops with defaults.loop_caps using integer soft and hard values before rerunning orc init upgrade",
					}
				}

				return schemaMigrationDecision{Edits: maxLoopsToLoopCapsEdits(value, strconv.Itoa(soft+1))}
			default:
				return schemaMigrationDecision{}
			}
		},
	}
}

func exactSchemaMigrationTarget(path string) func(string) bool {
	return func(candidate string) bool { return candidate == path }
}

func maxLoopsToLoopCapsEdits(soft, hard string) []SurgicalEdit {
	return []SurgicalEdit{
		{Kind: EditRemoveYAMLField, Path: mustYAMLPath("defaults.max_loops")},
		{Kind: EditAddYAMLField, Path: configDefaultsLoopCapsYAMLPath, Value: "enabled: true\nsoft: " + soft + "\nhard: " + hard},
	}
}

func hasYAMLFieldInMap(doc *ast.MappingNode, key string) bool {
	return mappingValue(doc, key) != nil
}

func yamlMapValue(doc *ast.MappingNode, key string) ast.Node {
	value := mappingValue(doc, key)
	if value == nil {
		return nil
	}

	return value.Value
}

func (p *planner) planSchemaMigrations() {
	paths := p.discoverSchemaMigrationPaths()
	for _, migration := range p.schemaMigrations {
		if migration.ID == "" || migration.Summary == "" || migration.Target == nil || migration.Plan == nil {
			p.conflict(".orc", schemaMigrationConflictCode, "schema migration registry contains an invalid migration definition", "fix the internal migration registry before running orc init upgrade")
			continue
		}

		for _, item := range paths {
			if !migration.Target(item.path) {
				continue
			}

			p.planSchemaMigrationForPath(migration, item)
		}
	}
}

func (p *planner) planSchemaMigrationForPath(migration schemaMigration, item schemaMigrationPath) {
	reason := schemaMigrationReason(migration)
	if item.conflict != "" {
		p.conflict(item.path, schemaMigrationConflictCode, fmt.Sprintf("schema migration %s: %s", migration.ID, item.conflict), "replace the target with a regular project-local file before applying this schema migration")
		return
	}

	content, err := p.read(item.path)
	if err != nil {
		p.conflict(item.path, schemaMigrationConflictCode, fmt.Sprintf("schema migration %s: %v", migration.ID, err), "fix the file before rerunning orc init upgrade")
		return
	}

	file := parseSchemaMigrationFile(item.path, content)

	decision := migration.Plan(file)
	switch {
	case decision.Conflict != "":
		guidance := decision.Guidance
		if guidance == "" {
			guidance = "resolve the ambiguous file state before applying this schema migration"
		}

		p.conflict(item.path, schemaMigrationConflictCode, fmt.Sprintf("schema migration %s: %s", migration.ID, decision.Conflict), guidance)
	case decision.Skipped != "":
		guidance := decision.Guidance
		if guidance == "" {
			guidance = "fix this file before rerunning orc init upgrade"
		}

		p.skip(item.path, "schema-migration-skipped", fmt.Sprintf("schema migration %s: %s", migration.ID, decision.Skipped), guidance, ActionModify, nil)
	case len(decision.Edits) > 0:
		p.modify(item.path, reason, identity(content), decision.Edits)
	}
}

func schemaMigrationReason(migration schemaMigration) string {
	return fmt.Sprintf("schema migration %s: %s", migration.ID, migration.Summary)
}

type schemaMigrationPath struct {
	path     string
	conflict string
}

func (p *planner) discoverSchemaMigrationPaths() []schemaMigrationPath {
	byPath := map[string]schemaMigrationPath{}
	add := func(path, conflict string) {
		clean, err := cleanOrcPath(path)
		if err != nil || isRunsPath(clean) {
			return
		}

		if existing, ok := byPath[clean]; ok && existing.conflict != "" {
			return
		}

		byPath[clean] = schemaMigrationPath{path: clean, conflict: conflict}
	}

	if conflict := p.schemaPathConflict(configPath); conflict == "" || p.anySchemaMigrationTargets(configPath) {
		add(configPath, conflict)
	}

	for _, dir := range []string{".orc/workflows", ".orc/agents", ".orc/runtimes"} {
		p.discoverSchemaMigrationDir(dir, add)
	}

	if p.anySchemaMigrationTargets(initconfig.ScaffoldManifestPath()) {
		if conflict := p.schemaPathConflict(initconfig.ScaffoldManifestPath()); conflict == "" || conflict != "missing" {
			add(initconfig.ScaffoldManifestPath(), conflict)
		}
	}

	paths := make([]schemaMigrationPath, 0, len(byPath))
	for _, item := range byPath {
		if item.conflict == "" && !p.anySchemaMigrationTargets(item.path) {
			continue
		}

		paths = append(paths, item)
	}

	slices.SortFunc(paths, func(a, b schemaMigrationPath) int { return strings.Compare(a.path, b.path) })

	return paths
}

func (p *planner) discoverSchemaMigrationDir(dir string, add func(path, conflict string)) {
	root := filepath.Join(p.root, filepath.FromSlash(dir))
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}

		rel, err := filepath.Rel(p.root, path)
		if err != nil {
			return filepath.SkipDir
		}

		slash := filepath.ToSlash(rel)
		if isRunsPath(slash) {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if slash == dir {
			return nil
		}

		if entry.IsDir() {
			if p.anySchemaMigrationTargets(slash) {
				add(slash, "target is a directory")
			}

			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return filepath.SkipDir
		}

		conflict := ""
		if info.Mode()&os.ModeSymlink != 0 {
			conflict = "target is a symlink"
		} else if !info.Mode().IsRegular() {
			conflict = "target is not a regular file"
		}

		if conflict == "" || p.anySchemaMigrationTargets(slash) {
			add(slash, conflict)
		}

		return nil
	})
}

func (p *planner) schemaPathConflict(path string) string {
	abs := filepath.Join(p.root, filepath.FromSlash(path))

	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}

	if err != nil {
		return err.Error()
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return "target is a symlink"
	}

	if info.IsDir() {
		return "target is a directory"
	}

	if !info.Mode().IsRegular() {
		return "target is not a regular file"
	}

	return ""
}

func (p *planner) anySchemaMigrationTargets(path string) bool {
	for _, migration := range p.schemaMigrations {
		if migration.Target != nil && migration.Target(path) {
			return true
		}
	}

	return false
}

func parseSchemaMigrationFile(path string, content []byte) schemaMigrationFile {
	file := schemaMigrationFile{Path: path, Content: append([]byte(nil), content...)}
	if strings.HasSuffix(path, ".md") {
		file.Markdown = true
		frontmatter, _, ok, err := splitMarkdownFrontmatter(content)
		file.HasFrontmatter = ok
		file.InvalidMarkdown = err

		if ok && err == nil {
			file.Frontmatter, file.InvalidYAML = parseYAMLASTDocument(frontmatter)
		}

		return file
	}

	file.Doc, file.InvalidYAML = parseYAMLASTDocument(content)

	return file
}

func cleanOrcPath(path string) (string, error) {
	clean, err := cleanPlanPath(path)
	if err != nil {
		return "", err
	}

	if clean != ".orc" && !strings.HasPrefix(clean, ".orc/") {
		return "", fmt.Errorf("%w: %s", errSchemaMigrationOutsideOrc, path)
	}

	return clean, nil
}
