package releasenotes

import "testing"

func TestMarkdownGroupsConventionalCommits(t *testing.T) {
	commits := []Commit{
		{SHA: "1111111111111111111111111111111111111111", Subject: "feat(cli): add launch command"},
		{SHA: "2222222222222222222222222222222222222222", Subject: "fix: handle missing config"},
		{SHA: "3333333333333333333333333333333333333333", Subject: "perf: avoid repeated load"},
		{SHA: "4444444444444444444444444444444444444444", Subject: "docs: explain release path"},
		{SHA: "5555555555555555555555555555555555555555", Subject: "ci: pin checkout"},
		{SHA: "6666666666666666666666666666666666666666", Subject: "refactor: split parser"},
		{SHA: "7777777777777777777777777777777777777777", Subject: "style: adjust wording"},
		{SHA: "8888888888888888888888888888888888888888", Subject: "plain old commit"},
	}

	got := Markdown(commits, Options{RepositoryURL: "https://github.com/example/repo"})

	want := `## Release Notes

### Features

- feat(cli): add launch command ([1111111](https://github.com/example/repo/commit/1111111111111111111111111111111111111111))

### Fixes

- fix: handle missing config ([2222222](https://github.com/example/repo/commit/2222222222222222222222222222222222222222))

### Performance

- perf: avoid repeated load ([3333333](https://github.com/example/repo/commit/3333333333333333333333333333333333333333))

### Documentation

- docs: explain release path ([4444444](https://github.com/example/repo/commit/4444444444444444444444444444444444444444))

### CI

- ci: pin checkout ([5555555](https://github.com/example/repo/commit/5555555555555555555555555555555555555555))

### Maintenance

- refactor: split parser ([6666666](https://github.com/example/repo/commit/6666666666666666666666666666666666666666))

### Other Changes

- style: adjust wording ([7777777](https://github.com/example/repo/commit/7777777777777777777777777777777777777777))
- plain old commit ([8888888](https://github.com/example/repo/commit/8888888888888888888888888888888888888888))

### Artifact Build

Artifacts are built and uploaded by the release.published Linux x86_64 workflow after this GitHub Release is published.
`
	if got != want {
		t.Fatalf("Markdown() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMarkdownDetectsBreakingChangesOnce(t *testing.T) {
	commits := []Commit{
		{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Subject: "feat!: replace config schema"},
		{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Subject: "fix(parser)!: reject bad state"},
		{SHA: "cccccccccccccccccccccccccccccccccccccccc", Subject: "feat: add strict mode", Body: "Body text\n\nBREAKING CHANGE: strict mode is now default\n"},
	}

	got := Markdown(commits, Options{})

	want := `## Release Notes

### Breaking Changes

- feat!: replace config schema (aaaaaaa)
- fix(parser)!: reject bad state (bbbbbbb)
- feat: add strict mode (ccccccc)

### Artifact Build

Artifacts are built and uploaded by the release.published Linux x86_64 workflow after this GitHub Release is published.
`
	if got != want {
		t.Fatalf("Markdown() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMarkdownOmitsEmptySectionsButKeepsArtifactBuild(t *testing.T) {
	got := Markdown(nil, Options{})

	want := `## Release Notes

### Artifact Build

Artifacts are built and uploaded by the release.published Linux x86_64 workflow after this GitHub Release is published.
`
	if got != want {
		t.Fatalf("Markdown() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
