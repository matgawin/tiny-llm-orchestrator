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

## Bubblewrap Defaults

The v1 runner constructs a pragmatic default bubblewrap invocation for Codex
orchestration. By default it includes:

- `--die-with-parent`
- PID, IPC, and UTS isolation
- optional network isolation when `sandbox.bubblewrap.network: false`
- a read-write bind of the repository root at the same absolute path
- a read-write bind of `../.beads` from the repository root when that directory
  exists; missing Beads state is skipped as an optional default
- a read-write Codex config bind. If `CODEX_HOME` is set, Orc mounts that
  absolute path at the same path and sets `CODEX_HOME` to it inside the
  sandbox. Otherwise Orc uses the host `~/.codex`, creating it when needed, and
  mounts it at `/home/orc/.codex`.
- a synthetic home at `/home/orc`; the real host home directory is not bound
  wholesale by default
- a private writable `/tmp` tmpfs instead of writable host `/tmp`
- read-only binds for existing executable/system paths needed to start normal
  configured commands: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc`, and
  `/nix/store`
- `/proc` and `/dev`
- `--chdir` to the validated sandbox cwd

## Environment

The sandbox does not pass the whole host environment. Orc clears the child
environment and sets an allowlisted environment into bubblewrap. The default
allowlist includes `PATH`, synthetic `HOME`, `TERM`, `LANG`, `LC_*`, `SHELL`,
`USER`, `LOGNAME`, `CODEX_HOME`, `OPENAI_API_KEY`, `ORC_SANDBOX`, and
`ORC_SANDBOX_ROOT`.

`sandbox.env.pass` adds explicit host variables by name when present.
`sandbox.env.set` sets fixed values and wins over pass-through values with the
same name. Orc-managed values for `HOME`, `CODEX_HOME`, `ORC_SANDBOX`, and
`ORC_SANDBOX_ROOT` are set to the resolved sandbox values.

## Extra Mounts

Extra `sandbox.mounts` entries support `ro` and `rw` modes. Relative host paths
resolve from the repository root. Missing mounts are errors unless
`optional: true` is set, in which case Orc skips the missing mount.

Writable repo-relative host paths must stay inside the repository and must not
escape through traversal or symlinks. Mount targets must be clean absolute
sandbox paths and cannot override critical sandbox internals such as `/proc`,
`/dev`, `/tmp`, `/home`, `/home/orc`, read-only system paths, `/nix/store`, or
the repository mount. Parent paths that would mask those protected mounts are
also rejected. Explicit mounts under `/home/orc/...` are allowed for selected
synthetic-home config paths.

## Explicit Non-Defaults

v1 does not bind the whole real home, pass the whole host environment, mount
writable host caches by default, mount `/nix/store` writable, expose SSH agents,
Git credentials, browser profiles, or unrelated user files by default, deny
network access by default, implement worker launch refusal outside the sandbox,
add diagnostic helper subcommands such as `sandbox check` or
`sandbox print-bwrap`, or add scaffold examples.
