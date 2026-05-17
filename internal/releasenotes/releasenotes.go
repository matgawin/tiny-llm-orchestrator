package releasenotes

import (
	"fmt"
	"regexp"
	"strings"
)

type Commit struct {
	SHA     string
	Subject string
	Body    string
}

type Options struct {
	RepositoryURL string
}

var conventionalSubjectPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*)(\([^)]+\))?(!)?: .+$`)

const shortSHALength = 7

type section struct {
	title   string
	types   map[string]struct{}
	entries []string
}

func Markdown(commits []Commit, opts Options) string {
	sections := []section{
		{title: "Breaking Changes"},
		{title: "Features", types: typeSet("feat")},
		{title: "Fixes", types: typeSet("fix")},
		{title: "Performance", types: typeSet("perf")},
		{title: "Documentation", types: typeSet("docs")},
		{title: "CI", types: typeSet("ci")},
		{title: "Maintenance", types: typeSet("refactor", "chore", "build", "test")},
		{title: "Other Changes"},
	}

	for _, commit := range commits {
		target := sectionIndex(commit, sections)
		sections[target].entries = append(sections[target].entries, entry(commit, opts.RepositoryURL))
	}

	var out strings.Builder
	out.WriteString("## Release Notes\n\n")

	for _, section := range sections {
		if len(section.entries) == 0 {
			continue
		}

		fmt.Fprintf(&out, "### %s\n\n", section.title)

		for _, item := range section.entries {
			fmt.Fprintf(&out, "- %s\n", item)
		}

		out.WriteString("\n")
	}

	out.WriteString("### Artifact Build\n\n")
	out.WriteString("Artifacts are built and uploaded by the release.published Linux x86_64 workflow after this GitHub Release is published.\n")

	return out.String()
}

func sectionIndex(commit Commit, sections []section) int {
	commitType, subjectBreaking, conventional := parseSubject(commit.Subject)
	if subjectBreaking || hasBreakingFooter(commit.Body) {
		return 0
	}

	if conventional {
		for i := 1; i < len(sections)-1; i++ {
			if _, ok := sections[i].types[commitType]; ok {
				return i
			}
		}
	}

	return len(sections) - 1
}

func parseSubject(subject string) (string, bool, bool) {
	matches := conventionalSubjectPattern.FindStringSubmatch(subject)
	if matches == nil {
		return "", false, false
	}

	return strings.ToLower(matches[1]), matches[3] == "!", true
}

func hasBreakingFooter(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "BREAKING CHANGE:") {
			return true
		}
	}

	return false
}

func entry(commit Commit, repositoryURL string) string {
	shortSHA := commit.SHA
	if len(shortSHA) > shortSHALength {
		shortSHA = shortSHA[:shortSHALength]
	}

	if repositoryURL == "" {
		return fmt.Sprintf("%s (%s)", commit.Subject, shortSHA)
	}

	return fmt.Sprintf("%s ([%s](%s/commit/%s))", commit.Subject, shortSHA, strings.TrimRight(repositoryURL, "/"), commit.SHA)
}

func typeSet(types ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(types))
	for _, commitType := range types {
		set[commitType] = struct{}{}
	}

	return set
}
