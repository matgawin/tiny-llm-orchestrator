# Release Workflow

## Purpose

Describe the GitHub Release workflow contract that creates Orc release metadata
and builds uploaded Orc release artifacts.

## Audience

Maintainers publishing or reviewing Orc GitHub Releases.

## Read This When

- You need to create or publish a release that produces downloadable artifacts.
- A release workflow failed before uploading assets.
- You are changing release artifact packaging, naming, or platform scope.

## Related Docs

- [release-builds.md](release-builds.md)
- [runtime-stack.md](runtime-stack.md)
- [../contributing/repository-workflow.md](../contributing/repository-workflow.md)

## Manual Release Creation

Release metadata is prepared by the separate `Create Orc Release`
`workflow_dispatch` workflow. It creates or updates the GitHub Release for tag
`vX.Y.Z` from a selected commit on `origin/main`; the published-release workflow
below remains responsible for building and uploading artifacts after the release
is published.

Manual inputs are:

- `version`: required exact numeric `X.Y.Z` version without a leading `v`;
- `commit`: optional full 40-character commit SHA only; empty resolves
  `origin/main` HEAD;
- `previous_tag`: optional exact `vX.Y.Z` lower-bound tag for release notes;
- `draft`: optional boolean, default `true`;
- `prerelease`: optional boolean, default `false`.

The selected commit must be reachable from `origin/main`. The workflow creates a
lightweight `vX.Y.Z` tag at that commit when missing, continues when the tag
already points at that commit, and fails when the tag points elsewhere. Existing
published releases are not rewritten. Existing draft releases may have their
generated body and prerelease setting updated, but a run with `draft=false`
fails instead of publishing an existing draft as a side effect. New releases use
title `Orc vX.Y.Z` and apply the `draft` and `prerelease` inputs exactly.

Release notes come from first-parent commits in `previous_tag..selected_commit`.
When `previous_tag` is omitted, the workflow discovers the nearest reachable
ancestor tag matching `v[0-9]+.[0-9]+.[0-9]+` on the selected commit's
first-parent history before creating the new tag. If no previous semver tag is
found, the workflow fails and asks the operator to provide `previous_tag`.

Release-note generation groups Conventional Commit subjects into deterministic
sections for breaking changes, features, fixes, performance, documentation, CI,
maintenance, and other changes. Breaking changes are detected from `!` subjects
and `BREAKING CHANGE:` footers and appear only in the breaking section.
Non-conventional commits are included under other changes. Entries are listed
oldest to newest within each section and include the commit subject plus a short
SHA link in GitHub Actions. The local preview path uses plain short SHAs unless
`REPOSITORY_URL` is provided:

```bash
task release-notes-preview RANGE=v1.2.2..HEAD
task release-notes-preview RANGE=v1.2.2..HEAD REPOSITORY_URL=https://github.com/OWNER/REPO
```

Generated release notes replace the managed release body. Maintainers can edit a
draft afterward for custom prose.

## Artifact Trigger

Release artifact automation runs only when a GitHub Release is published. It does
not run on tag push. Manual release creation only prepares metadata and does not
build or upload artifacts.

The workflow builds and uploads the current release artifacts for one platform:
Linux x86_64. This is the only release platform because the flake currently
defines only `x86_64-linux`.

Do not expect macOS, ARM, container, installer, package-manager, raw-binary, or
other platform artifacts from this workflow.

## Release Tag Contract

Publish the GitHub Release from a tag that matches this exact shape:

```text
vX.Y.Z
```

`X`, `Y`, and `Z` must be numeric components. Prerelease labels, build metadata,
missing components, and tags without the leading `v` are invalid for release
artifact publication.

The workflow derives the binary version by stripping the leading `v` from the
release tag. For example, tag `v1.2.3` produces binary version `1.2.3`.

Before building, the workflow resolves the release tag commit and checks that it
is reachable from `origin/main`. Release artifact publication is allowed only
for tags whose commit is in main history.

Invalid tag format or a tag commit that is not reachable from `origin/main`
causes the workflow to fail before building or uploading assets.

## Release Build

Nix is the canonical release artifact build path. For a release tag `v1.2.3`,
the workflow builds the Orc package with:

```bash
ORC_RELEASE_VERSION=1.2.3 nix build --impure .#orc
```

The `ORC_RELEASE_VERSION` value is the tag-derived `X.Y.Z` version without the
leading `v`. The workflow verifies the built binary before packaging by requiring
this exact output:

```bash
./result/bin/orc version
orc 1.2.3
```

For local development, plain Nix builds remain the development path:

```bash
nix build .#orc
```

With no release version injected, the local development build reports:

```bash
./result/bin/orc version
orc dev
```

Taskfile build commands are useful for local development checks, but they are
not the release artifact build path. Release artifacts are produced from the Nix
package with `ORC_RELEASE_VERSION=X.Y.Z nix build --impure .#orc`.

## Uploaded Artifacts

For release tag `v1.2.3`, the workflow uploads exactly these two assets:

```text
orc-v1.2.3-linux-x86_64.tar.gz
orc-v1.2.3-linux-x86_64.tar.gz.sha256
```

The tarball contains these files at the archive root:

```text
orc
LICENSE
```

The checksum file is generated with `sha256sum` against the tarball basename.
For example, the checksum line names `orc-v1.2.3-linux-x86_64.tar.gz`, not an
absolute path.

Re-running the workflow for the same GitHub Release replaces those same two
asset names with `gh release upload --clobber`. It does not create versioned
duplicates for repeated runs of the same release.

## Failure Before Upload

The workflow exits before artifact upload when:

- the GitHub Release tag does not match exact `vX.Y.Z` format;
- the release tag commit is not reachable from `origin/main`;
- the Nix release build fails;
- `./result/bin/orc version` does not print exactly `orc X.Y.Z`.

Failures in tag validation or main-history validation happen before the build
starts. Build failures and binary-version mismatches happen before packaging or
uploading release assets.
