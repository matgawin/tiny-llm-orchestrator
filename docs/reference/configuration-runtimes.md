# Runtime Configuration Design

## Purpose

Define the v1 contract for configurable agent runtimes and descriptor-built
worker launch behavior.

## Audience

Contributors implementing runtime descriptor loading, workflow runtime
selection, launcher command construction, sandbox integration, scaffold
migration, and end-to-end runtime tests.

## Read This When

- You are implementing `.orc/runtimes/*.yaml` loading or validation.
- You are adding `runtime`, `model`, or `runtime_dirs` workflow fields.
- You are maintaining descriptor-built worker argv.
- You are updating sandbox requirements or runtime-related docs and tests.

## Related Docs

- [configuration.md](configuration.md)
- [configuration-project.md](configuration-project.md)
- [configuration-workflows.md](configuration-workflows.md)
- [configuration-init.md](configuration-init.md)
- [../features/worker-launching.md](../features/worker-launching.md)
- [../features/sandbox-run.md](../features/sandbox-run.md)

## Status

This page is the durable implementation contract for configurable runtime
descriptors, workflow runtime selection, descriptor-built worker argv, and
runtime sandbox requirements.

## Public Schema

The public abstraction name is `runtime`. Do not use `cli`, `harness`, or
`llm` in public YAML field names for this feature.

Project config declares runtime descriptor files in `.orc/config.yaml`:

```yaml
runtimes:
  codex: runtimes/codex.yaml
```

The `runtimes` map keys are runtime ids. Values are descriptor paths relative
to `.orc`. Runtime descriptors live under `.orc/runtimes/*.yaml`; implementation
must reject absolute paths, traversal outside `.orc`, and symlink escapes using
the same path-safety model as workflow and agent descriptors. The descriptor
`id` must match the map key.

The existing `agents` map remains the prompt/persona descriptor inventory:

```yaml
agents:
  coder: agents/coder.md
```

Agent descriptors are not executable descriptors and must not grow runtime
command fields. Workflow agent steps select an agent descriptor and an
effective runtime independently.

Workflow defaults and agent steps add these fields:

```yaml
defaults:
  runtime: codex
  model: gpt-5.3-codex
  runtime_dirs:
    - shared-worktree

steps:
  code:
    agent: coder
    runtime: codex
    model: gpt-5.3-codex
    runtime_dirs:
      - /home/matt/Documents/other-repo
```

The public YAML fields are exactly:

- `defaults.runtime`
- `defaults.model`
- `defaults.runtime_dirs`
- `steps.<id>.runtime`
- `steps.<id>.model`
- `steps.<id>.runtime_dirs`

These fields are agent-step only. Command and script steps must reject
`runtime`, `model`, and `runtime_dirs`.

## Runtime Descriptor Shape

The minimum descriptor shape is:

```yaml
id: codex
command:
  executable: codex
  args: [exec, --skip-git-repo-check, "-"]
  normal_args: [--ask-for-approval, never]
  sandbox_args: [--dangerously-bypass-approvals-and-sandbox]
prompt:
  delivery: stdin
model:
  supported: true
  required: false
  allowed: []
  args: [--model, "{model}"]
directories:
  supported: true
  args: [--add-dir, "{dir}"]
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: []
      set: {}
    mounts: []
```

`command.executable` is required and non-empty. `command.args`,
`command.normal_args`, and `command.sandbox_args` are argv fragments; omitted
fragments resolve to empty lists, but present entries must be non-empty.
Runtime command declarations are never shell strings.

`model.default` is optional and is omitted from this minimum Codex-compatible
descriptor so that Codex keeps its current no-model argv by default. Runtimes
that need a default model may declare one under `model.default`; it participates
in the model resolution order below.

The deterministic argv order is:

1. `command.executable`
2. `command.normal_args` or `command.sandbox_args`, selected from the active
   worker mode
3. `command.args`
4. `model.args`, appended only when an effective model resolves
5. `directories.args`, repeated once per effective runtime directory

This order is intentionally different from shell-style command assembly. Each
YAML list item is one argv entry.

`prompt.delivery` accepts exactly `stdin` or `file`. For `stdin`, the rendered
worker prompt is written to process stdin. For `file`, the existing persisted
prompt artifact path is the prompt file and may be substituted through
`{prompt_file}`. The launcher must not create a second prompt file unless a
later design explicitly changes this contract.

## Placeholders

Runtime descriptors may use only these placeholders in argv fragments:

- `{model}`
- `{prompt_file}`
- `{agent_id}`
- `{step_id}`
- `{attempt_id}`
- `{run_id}`
- `{dir}`

`{dir}` is valid only in `directories.args`. The public command/model
placeholder list is exactly `{model}`, `{prompt_file}`, `{agent_id}`,
`{step_id}`, `{attempt_id}`, and `{run_id}`; `{dir}` is reserved for the
directory capability fragment because it is repeated per directory.

Unknown placeholders are configuration errors. Orc does not perform shell
expansion, environment expansion, tilde expansion, command substitution, or
arbitrary string interpolation. Values such as `$HOME`, `${HOME}`, `~`,
`$(cmd)`, and backticks are literal YAML values and are rejected where they
violate field validation.

`{prompt_file}` is valid only when `prompt.delivery: file`. `{model}` is valid
only in `model.args` or command argv for runtimes with `model.supported: true`.
`directories.args` must include `{dir}` when `directories.supported: true`.

## Resolution Rules

Runtime resolution for an agent step is:

1. `steps.<id>.runtime`
2. `defaults.runtime`

There is no runtime descriptor default. Missing effective runtime for an agent
step is a validation error. The effective runtime id must reference a declared
runtime descriptor.

Model resolution for an agent step is:

1. `steps.<id>.model`
2. `defaults.model`
3. `runtime.model.default`

If `runtime.model.required: true` and no effective model resolves, validation
fails. If any workflow model resolves or is declared while
`runtime.model.supported: false`, validation fails. When `runtime.model.allowed`
is missing or empty, model values are pass-through. When it is non-empty,
workflow defaults, step overrides, and runtime defaults must all be members of
the allowlist.

Runtime directory resolution is:

1. all `defaults.runtime_dirs` entries in declared order
2. all `steps.<id>.runtime_dirs` entries in declared order

Exact duplicate runtime directory entries are preserved and emitted repeatedly.
The first implementation must not normalize or deduplicate them because repeated
argv entries may be meaningful to some runtimes.

`runtime_dirs` entries may be clean repository-relative paths or absolute host
paths. Repository-relative entries resolve from the project root and must not
contain unclean path syntax, `..` traversal, shell syntax, environment syntax,
or tilde syntax. Absolute entries are allowed for external worktrees, but they
are passed only as explicit argv values after validation; no shell expansion is
performed. Empty entries are invalid.

## Capabilities

Runtime capabilities are explicit descriptor metadata.

`model.supported: false` means the runtime cannot receive model selection from
workflow config. It rejects `model.required`, `model.default`, `model.allowed`,
`model.args`, and any effective workflow model.

`directories.supported: false` means the runtime cannot receive
`runtime_dirs`. It rejects `directories.args` and any effective
`runtime_dirs`.

`sandbox.supported: false` means the runtime cannot be launched while the
launcher is inside a verified Orc sandbox. `sandbox.required: true` means the
runtime must be launched only inside a verified Orc sandbox. If the selected
runtime and active worker mode conflict, the launcher must fail before process
start with an actionable process/configuration error.

Unsupported requested capabilities are validation errors when they can be
known from loaded project config and workflow config. Host-dependent sandbox
availability is checked later, as described below.

Directory capability behavior is not ad hoc argv concatenation. The only
directory argv surface is `directories.args`, repeated once per effective
runtime directory, with `{dir}` substituted by the validated directory value.

## Sandbox Requirements

Runtime sandbox requirements belong to the runtime descriptor under
`sandbox.requirements`:

```yaml
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: [CODEX_HOME]
      set:
        ORC_RUNTIME: codex
    mounts:
      - host: .orc/cache/codex
        target: /workspace/.orc/cache/codex
        mode: rw
        optional: true
```

`sandbox.requirements.env.pass` is a list of host environment variable names to
pass when present. `sandbox.requirements.env.set` is a map of fixed string
environment values. Runtime env requirements merge into sandbox env config;
fixed values win over pass-through values with the same key. Static duplicate
fixed values with different values are config-load conflicts.

`sandbox.requirements.mounts` entries use the same fields and path semantics as
project `sandbox.mounts`: `host`, `target`, `mode`, and optional `optional`.
`mode` is `ro` or `rw`. `host` may be repository-relative or absolute.
`target` must be a clean absolute sandbox path. Runtime requirement values are
not shell-expanded.

Static sandbox requirement conflicts fail during config load when they can be
known without the host filesystem: invalid env names, invalid modes, unclean
paths, protected targets, duplicate targets with incompatible declarations, and
fixed env conflicts.

Host-dependent sandbox requirement failures occur while building
`orc sandbox run` bubblewrap specs: missing required host paths, symlink
resolution failures, host paths that escape allowed roots, and conflicts with
automatic mounts or protected targets that depend on resolved host paths.

Worker launch does not add mounts to an already-running sandbox. It only checks
the selected runtime's `sandbox.supported` and `sandbox.required` policy
against the active verified Orc sandbox markers.

In sandbox mode, `runtime_dirs` must already resolve inside an available
sandbox mount or be covered by project or runtime sandbox mount requirements.
Static path-shape errors fail during workflow validation. Missing host paths or
unavailable sandbox coverage fail while building the sandbox spec. If a worker
is launched inside a verified sandbox whose selected runtime requires a
directory that is not visible in the active sandbox, the launcher fails before
process start with a runtime directory sandbox coverage error.

## Launcher Overrides

Explicit launcher command overrides bypass runtime resolution entirely and keep
their current behavior. They do not merge with runtime descriptor argv,
workflow model selection, prompt delivery settings, directory args, or sandbox
mode args.

If a launcher override is present, the override command receives the rendered
prompt on stdin under the existing override contract. Runtime validation still
loads project config, but the selected worker command is the override command.

## Codex Runtime

There is no hidden Codex launcher fallback. Codex is represented by
`.orc/runtimes/codex.yaml` and referenced from `.orc/config.yaml`:

```yaml
runtimes:
  codex: runtimes/codex.yaml
```

Scaffolded workflows should set the default runtime once:

```yaml
defaults:
  runtime: codex
  timeout: 30m
  report_exit_grace: 30s
  retries:
    failed/missing_report: 1
```

The Codex runtime descriptor must preserve current argv behavior:

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
directories:
  supported: true
  args: [--add-dir, "{dir}"]
sandbox:
  supported: true
  required: false
  requirements:
    env:
      pass: [CODEX_HOME, OPENAI_API_KEY]
      set: {}
    mounts: []
```

With no effective model, Codex argv remains:

```bash
codex --ask-for-approval never exec --skip-git-repo-check -
```

Inside a verified Orc sandbox, Codex argv remains:

```bash
codex --dangerously-bypass-approvals-and-sandbox exec --skip-git-repo-check -
```

Codex model values are pass-through because `allowed` is empty. Downstream
implementation may keep existing sandbox Codex home compatibility temporarily
only if it is documented and tested as compatibility behavior while runtime
sandbox requirements are introduced.

## Validation Timing

Project config load validates:

- `runtimes` map path safety and non-empty ids
- runtime descriptor id/key match
- required descriptor fields and supported enum values
- argv fragments, empty entries, and unknown placeholders
- prompt delivery mode and `{prompt_file}` legality
- model and directory capability self-consistency
- static sandbox requirement conflicts

Workflow validation validates:

- agent steps have an effective declared runtime
- model precedence and model allowlist membership
- required model presence
- unsupported workflow `model` and `runtime_dirs` requests
- `runtime_dirs` path shape
- command/script rejection of `runtime`, `model`, and `runtime_dirs`

Sandbox launch validates host-dependent sandbox requirement behavior before
starting bubblewrap.

Worker launch validates only the selected runtime resolution that depends on
the selected step and active run, selected prompt delivery, active sandbox mode
compatibility, placeholder value availability, and override bypass behavior. It
must fail before process start for missing selected runtime data, unsupported
prompt delivery, missing required placeholder values, or active sandbox policy
conflicts.

## Scope Exclusions

The first implementation does not include:

- shell command strings or shell evaluation
- environment, tilde, command, or arbitrary placeholder expansion
- user-defined placeholder names
- runtime fields on command or script steps
- automatic migration of user-owned existing projects outside scaffold output
- dynamic mounting of runtime directories during worker launch
- runtime-specific prompt rendering templates
- persisted runtime id in attempt metadata unless a later task explicitly
  scopes a separate field
- runtime discovery from `PATH`, package managers, editor settings, or Codex
  custom agent directories
- model allowlist fetching from external providers

## Downstream Implementation Checklist

Implementation tasks must update or add tests for:

- valid Codex runtime descriptor loading
- valid non-Codex file-prompt runtime loading
- missing runtime files, unsafe paths, id/key mismatch, empty argv entries, and
  unknown placeholders
- prompt delivery validation, including `{prompt_file}` only with file delivery
- model pass-through and allowlist rejection
- unsupported model and directory capability failures
- explicit step runtime/model override and workflow default fallback
- missing effective runtime and missing required model failures
- `runtime_dirs` validation, preserved ordering, duplicate retention, and
  repeated directory argv emission
- command/script rejection of `runtime`, `model`, and `runtime_dirs`
- descriptor-built Codex normal and sandbox argv compatibility
- launcher override bypass behavior
- runtime sandbox requirements, static conflicts, host-dependent failures, and
  remaining Codex home compatibility behavior
- end-to-end non-Codex runtime execution using real executable fixtures for
  stdin and file prompt delivery

Related docs that must stay consistent with this contract:

- [configuration-project.md](configuration-project.md) for `runtimes` and
  descriptor validation
- [configuration-workflows.md](configuration-workflows.md) for
  `defaults.runtime`, `defaults.model`, `defaults.runtime_dirs`, and step
  overrides
- [configuration-init.md](configuration-init.md) for scaffolded
  `.orc/runtimes/codex.yaml`
- [../features/worker-launching.md](../features/worker-launching.md) for
  descriptor-built worker commands
- [../features/sandbox-run.md](../features/sandbox-run.md) for runtime sandbox
  requirements and remaining compatibility behavior
- [../architecture/service-boundaries.md](../architecture/service-boundaries.md)
  if package ownership changes while implementing runtime loading, sandbox
  integration, or launcher command construction
