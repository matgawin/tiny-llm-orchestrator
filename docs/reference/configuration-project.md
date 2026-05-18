# Project Configuration Reference

## Purpose

Document the `.orc/config.yaml` project configuration contract and validation rules.

## Audience

Contributors and maintainers changing project config loading, validation, loop caps, or sandbox config schema.

## Read This When

- You are updating `.orc/config.yaml` schema validation.
- You need project config defaults or allowed values.
- You are changing sandbox config shape or validation.

## Related Docs

- [configuration.md](configuration.md)
- [configuration-init.md](configuration-init.md)
- [configuration-init-upgrade.md](configuration-init-upgrade.md)
- [configuration-workflows.md](configuration-workflows.md)
- [../features/sandbox-run.md](../features/sandbox-run.md)

## `.orc/config.yaml`

Required fields:

- `version`: currently `1`
- `workflows`: map of workflow name to either a legacy `.orc`-relative
  workflow file path scalar or an object with `path` and optional `loop_caps`
- `agents`: map of agent id to `.orc`-relative descriptor file path

Optional setup-upgrade field:

- `setup_version`: project setup/scaffold version for `orc init upgrade`;
  current value is `1`

Missing `setup_version` means legacy setup version `0` for upgrade planning and
older-setup warnings. It is not invalid config by itself. `setup_version` is
validated separately from `version`: `version` is the project config schema
version, while `setup_version` is the project-local setup/scaffold migration
version. It is not a run-store `schema_version`, a run config snapshot schema
version, or an Orc binary semantic version. See
[configuration-init-upgrade.md](configuration-init-upgrade.md).

The `workflows` and `agents` maps must each contain at least one entry.
Referenced paths must be relative to `.orc`; absolute paths, traversal outside `.orc`, and symlink escapes are rejected.

Runtime descriptors are executable descriptors. Agent descriptors remain
prompt/persona descriptors. Workflow agent steps select prompt/persona
descriptors through `agent` and select executable descriptors through their
effective runtime.

Agent workflows must have an effective declared runtime for every agent step.
The scaffolded project config provides this through a `runtimes` map:

```yaml
agents:
  coder: agents/coder.md
runtimes:
  codex: runtimes/codex.yaml
```

Runtime descriptor paths are relative to `.orc`; by convention they live under
`.orc/runtimes/*.yaml`. The loader rejects absolute paths, traversal outside
`.orc`, and symlink escapes. Runtime ids must be non-empty and descriptor
`id` values must match their `runtimes` map keys. Any agent step with an
effective runtime must reference a declared runtime. There is no built-in Codex
fallback after the runtime migration; projects that want Codex workers must
declare a Codex runtime descriptor and select it through workflow defaults or
step overrides.

A scaffolded Codex runtime descriptor is:

```yaml
id: codex
command:
  executable: codex
  normal_args: [--ask-for-approval, never]
  sandbox_args: [--dangerously-bypass-approvals-and-sandbox]
  args: [exec, --skip-git-repo-check, "-"]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  allowed: []
  args: [--model, "{model}"]
reasoning:
  supported: true
  required: false
  default: medium
  allowed: [low, medium, high, xhigh]
  args: [--config, 'model_reasoning_effort="{reasoning}"']
directories:
  supported: true
  args: [--add-dir, "{dir}"]
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: [OPENAI_API_KEY]
      set: {}
      set_from_mount:
        CODEX_HOME:
          mount: config_home
          value: target
    mounts:
      - id: config_home
        source:
          env: CODEX_HOME
          fallback:
            host_home: .codex
          create: true
        target:
          env_same_as_source: true
          fallback:
            sandbox_home: .codex
        mode: rw
```

Runtime descriptor validation rejects empty command executables, empty argv
entries, unsupported prompt delivery values, unsupported or misplaced
placeholders, inconsistent model, reasoning, or directory capability
declarations, and static sandbox requirement conflicts. The only runtime argv
placeholders are
`{model}`, `{reasoning}`, `{prompt_file}`, `{agent_id}`, `{step_id}`,
`{attempt_id}`, `{run_id}`, and the directory-only `{dir}` placeholder.
`{prompt_file}` is valid only with `prompt.delivery: file`; `stdin` delivery
writes the rendered worker prompt to process stdin. Missing or empty
`model.allowed` means model values are passed through; a non-empty list is an
allowlist for runtime defaults and workflow-selected models. `{reasoning}` is
valid only in `reasoning.args`. Missing or empty `reasoning.allowed` means
reasoning values are passed through; a non-empty list is an allowlist for
runtime defaults and workflow-selected reasoning values. Directory args are
emitted by repeating `directories.args` once per effective `runtime_dirs` entry;
paths are explicit argv values and are not shell-expanded.

Project config also supports workflow loop cap defaults:

```yaml
defaults:
  loop_caps:
    enabled: true
    soft: 2
    hard: 4
```

`defaults.loop_caps` may be omitted for older configs. Missing loop cap config
resolves to the built-in default `enabled: true`, `soft: 2`, and `hard: 4`.
New scaffolded configs include those values explicitly.

Workflow-level loop cap overrides use the expanded workflow object form:

```yaml
workflows:
  implementation:
    path: workflows/implementation.yaml
    loop_caps:
      hard: 6
```

Workflow overrides merge with `defaults.loop_caps`, so partial overrides inherit
omitted fields. `enabled: false` is the only supported disable signal. When the
effective value is disabled, `soft` and `hard` may be omitted and are ignored if
present. When the effective value is enabled, `soft` and `hard` must resolve to
positive integers, and `hard` must be greater than `soft`. Negative caps are
always invalid; zero caps are invalid when the effective value is enabled. Loop
caps apply only to workflow routing loops. They do not change agent execution
retry caps, report validation retries, or the `defaults.retries` workflow
outcome retry policy.

Project config may also declare an Orc-managed sandbox command contract:

```yaml
sandbox:
  command:
    argv: ["codex", "--dangerously-bypass-approvals-and-sandbox"]
  cwd: "."
  require_for_workers: true
  home:
    mode: synthetic
  path:
    mode: none
  protected_paths:
    - host_home: .ssh
    - absolute: /var/lib/orc/secrets
  bubblewrap:
    enabled: true
    network: true
  env:
    pass: []
    set: {}
  mounts:
    - host: ".orc/cache"
      target: "/workspace/.orc/cache"
      mode: rw
      optional: true
```

The sandbox section configures `orc sandbox run`. This reference documents the
configuration shape and validation rules; see
[../features/sandbox-run.md](../features/sandbox-run.md) for the executable CLI
behavior and the canonical bubblewrap mount, environment, home, network, and
non-default policy. Bubblewrap sandbox execution is Linux-only for v1, although
the configuration schema can be loaded on any platform.

`sandbox.command.argv` is required whenever `sandbox` is present. It must be a
non-empty argv list with no empty entries. Shell-string command declarations
are rejected, and Orc does not default this field to Codex, yolo mode, or any
other command.

`sandbox.cwd` defaults to the repository root when omitted. When set, it is
interpreted relative to the repository root and must be an existing directory
that is not absolute, traversing outside the repository, or escaping through a
symlink.

`sandbox.require_for_workers` is optional and defaults to `false`. When set to
`true`, `orc worker launch-next` refuses to run unless the process has
`ORC_SANDBOX=1` and `ORC_SANDBOX_ROOT` matches the current repository root.
Enable it for projects that expect workers to be launched only by a top-level
orchestrator session started through `orc sandbox run`.

`sandbox.home.mode` is optional and defaults to `synthetic`. Allowed values are
exactly `synthetic` and `host_path`. `synthetic` keeps sandbox `HOME` at
`/home/orc`; `host_path` sets sandbox `HOME` to the resolved absolute host HOME
path without binding the whole host home directory. Runtime descriptors can add
config-home mounts through generic sandbox requirements; the scaffolded Codex
runtime descriptor maps `CODEX_HOME` and `.codex` behavior that way.
See [../features/sandbox-run.md](../features/sandbox-run.md) for current HOME
resolution and [configuration-runtimes.md](configuration-runtimes.md) for the
descriptor schema.

`sandbox.path.mode` is optional and defaults to `none`. Allowed values are
exactly `none` and `host_entries`. `none` preserves the existing PATH and mount
behavior. `host_entries` uses the effective sandbox PATH, meaning
`sandbox.env.set.PATH` when configured and otherwise the original host process
`PATH`, and mounts existing absolute PATH directories read-only at their
original sandbox paths. Empty, relative, missing, unresolvable, non-directory,
and already mounted repository, Beads, or first-class system subtree entries
are preserved in PATH but not mounted. Host-dependent safety checks happen
while building the sandbox spec, not during static config load. See
[../features/sandbox-run.md](../features/sandbox-run.md) for symlink
resolution, dangerous-entry errors, dedupe, and explicit mount conflict
behavior.

`sandbox.protected_paths` is an optional list of static protected host-path
declarations. Each entry must set exactly one of `host_home` or `absolute`.
`host_home` values are clean relative descendant paths under the host HOME,
such as `.ssh` or `.config/tool/secrets`; absolute values must be clean
absolute paths and cannot be `/`.

The default list is empty. Orc v1 does not implicitly protect `.ssh`,
`.gnupg`, or any other host-home path unless the project config declares it.
Entries must use object form:

```yaml
sandbox:
  protected_paths:
    - host_home: .ssh
    - host_home: .gnupg
    - absolute: /var/lib/orc/secrets
```

Bare strings and repository-relative protected paths are invalid. Use
`host_home: .ssh`, not a bare `.ssh`, because `host_home` means a descendant
of the resolved host HOME and never a path relative to the repository.
`host_home` values must not be empty, `.`, absolute, unclean, or contain any
`..` traversal segment. `absolute` values must not be empty, relative, unclean,
or `/`. Protected path values are not shell-expanded or interpolated; `~`,
`$HOME`, `${HOME}`, `$(pwd)`, and backtick command substitutions are literal
YAML text and are rejected.

Static syntax validation happens while loading project config. Host-dependent
validation happens while building the sandbox spec for `orc sandbox run`, after
the host HOME and mount sources are resolved. That phase handles missing
protected paths, symlink evaluation, and conflicts with project mounts, runtime
mount requirements, and automatic PATH mounts.

`sandbox.bubblewrap.enabled` is reserved for bubblewrap policy selection; v1
`orc sandbox run` always shells out to `bwrap` and never treats this field as
permission to run unsandboxed. `sandbox.bubblewrap.network` accepts `true` or
`false` and defaults to `true`.

`sandbox.env.pass` is an optional list of environment variable names to pass
from the host when present. `sandbox.env.set` is an optional map of fixed
environment variable values; duplicate keys are allowed with pass-through names,
and the fixed value takes precedence.

Extra `sandbox.mounts` entries declare project-specific host mounts. `mode` must
be exactly `ro` or `rw`. `host` may be absolute or repository-relative.
Repository-relative writable host paths must resolve inside the repository.
`target` must be a clean absolute sandbox path that passes the protected-target
validation used by `orc sandbox run`. Missing required mounts are validation
errors; missing mounts with `optional: true` are skipped. In `host_path` mode,
home-local tool directories such as `/home/user/.bun` or
`/home/user/.cache/tool` must be mounted explicitly with concrete absolute
targets strictly under the active sandbox HOME path; the active HOME itself and
its ancestors are rejected.

These mounts are generic project-level sandbox inputs for tools, caches, and
external worktrees. They merge with sandbox requirements from the runtimes
selected by loaded workflows before bubblewrap starts; they are not tied to any
specific runtime and worker launch does not add additional mounts dynamically.

Sandbox config values are not shell-expanded or interpolated. `$HOME`,
`${HOME}`, `~`, `$(which codex)`, and backtick command substitutions are
literal YAML values, not expansion syntax. Orc does not run commands or perform
shell, tilde, or environment expansion while loading config.

New `orc init` scaffolds include a commented sandbox example with explicit Codex
yolo-mode argv and `network: true`. The example is commented because Orc does
not enable yolo mode or sandboxing by default. Existing `.orc/config.yaml` files
are user-owned and are not automatically migrated or rewritten when scaffold
examples change.
