# Init Upgrade Reference

## Purpose

Define the `orc init upgrade` contract for upgrading project-local Tiny Orc
setup created by `orc init`.

## Audience

Contributors and maintainers implementing setup upgrade planning, apply
behavior, config schema validation, or scaffold migrations.

## Read This When

- You are changing `orc init upgrade` behavior or output.
- You are adding a schema migration or setup migration.
- You need to distinguish live project setup versions from run-store or config
  snapshot versions.
- You are deciding whether a setup file is safe to update.

## Related Docs

- [configuration.md](configuration.md)
- [configuration-init.md](configuration-init.md)
- [configuration-project.md](configuration-project.md)
- [run-store-layout.md](run-store-layout.md)
- [run-store-operations.md](run-store-operations.md)

## Command Contract

The setup upgrade command is `orc init upgrade`.

Do not introduce a top-level `orc update` command for this feature. Do not
introduce `orc project upgrade`, `orc init --upgrade`, or an otherwise-empty
`orc project` namespace for this feature.

Bare `orc init upgrade` is plan-only and writes nothing. It inspects the live
project setup and reports planned safe changes, warnings, conflicts, affected
paths, stale managed files, and follow-up guidance. V1 intentionally has no
`--dry-run` flag because the bare command is the dry-run behavior.

`orc init upgrade --apply` writes the safe independent subset of the upgrade
plan. Writes require the explicit `--apply` flag. Apply must refuse ambiguous
or risky path-specific writes instead of overwriting whole files, but one
path-specific conflict must not block unrelated safe actions.

`orc init upgrade --json` emits the same planning information as structured
JSON. `orc init upgrade --apply --json` emits structured apply results as well.
JSON output must include at least:

- `status`
- `current_setup_version`
- `target_setup_version`
- `config_schema_version`
- `actions`
- `warnings`
- `conflicts`
- `skipped_actions`
- `stale_files`
- `affected_paths`
- `follow_ups`

Apply JSON must also include written paths and unresolved skipped or refused
writes. JSON and human output must describe the same decisions. Human output
must not claim a fully clean upgrade when any action was skipped or conflicted.
It must report partial apply and list the remaining work.

`status` is a top-level summary string for human and JSON output:

- `current`: no setup writes are needed and no manual refresh work remains.
- `planned`: plan-only output contains safe writes and no skipped actions or
  conflicts.
- `applied`: apply wrote safe changes and no skipped actions or conflicts
  remain.
- `partial`: safe writes are available or were applied, but skipped actions or
  unresolved conflicts remain.
- `blocked`: conflicts prevent apply from writing any safe subset.
- `failed`: a global fatal error prevents planning or apply from deciding a
  safe action set.

## Version Marker

The persistent setup/scaffold version marker is top-level
`.orc/config.yaml` field `setup_version`.

```yaml
version: 1
setup_version: 1
```

`version` remains the project config schema version and is validated by the
config loader against the supported `.orc/config.yaml` schema. `setup_version`
is separate from that schema version and is validated against the setup upgrade
system. It must not be inferred from:

- the Orc CLI semantic version
- run-store `schema_version`
- run config snapshot schema versions
- run config snapshot version directories

Missing `setup_version` means legacy setup version `0` for upgrade planning and
older-setup warning purposes. Missing `setup_version` is not invalid config by
itself.

The current setup version is a named code constant owned by the config or setup
upgrade implementation. V1 defines current setup version `1`. New scaffolded
`.orc/config.yaml` files must include `setup_version: 1`.

## Upgrade Scope

`orc init upgrade` upgrades persistent project-local setup only.

Schema migration surfaces:

- `.orc/config.yaml`
- regular files under `.orc/workflows/**`
- regular files under `.orc/agents/**`
- regular files under `.orc/runtimes/**`
- `.orc/scaffold.lock.yaml` only for ownership metadata migrations

Scaffold refresh and setup surfaces:

- `.orc/scaffold.lock.yaml`
- embedded scaffold files under `.orc/runtimes/`, `.orc/workflows/`, and
  `.orc/agents/` only when ownership proof allows default-content refresh
- `.gitignore` only for `.orc/runs/` ignore handling
- `AGENTS.md` only under the conservative Tiny Orc section policy

Excluded surfaces:

- `.orc/runs/**`
- run snapshots and run-store artifacts
- arbitrary project docs
- deleted or renamed files not covered by an explicit migration
- unknown file types under `.orc` unless a migration explicitly targets them

`.orc/runs/**` is a hard exclusion. The planner and apply path must not inspect
run snapshots as a source of truth for live setup upgrades and must never plan
or write changes under `.orc/runs/**`.

Active runs do not block setup upgrades. Existing runs keep pinned config
snapshots. After applying live setup changes, users can run
`orc run refresh-config <run-id>`. `orc init upgrade` must not refresh runs
automatically.

## Schema Migrations

Schema migrations are the primary file-format compatibility mechanism. They
are separate from scaffold refresh, which is limited to Orc-owned default
content and prose when ownership proof is available.

When a change is needed so old user `.orc` files keep loading, add a schema
migration. When a change updates bundled default agent, workflow, or runtime
content or prose, use scaffold refresh only when the live file has ownership
proof. Scaffold replacement must not be used as a substitute for schema-level
compatibility in user-created or customized files.

Schema migrations run under `orc init upgrade`. There is no separate
`orc project upgrade` command. Test-only migrations must be wired only through
package tests and must not appear in production binaries, runtime output, or
docs.

Each schema migration declares a stable migration id, a target matcher, a
summary, a structural predicate, planned surgical edits, and path-scoped
conflict or skipped behavior. Action reasons include the migration id and
summary, for example
`schema migration <id>: <summary>`.

Schema migration discovery is deterministic. It considers `.orc/config.yaml`
plus files under `.orc/workflows/**`, `.orc/agents/**`, and
`.orc/runtimes/**`, sorted by slash-separated project-relative path.
`.orc/scaffold.lock.yaml` is considered only when an ownership-metadata
migration targets it. Discovery must reject traversal outside the project-local
`.orc` tree and must not recurse into `.orc/runs/**`.

Schema migrations plan against raw YAML or Markdown frontmatter before normal
config validation. They must not require `config.Load` to succeed because the
loader for the current binary can reject an older file format that a migration
needs to repair. Invalid targeted files are path-scoped skipped actions or
conflicts. A valid targeted file can still plan and apply when another targeted
file is invalid.

Existing files are changed only through surgical edits that operate on the
original bytes. YAML edits preserve comments and key order where feasible.
Markdown agent descriptors are migrated only through YAML frontmatter when the
file begins with `---` and has a closing `---`. Bytes after the frontmatter are
preserved exactly. Files without frontmatter are no-ops unless a migration
explicitly documents a different metadata rule. Schema migrations do not
semantically merge Markdown body prose.

### AST YAML Edit Engine

YAML and Markdown-frontmatter schema migrations, plus the setup `0 -> 1`
`.orc/config.yaml` YAML edits, must use the `internal/initupgrade` AST edit
engine. A migration must not pre-render a full YAML or frontmatter
replacement.

The engine supports map-key existence checks, nested map traversal, map entry
add, map entry set, map entry remove, and read-only wildcard map visits. It
does not support list writes in v1.

The renderer preserves comments, key order, unrelated YAML formatting, and
Markdown agent body bytes where the parser retains that information. Changed
nodes can use normalized indentation or scalar spelling. Migration tests must
pin accepted normalization.

Invalid YAML, invalid Markdown frontmatter, and unsupported workflow shapes are
path-scoped migration outcomes. Planning records a skipped action or conflict
for the targeted path and continues evaluating unrelated targeted files.
Apply-time parse or render errors are reported on the action path. They must
not become global process failures when other safe actions can still apply.

Migrations must use the helper for the edited surface: config, workflow,
runtime, agent frontmatter, or scaffold manifest metadata. Schema migrations
and setup `.orc/config.yaml` migrations must not add line-oriented YAML edit
logic or list mutation syntax. Text-only upgrade edits remain outside this
rule: `.gitignore` can use `append_line`, `AGENTS.md` can use
`append_section`, and scaffold refresh can use `replace_if_baseline` when
ownership proof allows replacement.

Structural predicates must be idempotent:

- old field only: migrate with the exact owned edit.
- new field only: no-op.
- old and new fields: path-scoped conflict.
- neither field: no-op unless the migration explicitly defines a default.

User-created or customized files can be schema-migrated automatically only when
a narrow structural predicate matches. A workflow file under
`.orc/workflows/**` does not need to be referenced from `.orc/config.yaml` to be
eligible. The same rule applies to agent and runtime descriptors under their
known subtrees. Unknown file types under `.orc` are ignored by default and do
not create conflicts unless a migration explicitly targets them.

Do not follow or mutate symlinks. Directories, devices, sockets, FIFOs, and
other non-regular files are path-scoped conflicts when an explicit migration
targets them. They are ignored when no migration targets them.

If two schema migrations edit the same path, they can compose only when the
edit engine can apply non-overlapping surgical edits deterministically.
Overlapping field edits, duplicate actions, or schema edits competing with a
whole-file scaffold replacement are path-scoped conflicts that name the
relevant migration id through the action reason, conflict message, or guidance.

### `config-defaults-max-loops-to-loop-caps`

`config-defaults-max-loops-to-loop-caps` is the first production schema
migration ported to the AST edit engine. It targets only `.orc/config.yaml`. It
does not target workflow files, runtime descriptors, agent descriptors,
`.orc/scaffold.lock.yaml`, or anything under `.orc/runs/**`.

The migration reads raw `.orc/config.yaml` YAML before typed project config
validation. It can plan when the current `config.Load` path rejects another
part of the file, as long as the raw YAML exposes the targeted `defaults`
structure safely.

Safe old shape:

```yaml
defaults:
  max_loops: 3
```

The migration removes `defaults.max_loops` and adds:

```yaml
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 4
```

The edit is surgical. It plans `ast_remove_yaml_field defaults.max_loops` and
`ast_add_yaml_field defaults.loop_caps`, then applies those edits through the
AST renderer after the file identity check. Comments and surrounding key order
are preserved where the YAML edit engine can preserve them. The migration is
not limited to scaffold-owned config files, so customized `.orc/config.yaml`
files are eligible when the same structural predicate matches.

Current shape:

```yaml
defaults:
  loop_caps:
    enabled: true
    soft: 3
    hard: 4
```

This is a no-op. A file with neither `defaults.max_loops` nor
`defaults.loop_caps` is also a no-op for this schema migration. The setup
`0 -> 1` migration can still add the built-in `defaults.loop_caps` default when
it owns that separate setup decision.

Ambiguous shape:

```yaml
defaults:
  max_loops: 3
  loop_caps:
    enabled: true
    soft: 3
    hard: 4
```

This reports a path-scoped `schema-migration-conflict` for `.orc/config.yaml`
and leaves that migration unapplied. Remove `defaults.max_loops` after
confirming `defaults.loop_caps` is correct, or remove `defaults.loop_caps` and
rerun `orc init upgrade`.

`defaults.max_loops` must be an integer when it carries a value. Blank
`defaults.max_loops: ""` uses the legacy setup conversion and becomes
`defaults.loop_caps.enabled: true`, `soft: 2`, and `hard: 4`. Other non-integer
values report a path-scoped `schema-migration-conflict`. Replace them manually
with `defaults.loop_caps` integer values before rerunning `orc init upgrade`.

## Scaffold Refresh Source Of Truth

Scaffold refresh handles Orc-owned default content and prose. The current
embedded scaffold can be used for new default file content and for recognizing
known scaffold baselines, but it is not enough to infer semantic migrations or
destructive changes.

Each scaffold refresh must have ownership proof for the live file. Accepted
proof is byte-for-byte equality with the current embedded scaffold, an exact
known scaffold baseline, or a valid `.orc/scaffold.lock.yaml` entry whose hash
matches the live bytes. Refresh rules must not depend on unavailable VCS
history or broad textual similarity.

Scaffold refresh must not perform file-format compatibility work for
user-created or customized `.orc` files. If compatibility requires a structural
YAML or frontmatter edit, implement a schema migration with a narrow predicate
for that structure. Existing scaffold-owned YAML refreshes must use the
narrowest safe write form where feasible, but ownership proof remains required.
Whole-file normalized rewrites of existing files are out of scope. New files
can use scaffold formatting.

Examples:

- User-created workflow schema migration: `.orc/workflows/review.yaml` contains
  `legacy_field: true` and no replacement field. Add a schema migration that
  targets workflow YAML files, plans `schema migration <id>: <summary>`, and
  edits only that field. The workflow does not need a `.orc/config.yaml`
  reference and does not need scaffold ownership.
- Customized scaffold file without ownership proof: `.orc/agents/planner.md`
  differs from the embedded scaffold, differs from every exact known baseline,
  and has no matching manifest hash. `orc init upgrade` preserves the file and
  reports a `customized-scaffold-file` skipped action. Operators compare and
  refresh it manually.
- Scaffold-owned default content refresh: `.orc/workflows/implementation.yaml`
  has a valid `.orc/scaffold.lock.yaml` entry and the live file hash matches
  that entry. When the embedded default workflow prose or content changes,
  `orc init upgrade` can plan a scaffold refresh for the workflow and update
  the manifest entry in the same safe write group.

## Scaffold Ownership Manifest

The scaffold ownership manifest is `.orc/scaffold.lock.yaml`. It records the
setup version and the exact byte identity Orc last wrote for manifest-managed
scaffold files:

```yaml
version: 1
setup_version: 1
files:
  - path: .orc/agents/planner.md
    sha256: <hex sha256 of last Orc-written bytes>
```

Manifest paths are slash-separated project-relative paths sorted
lexicographically. A valid v1 manifest must use `version: 1` and
`setup_version: 1`, matching the current supported setup version. Future and
older `setup_version` values are reported as `invalid-scaffold-manifest` and do
not prove ownership. Hashes are SHA-256 over exact file bytes. Each `sha256`
value must be exactly 64 hexadecimal characters. Uppercase hex is accepted and
normalized to lowercase internally. Empty, wrong-length, or non-hex values make
the manifest invalid. Orc does not normalize line endings, YAML formatting,
comments, or trailing newlines before hashing.

The manifest owns embedded scaffold descriptors under `.orc/agents/`,
`.orc/workflows/`, and `.orc/runtimes/`. It excludes `.orc/config.yaml`,
`.orc/runs/**`, `.gitignore`, and `AGENTS.md`. `.orc/config.yaml` remains
migrated only by explicit semantic or surgical rules. The manifest must not
justify whole-file config replacement.

`.orc/scaffold.lock.yaml` is scaffold ownership metadata only. It is not a
schema migration tracker, and v1 does not add a persistent migration state file.
Schema migrations stay structurally idempotent by inspecting the live file
shape.

`orc init` creates the manifest for new projects. `orc init upgrade` creates it
for existing projects when the target path is missing and the entries can be
derived from safe managed content. Safe manifest entries are current embedded
scaffold files that are byte-equal on disk, files created by the same apply, or
files replaced from an exact known baseline or a valid manifest ownership
proof. Customized or skipped files are not recorded as managed.

For future scaffold refreshes, a valid manifest entry whose `sha256` matches
the live file hash is the primary proof that the file is still Orc-managed. If
the embedded scaffold changes, upgrade planning can replace that file with the
current scaffold content and update its manifest entry in the same plan. If the
live file hash differs from the manifest entry, the file is customized and is
preserved as a `customized-scaffold-file` skipped action unless a narrow
explicit migration rule handles that exact content.

When the manifest is missing, invalid, or incomplete, upgrade planning remains
conservative. Orc falls back to byte-for-byte current scaffold checks, exact
known replacement baselines, missing-file creation, and manual-refresh skipped
items. Invalid manifest data must not make replacement more permissive. If an
existing manifest cannot be parsed or uses an unsupported schema, Orc reports a
path-specific `invalid-scaffold-manifest` conflict and leaves the file
unchanged.

## Safety Rules

Local project customizations are preserved. The upgrader can apply only
unambiguous migrations and additions.

Safe changes include:

- adding a missing `setup_version` marker
- advancing an older `setup_version` marker to the target setup version
- adding structurally unambiguous missing fields
- creating required scaffold files when the target path does not exist
- replacing an exact known scaffold baseline
- replacing a scaffold file whose live bytes match a valid manifest entry
- removing or migrating deprecated fields only when semantics are unambiguous

Unsafe or ambiguous cases become warnings or conflicts with exact operator
guidance. Customized scaffold files become skipped manual-refresh items with
exact operator guidance. The upgrader must not silently overwrite an
unrecognized historical shape when the migration cannot enumerate a safe rule
for it.

Conflict behavior:

- Global fatal errors prevent all writes. These include invalid input or plan
  shape, unreadable required planning state, inability to determine a safe
  actionable subset, internal edit construction errors, or filesystem/VCS
  failures that prevent deciding action safety.
- Action-scoped conflicts block only the affected action and actions that
  depend on it. These include dirty affected paths, changed-during-apply
  identity mismatches, unsafe target paths, symlink parents or targets,
  non-regular existing files, permission-denied writes, semantic config
  conflicts that apply to a specific config edit, and `.orc/runs/**`
  exclusions.
- Customized or unknown existing scaffold-managed files under `.orc/agents/`,
  `.orc/workflows/`, and `.orc/runtimes/` become nonfatal skipped actions with
  stable code `customized-scaffold-file` unless an exact current scaffold match,
  an exact known replacement baseline, or valid manifest hash match applies.
- Path conflicts for missing new files become conflicts.
- Deprecated fields with no unambiguous replacement become warnings or
  conflicts, not silent removals.
- Existing `AGENTS.md` sections headed `## Tiny Orc` are reported as present
  and not merged or rewritten in v1.

Skipped action behavior:

- Skipped actions are planned or applied work that Orc intentionally does not
  write while the command can continue.
- Each skipped action must include `path`, stable `code`, `message`, and
  `guidance`. It must include `action_kind` when it corresponds to a planned
  write, and `depends_on` when a dependency made the action unsafe to write.
- `customized-scaffold-file` means an embedded scaffold-managed file exists but
  differs from the current embedded scaffold, exact known replacement
  baselines, and any valid ownership manifest hash. The guidance must say local
  customization was preserved and that the operator can manually compare the
  file with the current embedded scaffold or docs before refreshing it.
- `dependency-skipped` means a planned write depends on another path that was
  skipped or conflicted. The skipped action records the dependent path in
  `path`, the skipped write kind in `action_kind`, and the blocking path in
  `depends_on`.
- Skipped customized scaffold files are not global apply blockers. Safe
  unrelated actions such as `.orc/config.yaml` surgical edits, `.gitignore`
  updates, and independent missing-file creates can still apply.
- Human and JSON output must list skipped actions separately from warnings and
  conflicts. Human apply output must not claim a fully clean upgrade while
  skipped actions remain.
- Skipped actions do not make `--apply` fail by themselves. True semantic or
  safety conflicts remain conflicts and still make apply report unresolved
  conflicts after writing any independent safe actions.

Missing-file behavior:

- Missing required scaffold files are planned as creates when no path conflict
  exists and the corresponding `.orc/config.yaml` reference is safe.
- A scaffold file create can proceed only when the config already points to the
  scaffold path, or when the config action that establishes the reference is
  also safe and will be applied. If the config reference edit is skipped or
  conflicted, the dependent scaffold create is reported as
  `dependency-skipped` instead of created blindly.
- New file content comes from the current embedded scaffold.

Existing scaffold-file behavior:

- Byte-for-byte equality with the current embedded scaffold is a no-op. No
  action and no skipped item is emitted for that file.
- A valid `.orc/scaffold.lock.yaml` entry whose hash matches the live file is
  the primary ownership proof for automatic scaffold refresh when the embedded
  scaffold changes.
- Exact known replacement baselines remain the automatic replacement mechanism
  for pre-manifest historical scaffolds. When a file exactly matches such a
  baseline, apply can replace it with the current embedded scaffold content
  using the safe baseline edit path.
- Any other existing content under the embedded scaffold-managed
  `.orc/agents/`, `.orc/workflows/`, or `.orc/runtimes/` paths is treated as
  customized and preserved. This includes harmless-looking comments,
  whitespace-only differences, reordered YAML, and known fields unless a narrow
  explicit migration rule owns that exact change.
- `orc init upgrade` does not semantically merge YAML or Markdown scaffold
  files and does not write `.new` merge candidates in v1. Operators refresh
  customized files manually by comparing the preserved local file with the
  current embedded scaffold or docs.
- `.orc/config.yaml` is not a whole-file scaffold replacement target. It remains
  migrated only by explicit surgical YAML rules.
- `.orc/scaffold.lock.yaml` is updated only through the same safe write path as
  other upgrade files. Apply rejects symlink parents or targets, `.orc/runs/**`
  paths, unsafe overwrites, and changed-during-apply identity mismatches.

Stale-file behavior:

- Removed managed scaffold files are reported as stale.
- V1 does not delete stale files by default.
- Do not add `--prune` behavior in v1.

Local-edit behavior:

- Before `--apply`, inspect VCS state for existing files in the safe actionable
  subset.
- If an actionable existing file is dirty before apply, skip or refuse that
  path with a stable path-specific conflict.
- Do not require a clean repository globally.
- Unrelated dirty files must not block apply.
- A newly created untracked target that does not exist yet is not a dirty-file
  conflict.
- Planning must work without VCS.
- `--apply` can proceed without a recognized VCS because `--apply` is explicit.
  It must warn that affected-file dirtiness was not checked.
- Changed-during-apply content verification still applies per action. If one
  target changes after planning, reject that write and continue with unrelated
  safe writes. Earlier successful independent writes are not rolled back. The
  manifest refresh group is `.orc/scaffold.lock.yaml` plus each scaffold file
  refresh action with a reciprocal dependency on the manifest. If a
  manifest-managed scaffold refresh fails after `.orc/scaffold.lock.yaml` or
  another file in that group was written, apply restores already-written
  modified files in the group when their bytes still match the attempted write.
  The failed path is reported as a conflict. Remaining unattempted files in the
  same group are reported as `dependency-skipped`, with the failed path in
  `depends_on`, and are not written against the rolled-back manifest. Unrelated
  independent actions keep the existing partial-apply behavior.
- Do not create backup files by default.

Apply ordering:

- Compute the safe actionable subset and dependency-skipped subset before
  writing.
- Write actions after dependency ordering. Stable path order is the tie-breaker
  for independent actions.
- When manifest refresh actions depend on each other, write
  `.orc/scaffold.lock.yaml` before reciprocal scaffold refresh files, then write
  those scaffold files in stable path order. This keeps the group rollback and
  remaining-group skip behavior deterministic.
- If a later action fails a changed-during-apply or filesystem safety check
  after earlier independent writes succeeded, report partial failure with both
  the written paths and the unresolved conflict.
- `.gitignore` and `AGENTS.md` append/create actions are independent of
  `.orc/config.yaml` unless their own path-specific safety checks fail.

## AGENTS.md Policy

V1 uses the same `## Tiny Orc` heading convention as `orc init`.

- If `AGENTS.md` is missing, plan creation according to the existing init
  instruction-file policy.
- If `AGENTS.md` exists without `## Tiny Orc`, plan appending the Tiny Orc
  section when safe.
- If `AGENTS.md` already contains `## Tiny Orc`, report it as present and do
  not rewrite or merge that section.

Managed block markers can be designed by a later migration. V1 does not infer
ownership of arbitrary prose in an existing Tiny Orc section.

## Older Setup Warning

Newer Orc binaries must detect older live setup/config versions when commands
load live `.orc/config.yaml` from the working project and emit a warning:

```text
warning: project Tiny Orc setup version <current> is older than this orc supports (<target>); run "orc init upgrade" to inspect the upgrade plan
```

Missing `setup_version` renders as `0` for this warning. Commands that read only
pinned run snapshots, such as run inspection or config snapshot readers, must
not load live config only to warn.

## Initial Migration: 0 To 1

Version `0` means legacy setup. It is usually represented by a
`.orc/config.yaml` that lacks top-level `setup_version`, but an explicit
`setup_version: 0` is also planned as legacy setup. Version `1` is the first
setup-upgrade-aware scaffold version and corresponds to the current embedded
scaffold plus `setup_version: 1`.

The `0 -> 1` migration must make an explicit plan decision for these surfaces:

- top-level `.orc/config.yaml` `setup_version`
- `defaults.loop_caps`
- `runtimes.codex` and `.orc/runtimes/codex.yaml`
- currently scaffolded workflow references and workflow files
- currently scaffolded agent references and agent descriptor files
- `.gitignore` `.orc/runs/` handling
- `AGENTS.md` Tiny Orc guidance under the conservative v1 policy

Safe `0 -> 1` rules:

- Add or advance `setup_version: 1` when `.orc/config.yaml` is otherwise
  loadable and the marker is absent or older.
- Add `defaults.loop_caps` only when the field is missing and no existing
  defaults shape conflicts with the built-in default semantics.
- Add `runtimes.codex` and create `.orc/runtimes/codex.yaml` only when Codex is
  the intended effective runtime and the runtime path is absent or already
  points at `runtimes/codex.yaml`.
- A true `runtimes.codex` semantic conflict, such as an existing value that
  points somewhere other than `runtimes/codex.yaml`, blocks `.orc/config.yaml`
  edits and dependent `.orc/runtimes/codex.yaml` creation, but does not block
  unrelated safe actions such as `.gitignore` updates or `AGENTS.md` creation.
- Create missing current scaffold workflow or agent files only when they are
  referenced or explicitly required by the migration and the target path is
  absent.
- Append `.orc/runs/` to `.gitignore` when no equivalent ignore entry exists
  and no broad `.orc` ignore hides persistent config.
- Apply the v1 `AGENTS.md` policy above.

Setup `0 -> 1` edits to `.orc/config.yaml` are planned as AST-backed add, set,
and map-entry edits after raw YAML inspection. The migration does not use
line-oriented YAML helpers or whole-file replacement for `.orc/config.yaml`.
Scaffold refresh can still use whole-file replacement for ownership-proven
default files. `.gitignore` and `AGENTS.md` remain text edits because they are
not YAML migration surfaces.

If a current scaffold file exists and matches the current embedded scaffold, the
migration emits no action or skipped item for that file. If it matches an exact
known replacement baseline, the migration can replace it with the current
embedded scaffold content. If it is customized or unknown and there is no narrow
explicit migration rule, preserve the file and report a skipped
`customized-scaffold-file` manual-refresh item with path, reason, and operator
guidance.
