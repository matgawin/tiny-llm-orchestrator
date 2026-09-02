package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

type commit struct {
	SHA     string
	Subject string
	Body    string
}

type releaseNoteOptions struct {
	RepositoryURL string
}

var conventionalSubjectPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*)(\([^)]+\))?(!)?: .+$`)

const shortSHALength = 7

const (
	breakingSectionIndex = iota
	featureSectionIndex
	fixSectionIndex
	performanceSectionIndex
	documentationSectionIndex
	ciSectionIndex
	maintenanceSectionIndex
)

type section struct {
	title   string
	entries []string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "release-notes: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("release-notes", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	repositoryURL := flags.String("repository-url", "", "optional GitHub repository URL used for commit links")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("run: %w", err)
	}

	if flags.NArg() != 1 {
		return errors.New("usage: release-notes [--repository-url URL] <previous_tag..selected_commit>")
	}

	commits, err := gitLog(flags.Arg(0))
	if err != nil {
		return err
	}

	fmt.Print(markdown(commits, releaseNoteOptions{RepositoryURL: *repositoryURL}))

	return nil
}

func gitLog(revisionRange string) ([]commit, error) {
	if err := validateRevisionRange(revisionRange); err != nil {
		return nil, err
	}
	// #nosec G204 -- revisionRange is validated and passed as one non-option git revision argument.
	cmd := exec.CommandContext(context.Background(), "git", "log", "--first-parent", "--reverse", "--format=%H%x00%s%x00%B%x00%x1e", revisionRange, "--")

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git log failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}

		return nil, fmt.Errorf("git log failed: %w", err)
	}

	return parseGitLog(output), nil
}

func validateRevisionRange(revisionRange string) error {
	if revisionRange == "" {
		return errors.New("revision range must not be empty")
	}

	if strings.HasPrefix(revisionRange, "-") {
		return fmt.Errorf("revision range must not start with an option prefix: %q", revisionRange)
	}

	for _, r := range revisionRange {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("revision range contains unsupported whitespace or control character: %q", revisionRange)
		}
	}

	return nil
}

func parseGitLog(output []byte) []commit {
	const gitLogFieldCount = 3

	records := bytes.Split(output, []byte{0x1e})

	commits := make([]commit, 0, len(records))
	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}

		parts := bytes.SplitN(record, []byte{0}, gitLogFieldCount)
		if len(parts) != gitLogFieldCount {
			continue
		}

		commits = append(commits, commit{
			SHA:     string(parts[0]),
			Subject: string(parts[1]),
			Body:    string(bytes.Trim(parts[2], "\x00\n")),
		})
	}

	return commits
}

func markdown(commits []commit, opts releaseNoteOptions) string {
	sections := []section{
		{title: "Breaking Changes"},
		{title: "Features"},
		{title: "Fixes"},
		{title: "Performance"},
		{title: "Documentation"},
		{title: "CI"},
		{title: "Maintenance"},
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

func sectionIndex(commit commit, sections []section) int {
	commitType, subjectBreaking, conventional := parseSubject(commit.Subject)
	if subjectBreaking || hasBreakingFooter(commit.Body) {
		return breakingSectionIndex
	}

	if !conventional {
		return len(sections) - 1
	}

	switch commitType {
	case "feat":
		return featureSectionIndex
	case "fix":
		return fixSectionIndex
	case "perf":
		return performanceSectionIndex
	case "docs":
		return documentationSectionIndex
	case "ci":
		return ciSectionIndex
	case "refactor", "chore", "build", "test":
		return maintenanceSectionIndex
	default:
		return len(sections) - 1
	}
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

func entry(commit commit, repositoryURL string) string {
	shortSHA := commit.SHA
	if len(shortSHA) > shortSHALength {
		shortSHA = shortSHA[:shortSHALength]
	}

	if repositoryURL == "" {
		return fmt.Sprintf("%s (%s)", commit.Subject, shortSHA)
	}

	return fmt.Sprintf("%s ([%s](%s/commit/%s))", commit.Subject, shortSHA, strings.TrimRight(repositoryURL, "/"), commit.SHA)
}
