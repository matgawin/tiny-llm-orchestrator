# Release Builds

## Purpose

Describe the Nix build contract used for local Orc builds and release artifacts.

## Audience

Maintainers preparing or reviewing Orc release automation.

## Read This When

- You need to build the Orc CLI through Nix.
- You need release artifacts to embed a tag-derived version.
- You are changing release packaging or build workflow behavior.

## Related Docs

- [runtime-stack.md](runtime-stack.md)
- [../getting-started/local-development.md](../getting-started/local-development.md)

## Local Build

The canonical local Nix build path is:

```bash
nix build .#orc
```

When `ORC_RELEASE_VERSION` is unset or empty, the Nix package version is `dev`.
The linker-injected CLI version is also `dev`, so the built binary reports:

```bash
./result/bin/orc version
orc dev
```

Plain local builds must not require `--impure`.

## Release Build

Release automation injects the released version through `ORC_RELEASE_VERSION`.
Release tags include the leading `v`, but this environment variable must not.

For a release tag `v1.2.3`, build the release artifact with:

```bash
ORC_RELEASE_VERSION=1.2.3 nix build --impure .#orc
```

The Nix package version and linker-injected CLI version use the same value, so
the built binary reports:

```bash
./result/bin/orc version
orc 1.2.3
```

`ORC_RELEASE_VERSION` must match exact numeric `X.Y.Z` semver shape. Values with
a leading `v`, missing components, prerelease metadata, or build metadata fail
during Nix evaluation before a release artifact is produced.
