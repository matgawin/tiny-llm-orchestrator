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

## Bubblewrap Defaults In This Slice

The v1 runner constructs a minimal bubblewrap invocation that includes:

- `--die-with-parent`
- PID, IPC, and UTS isolation
- optional network isolation when `sandbox.bubblewrap.network: false`
- a read-write bind of the repository root at the same absolute path
- read-only binds for existing executable/system paths needed to start normal
  configured commands: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc`, and
  `/nix/store`
- `/proc` and `/dev`
- `--chdir` to the validated sandbox cwd

The complete mount and environment policy is intentionally separate follow-up
work. v1 does not implement worker launch refusal outside the sandbox,
diagnostic helper subcommands such as `sandbox check` or `sandbox print-bwrap`,
or scaffold examples.
