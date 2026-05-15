package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"tiny-llm-orchestrator/orc/internal/releasenotes"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

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
		return stableerr.New("usage: release-notes [--repository-url URL] <previous_tag..selected_commit>")
	}

	commits, err := gitLog(flags.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(releasenotes.Markdown(commits, releasenotes.Options{RepositoryURL: *repositoryURL}))
	return nil
}

func gitLog(revisionRange string) ([]releasenotes.Commit, error) {
	if err := validateRevisionRange(revisionRange); err != nil {
		return nil, err
	}
	// #nosec G204 -- revisionRange is validated and passed as one non-option git revision argument.
	cmd := exec.CommandContext(context.Background(), "git", "log", "--first-parent", "--reverse", "--format=%H%x00%s%x00%B%x00%x1e", revisionRange, "--")
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, stableerr.Errorf("git log failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git log failed: %w", err)
	}
	return parseGitLog(output), nil
}

func validateRevisionRange(revisionRange string) error {
	if revisionRange == "" {
		return stableerr.New("revision range must not be empty")
	}
	if strings.HasPrefix(revisionRange, "-") {
		return stableerr.Errorf("revision range must not start with an option prefix: %q", revisionRange)
	}
	for _, r := range revisionRange {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return stableerr.Errorf("revision range contains unsupported whitespace or control character: %q", revisionRange)
		}
	}
	return nil
}

func parseGitLog(output []byte) []releasenotes.Commit {
	records := bytes.Split(output, []byte{0x1e})
	commits := make([]releasenotes.Commit, 0, len(records))
	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{0}, 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, releasenotes.Commit{
			SHA:     string(parts[0]),
			Subject: string(parts[1]),
			Body:    string(bytes.Trim(parts[2], "\x00\n")),
		})
	}
	return commits
}
