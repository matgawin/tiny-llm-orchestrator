# Configuration Init Reference

## Purpose

Document the `orc init` scaffold contract and the v1 scaffold files generated from `internal/initconfig/scaffold/.orc`.

## Audience

Contributors and maintainers changing scaffold source, init behavior, or generated project-local `.orc` files.

## Read This When

- You are updating scaffold source or generated init output.
- You need the scaffolded workflow and agent inventory.
- You are checking `orc init` dry-run, prompt, `.gitignore`, or instruction-file behavior.

## Related Docs

- [configuration.md](configuration.md)
- [configuration-init-upgrade.md](configuration-init-upgrade.md)
- [configuration-project.md](configuration-project.md)
- [configuration-workflows.md](configuration-workflows.md)
- [run-store-layout.md](run-store-layout.md)

## Init Scaffolding

`orc init` scaffolds the v1 `.orc` configuration shape into the current working
directory:

- `orc init --dry-run` previews planned files without writing.
- `orc init --yes` creates missing scaffold files noninteractively.
- Interactive `orc init` prompts before overwriting differing scaffold files and
  before creating a missing `.gitignore`.
- Instruction files: interactive init prompts before creating or updating
  `AGENTS.md`; `--yes` skips `AGENTS.md` creation or update; v1 only supports
  `AGENTS.md`.
- `orc init` creates and ignores `.orc/runs/`.
- Persistent files under `.orc/` are user-owned and reviewable; runtime run
  state belongs under the ignored `.orc/runs/` directory; see
  [run-store-layout.md](run-store-layout.md) for the durable file contract.
- If `.gitignore` broadly ignores `.orc`, `orc init` fails and asks you to
  replace that broad rule with `.orc/runs/` so persistent config remains
  trackable.
- `orc init upgrade` owns later setup upgrades. Bare `orc init upgrade` is
  plan-only and writes nothing; `orc init upgrade --apply` writes safe changes.
  V1 has no `--dry-run` flag for upgrade because the bare command is the dry-run
  behavior. See [configuration-init-upgrade.md](configuration-init-upgrade.md).

The scaffold includes these workflows:

- `implementation`: plan, code, test, and review a general change.
- `bugfix`: reproduce the bug before planning, coding, testing, and review.
- `mechanical-change`: plan, apply low-judgment mechanical edits, run focused
  verification, and complete mechanical review.
- `test-only`: plan, design tests, edit tests, run tests, and review without
  intentional production behavior changes.
- `docs-update`: update durable docs and run docs review without the full
  implementation test and review chain.
- `review-fix`: review an existing dirty working-copy change, route requested
  fixes through the standard coder, rerun `task check` after fixes, and finish
  the redundancy and readability review lanes.
- `review-mechanical`: review a change for stale references, generated drift,
  config mismatch, and mechanical completeness.
- `review-readability`: review changed code or docs for clarity and
  maintainability.
- `review-redundancy`: review for duplicated logic, duplicated docs, unused
  scaffold, and unnecessary surface area.
- `review-docs`: review durable docs, indexes, examples, and links for
  contract accuracy.

The scaffold also includes `.orc/runtimes/codex.yaml` and references it from
`.orc/config.yaml` as `runtimes.codex`. Scaffolded workflows set
`defaults.runtime: codex`, so existing agent-only steps have an explicit
effective runtime while preserving the agent descriptor ids used in persisted
attempt metadata.

The scaffolded Codex runtime descriptor is:

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

This descriptor, not a launcher special case, preserves Codex argv behavior for
new projects. It also declares Codex config-home behavior through generic
runtime sandbox requirements; the schema is documented in
[configuration-runtimes.md](configuration-runtimes.md). Existing user-owned
`.orc` directories are not automatically migrated by plain `orc init` when
scaffold output changes. Future setup changes are handled through explicit
versioned `orc init upgrade` migrations.

The scaffolded `.orc/config.yaml` includes a commented sandbox example. The
example includes commented `sandbox.protected_paths` entries for common
host-home secrets:

```yaml
# sandbox:
#   protected_paths:
#     - host_home: .ssh
#     - host_home: .gnupg
```

The example remains commented so the scaffold keeps the v1 default empty
protected path list. Operators who enable it should keep object-form entries;
bare strings such as `.ssh` are intentionally invalid.

Implementation, bugfix, mechanical-change, and test-only workflows block dirty
starts by default so unrelated pre-existing changes do not mix with new work.
Review and review-fix workflows allow dirty starts by default because their
normal input is often the existing working-copy diff being reviewed.

Default scaffolded workflows keep skip points intentionally narrow. Only
explicit human-judgment bypass points are skippable: review steps that have not
run yet, and remediation steps selected after reviewer-requested changes when a
human decides not to implement those requested changes. Planning steps, normal
initial coding steps except where the same step is also the remediation target,
test design, and verification command steps are not general automation
shortcuts and are not skippable by default.

Skipped review routes to the next stage a human-approved bypass should reach.
In single-step review-only workflows, skipped review routes to
`ready_for_human`. In the multi-review implementation and review-fix workflows,
skipped `review` routes to `redundancy-review`, skipped `redundancy-review`
routes to `readability-review`, and skipped `readability-review` routes to
`ready_for_human`.

Remediation steps selected after reviewer changes have explicit skip routes
for the same human-bypass policy. In the implementation workflow, skipped
`code` routes to `redundancy-review`, skipped `code_fixer` routes to
`readability-review`, and skipped `code_cleaner` routes to `ready_for_human`.
The review-fix workflow uses the same remediation and verification routing, but
starts at review and allows dirty starts so it can finish an existing patch.
In bugfix, mechanical-change, and test-only workflows, skipped remediation
routes to `ready_for_human`. Because the shared `code`, `mechanical-code`, and
`test-code` steps cannot distinguish whether they were selected for initial
work or reviewer remediation, the skippable route is available whenever those
steps are selected; the required human skip reason is the audit record for why
the bypass was appropriate.

The `docs-update` workflow is the narrow default for documentation-only tasks.
It starts with a docs edit step using the standard coder agent, then routes to
`docs-review`. Reviewer-requested changes loop back to the docs edit step.
Skipped docs review routes directly to `ready_for_human`.

The scaffold includes detailed descriptors for these agents:

- `planner`
- `coder`
- `mechanical-coder`
- `bug-reproducer`
- `tester`
- `test-designer`
- `reviewer`
- `mechanical-reviewer`
- `readability-reviewer`
- `redundancy-reviewer`
- `docs-reviewer`

Each scaffold descriptor is written for the full rendered worker prompt, not as
a standalone instruction. Descriptors explicitly tell workers how to use
`Attempt Metadata`, `Task Context`, `Prior Report Context`, and `Report
Contract`, which are injected by the prompt renderer at worker launch time.
They also allow workers to use available repo-local skills and bounded
subagents when the active worker runtime exposes those capabilities.
