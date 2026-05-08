# Sandbox Run

## Purpose

Document the user-visible behavior of `orc sandbox run`.

## Audience

Contributors and operators configuring Orc to launch a command through the
system bubblewrap sandbox wrapper.

## Behavior

`orc sandbox` is a command group. v1 implements:

```bash
orc sandbox run
```

`orc sandbox run` loads `.orc/config.yaml` from the current repository root and
requires an explicit `sandbox.command.argv` list. Orc does not invent a Codex
command, yolo mode, shell string, or unsandboxed fallback when this config is
missing or invalid.

The command is launched by shelling out to the system `bwrap` binary. Bubblewrap
must be installed and available on `PATH`; if it is missing, Orc fails with
install guidance and does not start the configured command outside a sandbox.

Sandbox execution is Linux-only in v1. Other platforms fail before looking for
or starting `bwrap`.

## Child Process

The child process starts in `sandbox.cwd`, which defaults to the repository
root and is validated by the config loader as a repo-relative existing
directory.

Orc keeps stdin, stdout, and stderr attached to the sandboxed process so
interactive worker sessions remain usable. If the sandboxed command exits
nonzero, Orc exits with the same child status.

Termination from the parent Orc process is forwarded to the sandbox process
group. The v1 bubblewrap argv preserves the current effective UID/GID; it does
not use fake-root or ownership remapping.

Orc sets these marker variables in the sandboxed environment:

- `ORC_SANDBOX=1`
- `ORC_SANDBOX_ROOT=<repo>`

Worker launches inherit sandboxing through normal process inheritance. Start the
top-level orchestrator session with `orc sandbox run`; any child worker
processes launched through `orc run advance <run-id>` or
`orc worker launch-next <run-id>` from that session run in the same bubblewrap
environment and see the marker variables above.

Agent worker commands are built from the selected runtime descriptor. Outside
a verified Orc sandbox, the launcher uses the runtime's `command.normal_args`;
inside a verified Orc sandbox, it uses `command.sandbox_args`. The scaffolded
Codex runtime descriptor therefore produces this normal argv:

```bash
codex --ask-for-approval never exec --skip-git-repo-check -
```

When the repository has sandbox config and Orc verifies both `ORC_SANDBOX=1`
and a canonical `ORC_SANDBOX_ROOT` matching the current repository root, the
same Codex runtime descriptor produces this sandbox argv:

```bash
codex --dangerously-bypass-approvals-and-sandbox exec --skip-git-repo-check -
```

The outer bubblewrap process is the isolation boundary in this mode. Manually
exported marker variables do not switch runtime mode in repositories without
sandbox config, and invalid or mismatched markers do not select sandbox args.
There is no built-in Codex fallback after the runtime migration; projects that
want Codex workers must declare and select `.orc/runtimes/codex.yaml`.
Explicit worker command overrides are used unchanged.

Set `sandbox.require_for_workers: true` when a repository should refuse worker
launches unless those markers prove the launcher is already inside the
repository's sandbox. Runtime descriptors may also declare
`sandbox.required: true`, which refuses launches for that selected runtime
outside a verified Orc sandbox, or `sandbox.supported: false`, which refuses
launches inside one. These worker-mode conflicts are checked immediately before
process start. The project-wide guard is opt-in so existing non-sandbox
workflows keep working. Guard failures tell the operator to restart the
orchestrator with `orc sandbox run`.

## Bubblewrap Defaults

The v1 runner constructs a pragmatic default bubblewrap invocation for Orc
orchestration. By default it includes:

- `--die-with-parent`
- PID, IPC, and UTS isolation
- optional network isolation when `sandbox.bubblewrap.network: false`
- a read-write bind of the repository root at the same absolute path
- a read-write bind of `../.beads` from the repository root when that directory
  exists; missing Beads state is skipped as an optional default
- a read-write Codex config bind as current compatibility behavior until
  `main-b77.2` moves that policy into runtime descriptor data
- a configurable sandbox home policy; the real host home directory is never
  bound wholesale by default
- an optional `sandbox.path.mode: host_entries` policy that mounts existing
  absolute PATH directories read-only at their original sandbox paths
- sandbox requirements declared by runtimes selected by loaded workflows:
  required environment pass-through, fixed environment values, and
  descriptor-owned static mounts
- a private writable `/tmp` tmpfs instead of writable host `/tmp`
- read-only binds for existing executable/system paths needed to start normal
  configured commands: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc`, and
  `/nix/store`
- `/proc` and `/dev`
- `--chdir` to the validated sandbox cwd

## Environment

The sandbox does not pass the whole host environment. Orc clears the child
environment and sets an allowlisted environment into bubblewrap. The default
allowlist includes `PATH`, `HOME`, `TERM`, `LANG`, `LC_*`, `SHELL`, `USER`,
`LOGNAME`, `CODEX_HOME`, `OPENAI_API_KEY`, `ORC_SANDBOX`, and
`ORC_SANDBOX_ROOT`.

`sandbox.env.pass` adds explicit host variables by name when present.
`sandbox.env.set` sets fixed values and wins over pass-through values with the
same name. Orc-managed values for `HOME`, `CODEX_HOME`, `ORC_SANDBOX`, and
`ORC_SANDBOX_ROOT` are set to the resolved sandbox values after host allowlist,
`sandbox.env.pass`, and `sandbox.env.set` processing.

Runtime descriptor `sandbox.requirements.env` entries merge into the sandbox
environment policy before bubblewrap starts. Static fixed-value conflicts fail
when project config is loaded; missing host variables requested through
pass-through are skipped the same way as `sandbox.env.pass`.

The approved `main-b77.2` runtime sandbox design adds descriptor-owned
env-sourced mounts and `sandbox.requirements.env.set_from_mount.<NAME>.value:
target`, so runtime env values can be derived from resolved mount targets while
building the bubblewrap spec. See
[../reference/configuration-runtimes.md](../reference/configuration-runtimes.md)
for the schema design. Until that implementation lands, Codex config home
handling remains compatibility behavior in the sandbox builder.

## PATH Mount Policy

`sandbox.path.mode` controls automatic mounts for PATH entries. The allowed
values are `none` and `host_entries`; omitting the field is the same as
`none`, which preserves the existing environment and mount behavior.

In `host_entries` mode, Orc reads the effective sandbox PATH and makes existing
absolute PATH directories available inside bubblewrap as read-only mounts. The
effective PATH is `sandbox.env.set.PATH` when configured; otherwise it is the
original host process `PATH`. Orc preserves that PATH string exactly in the
sandbox, including empty entries and relative entries. Empty, relative,
missing, unresolvable, or non-directory entries are not mounted and do not fail
spec construction. PATH entries that are already strictly underneath the
repository, Beads, or first-class system mounts are also not mounted because
those directories are already visible through their parent mount.

For each existing absolute PATH entry, Orc resolves symlinks with
`filepath.EvalSymlinks` and binds the resolved directory read-only at the
original PATH entry path. This supports profile-style paths such as
`/home/user/.nix-profile/bin` while keeping the original PATH usable. Different
original PATH entries are not collapsed just because they resolve to the same
source; exact duplicate generated mounts are deduplicated.

PATH automation does not mount the whole host home. Narrow host-home PATH
directories such as `/home/user/.bun/bin`, `/home/user/.local/bin`, and
`/home/user/.nix-profile/bin` are allowed in both `synthetic` and `host_path`
home modes. Available PATH entries that are exactly the active sandbox HOME or
resolved host HOME, or ancestors such as `/`, `/home`, or `/home/user`, are
rejected as unsafe instead of skipped. Protected sandbox targets such as
`/proc`, `/dev`, `/tmp`, repository and Beads mounts, and broad system targets
remain protected. `/nix/store` remains handled by the first-class read-only
system mount; PATH entries resolving into the store are mounted only at their
original PATH entry path.

Automatic PATH mounts are emitted before explicit `sandbox.mounts`. If an
explicit mount targets the same sandbox path as an automatic PATH mount, Orc
fails instead of silently letting the explicit mount override the generated
read-only mount.

## Home Policy

`sandbox.home.mode` controls the sandbox HOME path. The allowed values are
`synthetic` and `host_path`; omitting the field is the same as `synthetic`.

In `synthetic` mode, Orc preserves the original v1 behavior. The sandboxed
process sees `HOME=/home/orc`. Current Codex compatibility resolves the host
home from `HOME` or the platform user-home fallback, creates the host `.codex`
directory when needed, mounts that directory read-write at `/home/orc/.codex`,
and sets `CODEX_HOME=/home/orc/.codex` in the sandbox.

In `host_path` mode, Orc preserves the resolved host HOME path inside
bubblewrap without binding the whole host home directory. Host HOME is resolved
from the host `HOME` environment variable when present, otherwise from the
platform user-home fallback. The result must be absolute. Orc creates empty
path directories for that HOME inside bubblewrap, sets `HOME` to that absolute
path, and leaves the host home itself unbound. Current Codex compatibility
creates host `$HOME/.codex` when needed, mounts it read-write at that same
absolute path inside the sandbox, and sets `CODEX_HOME` to that path.

When host `CODEX_HOME` is set in either home mode, current compatibility
requires an absolute path, mounts it read-write at the same absolute path inside
the sandbox, and sets sandbox `CODEX_HOME` to that path.

After `main-b77.2`, this Codex-specific compatibility should be represented as
generic runtime descriptor data: use an absolute `CODEX_HOME` source when set,
otherwise fall back to host HOME plus `.codex`, create the resolved source when
requested, mount it read-write, and derive sandbox `CODEX_HOME` from the
resolved target path. The approved target behavior preserves the same
synthetic-home and host-path results described above.

## Extra Mounts

Extra `sandbox.mounts` entries support `ro` and `rw` modes. Relative host paths
resolve from the repository root. Missing mounts are errors unless
`optional: true` is set, in which case Orc skips the missing mount.
Runtime descriptor `sandbox.requirements.mounts` entries use the same fields
for simple static mounts. The approved `main-b77.2` design also adds extended
mounts with `source.env`, `source.fallback.host_home`, `source.create`,
`target.env_same_as_source`, `target.fallback.sandbox_home`, and optional `id`
for `env.set_from_mount` references. Those extended runtime mounts will be
resolved while building the bubblewrap spec, before the sandbox process starts.
Static target and duplicate declaration conflicts fail during config load;
host-dependent missing paths, explicit creation, env source resolution,
env-from-mount reference resolution, and symlink escape checks fail during
sandbox launch preparation.

Runtime sandbox requirements are selected from loaded workflows before
bubblewrap starts. Static conflicts that do not require host inspection fail
during project config load. Host-dependent failures, including missing required
host paths, source paths that point to files, symlink resolution failures,
protected target conflicts that depend on resolved paths, mount collisions with
project or automatic mounts, and unavailable sandbox coverage for selected
`runtime_dirs`, fail during sandbox launch preparation. Worker launch does not
add mounts to an already-running sandbox; it only verifies that the selected
runtime is compatible with the active sandbox markers and that required runtime
directories are visible through existing sandbox coverage.

Writable repo-relative host paths must stay inside the repository and must not
escape through traversal or symlinks. Mount targets must be clean absolute
sandbox paths and cannot override critical sandbox internals such as `/proc`,
`/dev`, `/tmp`, `/home`, read-only system paths, `/nix/store`, or the repository
mount. Parent paths that would mask those protected mounts are also rejected.

In `synthetic` mode, explicit mounts under `/home/orc/...` are allowed for
selected synthetic-home config paths, but `/home/orc` itself is rejected. In
`host_path` mode, explicit mounts may target concrete absolute paths strictly
under the active sandbox HOME path, such as `/home/user/.bun` or
`/home/user/.cache/tool`. This supports tools referenced through variables such
as `CODEX_BIN` without adding tool-specific discovery. Mount targets exactly
equal to the active HOME path, ancestors of it such as `/home`, or sibling home
paths are rejected so config cannot bind the whole host home or mask sandbox
setup.

Config values are not shell-expanded. Write concrete absolute paths in YAML.
Values such as `$HOME/.bun`, `${HOME}/.bun`, `~/.bun`, `$(which codex)`, and
backtick command substitutions are treated as literal strings and are rejected
when they are not clean absolute paths. Orc does not perform shell expansion,
tilde expansion, environment interpolation, command substitution, or command
execution while loading sandbox config.

## Explicit Non-Defaults

v1 does not bind the whole real home, pass the whole host environment, mount
writable host caches by default, mount `/nix/store` writable, expose SSH agents,
Git credentials, browser profiles, or unrelated user files by default, deny
network access by default, discover Bun, Node, Codex, or `CODEX_BIN` binaries,
add diagnostic helper subcommands such as `sandbox check` or
`sandbox print-bwrap`, or enable yolo mode for non-sandbox worker launches.
`orc sandbox run` still does not invent a Codex or yolo command when sandbox
config is missing or invalid.

The generated `.orc/config.yaml` scaffold includes a commented Codex yolo-mode
sandbox example. The example is not active until the user uncomments it because
yolo mode is a deliberate operator choice, even when bubblewrap is configured.
Existing `.orc/config.yaml` files are user-owned and are not automatically
migrated or rewritten when scaffold examples change.
