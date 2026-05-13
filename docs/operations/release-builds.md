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

The repository `VERSION` file is the canonical binary version source. Plain Nix
builds use that version, so the built binary reports the same value:

```bash
./result/bin/orc version
orc dev
```

Plain local builds must not require `--impure`.

## Release Build

Release automation validates that the exact `vX.Y.Z` release tag matches the
repository `VERSION` file after stripping the leading `v`.

For a release tag `v1.2.3`, build the release artifact with:

```bash
nix build .#orc
```

The Nix package version and linker-injected CLI version use `VERSION`, so
the built binary reports:

```bash
./result/bin/orc version
orc 1.2.3
```

`VERSION` must match exact numeric `X.Y.Z` semver shape. Values with a leading
`v`, missing components, prerelease metadata, build metadata, or extra
whitespace fail before a release artifact is produced.
