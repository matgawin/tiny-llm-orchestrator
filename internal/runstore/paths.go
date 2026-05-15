package runstore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const (
	orcDirName    = ".orc"
	runsDirName   = "runs"
	eventsName    = "events.jsonl"
	statusName    = "status.json"
	followupsName = "followups.md"
)

var safeRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type artifactPathSpec struct {
	fixedPath string
	dir       string
	ext       string
}

type artifactSpecEntry struct {
	kind ArtifactKind
	spec artifactPathSpec
}

func generatedRunID(now time.Time, workflow, taskSlug string) (string, error) {
	workflowPart, err := requiredSlugPart(workflow)
	if err != nil {
		return "", fmt.Errorf("workflow slug: %w", err)
	}
	taskPart := slugPart(taskSlug, "task")
	suffix, err := randomHex(3)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s-%s", now.UTC().Format("20060102T150405Z"), workflowPart, taskPart, suffix), nil
}

func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate run id suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func requiredSlugPart(value string) (string, error) {
	slug := slugPart(value, "")
	if slug == "" {
		return "", stableerr.New("must contain at least one ASCII letter or digit")
	}
	return slug, nil
}

func slugPart(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return fallback
	}
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		return fallback
	}
	return slug
}

func validateRunID(id string) error {
	switch {
	case id == "":
		return stableerr.New("run id is required")
	case id == "." || id == "..":
		return stableerr.New("run id must be a filesystem-safe name")
	case strings.ContainsAny(id, `/\`):
		return stableerr.New("run id must not contain path separators")
	case filepath.Clean(id) != id:
		return stableerr.New("run id must be clean")
	case !safeRunIDPattern.MatchString(id):
		return stableerr.New("run id may contain only letters, digits, dot, underscore, and dash")
	default:
		return nil
	}
}

func validateRelativeArtifactPath(path string) error {
	if path == "" {
		return stableerr.New("artifact path is required")
	}
	if strings.Contains(path, `\`) {
		return stableerr.Errorf("artifact path %q must use slash separators", path)
	}
	nativePath := filepath.FromSlash(path)
	clean := filepath.Clean(nativePath)
	if filepath.ToSlash(clean) != path {
		return stableerr.Errorf("artifact path %q must be clean", path)
	}
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return stableerr.Errorf("artifact path %q must stay under run directory", path)
	}
	return nil
}

func artifactPath(kind ArtifactKind, name string, sequence int) (string, error) {
	spec, ok := artifactSpec(kind)
	if !ok {
		return "", stableerr.Errorf("unsupported artifact kind %q", kind)
	}
	if spec.fixedPath != "" {
		return spec.fixedPath, nil
	}
	namePart := slugPart(name, string(kind))
	return numberedArtifactPath(spec.dir, sequence, namePart, spec.ext), nil
}

func numberedArtifactPath(dir string, sequence int, name, ext string) string {
	return filepath.ToSlash(filepath.Join(dir, fmt.Sprintf("%06d-%s%s", sequence, name, ext)))
}

func artifactDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	for _, entry := range artifactSpecs {
		dir := entry.spec.dir
		if dir == "" && entry.spec.fixedPath != "" {
			dir = filepath.ToSlash(filepath.Dir(filepath.FromSlash(entry.spec.fixedPath)))
		}
		if dir == "" || dir == "." || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

var artifactSpecs = []artifactSpecEntry{
	{kind: KindTaskContext, spec: artifactPathSpec{fixedPath: "task/context.md"}},
	{kind: KindTaskSnapshot, spec: artifactPathSpec{fixedPath: "task/snapshot.json"}},
	{kind: KindReport, spec: artifactPathSpec{dir: "reports", ext: ".md"}},
	{kind: KindPrompt, spec: artifactPathSpec{dir: "prompts", ext: ".md"}},
	{kind: KindLog, spec: artifactPathSpec{dir: "logs", ext: ".log"}},
	{kind: KindSnapshot, spec: artifactPathSpec{dir: "snapshots", ext: ".json"}},
	{kind: KindSummary, spec: artifactPathSpec{dir: "summaries", ext: ".md"}},
	{kind: KindFollowup, spec: artifactPathSpec{fixedPath: followupsName}},
}

func artifactSpec(kind ArtifactKind) (artifactPathSpec, bool) {
	for _, entry := range artifactSpecs {
		if entry.kind == kind {
			return entry.spec, true
		}
	}
	return artifactPathSpec{}, false
}
