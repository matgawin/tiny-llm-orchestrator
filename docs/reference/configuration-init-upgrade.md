# Init Upgrade Reference

## Purpose

Define the `orc init upgrade` contract for upgrading project-local Tiny Orc
setup created by `orc init`.

## Audience

Contributors and maintainers implementing setup upgrade planning, apply
behavior, config schema validation, or scaffold migrations.

## Read This When

- You are changing `orc init upgrade` behavior or output.
- You are adding a setup migration.
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
project setup and reports planned safe changes, warnings, conflicts, stale
managed files, affected paths, and follow-up guidance. V1 intentionally has no
`--dry-run` flag because the bare command is the dry-run behavior.

`orc init upgrade --apply` writes the safe independent subset of the upgrade
plan. Writes require the explicit `--apply` flag. Apply must refuse ambiguous
or risky path-specific writes instead of overwriting whole files, but one
path-specific conflict must not block unrelated safe actions.

`orc init upgrade --json` emits the same planning information as structured
JSON. `orc init upgrade --apply --json` emits structured apply results as well.
JSON output must include at least:

- `current_setup_version`
- `target_setup_version`
- `config_schema_version`
- `actions`
- `warnings`
- `conflicts`
- `stale_files`
- `affected_paths`
- `follow_ups`

Apply JSON must also include written paths and unresolved skipped or refused
writes. JSON and human output must describe the same decisions. Human output
must not claim a fully clean upgrade when any action was skipped or conflicted;
it must report partial apply and list the remaining work.

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

Included surfaces:

- `.orc/config.yaml`
- `.orc/runtimes/*.yaml`
- `.orc/workflows/*.yaml` referenced by `.orc/config.yaml`
- `.orc/agents/*.md` referenced by `.orc/config.yaml`
- `.gitignore` only for `.orc/runs/` ignore handling
- `AGENTS.md` only under the conservative Tiny Orc section policy

Excluded surfaces:

- `.orc/runs/**`
- run snapshots and run-store artifacts
- arbitrary project docs
- user-created unreferenced workflows, agents, or runtime descriptors unless a
  specific migration explicitly owns them
- deleted or renamed files not covered by an explicit migration

`.orc/runs/**` is a hard exclusion. The planner and apply path must not inspect
run snapshots as a source of truth for live setup upgrades and must never plan
or write changes under `.orc/runs/**`.

Active runs do not block setup upgrades. Existing runs keep pinned config
snapshots. After applying live setup changes, users may need
`orc run refresh-config <run-id>`, but `orc init upgrade` must not refresh runs
automatically.

## Migration Source Of Truth

Behavior-changing setup upgrades use explicit versioned migrations. The current
embedded scaffold may be used for new default file content and for recognizing
known scaffold baselines, but it is not enough to infer semantic migrations or
destructive changes.

Each migration must define per-file predicates such as known content hashes,
known old scaffold ids, or narrow structural predicates. Migrations must not
depend on unavailable VCS history or broad textual similarity.

Existing YAML files should be represented as surgical edits where feasible so
comments and key order are preserved. Whole-file normalized rewrites of
existing files are out of scope. New files may use scaffold formatting.

## Safety Rules

Local project customizations are preserved. The upgrader may apply only
unambiguous migrations and additions.

Safe changes include:

- adding a missing `setup_version` marker
- advancing an older `setup_version` marker to the target setup version
- adding structurally unambiguous missing fields
- creating required scaffold files when the target path does not exist
- replacing an exact known scaffold baseline
- removing or migrating deprecated fields only when semantics are unambiguous

Unsafe or ambiguous cases become warnings or conflicts with exact operator
guidance. The upgrader must not silently skip or overwrite an unrecognized
historical shape when the migration cannot enumerate a safe rule for it.

Conflict behavior:

- Global fatal errors prevent all writes. These include invalid input or plan
  shape, unreadable required planning state, inability to determine a safe
  actionable subset, internal edit construction errors, or filesystem/VCS
  failures that prevent deciding action safety.
- Action-scoped conflicts block only the affected action and actions that
  depend on it. These include dirty affected paths, changed-during-apply
  identity mismatches, unsafe target paths, symlink parents or targets,
  non-regular existing files, semantic config conflicts that apply to a
  specific config edit, and `.orc/runs/**` exclusions.
- Customized or unknown existing scaffold files become conflicts or warnings
  unless a migration has a narrow structural rule for them.
- Path conflicts for missing new files become conflicts.
- Deprecated fields with no unambiguous replacement become warnings or
  conflicts, not silent removals.
- Existing `AGENTS.md` sections headed `## Tiny Orc` are reported as present
  and not merged or rewritten in v1.

Missing-file behavior:

- Missing required scaffold files are planned as creates when no path conflict
  exists and the corresponding `.orc/config.yaml` reference is safe.
- A scaffold file create may proceed only when the config already points to the
  scaffold path, or when the config action that establishes the reference is
  also safe and will be applied. If the config reference edit is skipped or
  conflicted, the dependent scaffold create is skipped instead of created
  blindly.
- New file content comes from the current embedded scaffold.

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
- `--apply` may proceed without a recognized VCS because `--apply` is explicit,
  but it must warn that affected-file dirtiness could not be checked.
- Changed-during-apply content verification still applies per action. If one
  target changes after planning, reject that write and continue with unrelated
  safe writes. Earlier successful writes are not rolled back.
- Do not create backup files by default.

Apply ordering:

- Compute the safe actionable subset and dependency-skipped subset before
  writing.
- Write actions in stable path order.
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

If a current scaffold file exists and matches a known baseline, the migration
may replace it or perform a surgical edit defined by that migration. If it is
customized or unknown and there is no narrow structural rule, report a conflict
or warning with path, reason, and operator guidance.
