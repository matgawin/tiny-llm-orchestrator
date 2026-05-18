---
name: go-lsp-workflow
description: Use when inspecting, editing, reviewing, or verifying Go code and gopls can provide semantic navigation, symbol references, definitions, implementations, call hierarchy, workspace symbols, or edited-file diagnostics. Also use when deciding whether to use gopls, rg, or grep for a repository search.
---

# go-lsp-workflow

Use `gopls` for semantic Go questions and diagnostics. Use `rg` for text
search. Use `grep` only as a fallback or for simple shell plumbing.

## When To Use `gopls`

Prefer `gopls` when the question is about Go symbols or compiled package
structure:

- where a type, function, method, variable, or const is defined;
- all references to a Go symbol before changing it;
- implementations of an interface or method;
- call hierarchy for behavior that may have multiple callers;
- workspace symbols when the exact file is unknown;
- diagnostics after editing Go files.

Use `gopls` position arguments as `file.go:line:column` or `file.go:#offset`.
Line and column positions are 1-indexed.

After editing Go files, run the changed-file diagnostic gate:

```bash
task lsp
```

Use `task lsp-all` only when a change spans broad package structure or the
changed-file check is not enough. Use `task lsp-stats` when gopls appears slow,
stale, or unable to load the workspace.

Do not run `gopls check ./...`; `gopls check` takes file paths, not Go package
patterns. Use `task lsp` or `task lsp-all` for diagnostics.

## When To Use `rg`

Use `rg` for non-semantic or non-Go discovery:

- Markdown, YAML, Nix, shell, JSON, and config files;
- string literals, error text, CLI output, comments, and docs;
- finding file names or broad repository inventory;
- generated or scaffold text where symbol meaning is irrelevant.

`rg` is still the default for broad text search. Do not force `gopls` onto
literal searches.

## When To Use `grep`

Use `grep` only when:

- `rg` is unavailable;
- a command pipeline needs a tiny POSIX-style filter;
- existing scripts already use it for deterministic plumbing.

Do not use `grep` for exploratory repository search when `rg` is available.

## Read Workflow

1. Use `rg --files` or `rg` when the relevant package or file is unknown.
2. Use `gopls workspace_symbol` when the relevant Go symbol name is known or
   partly known.
3. Use `gopls definition`, `references`, `implementation`, or `call_hierarchy`
   once you have a symbol position.
4. Read only the files needed to answer the question or plan the edit.

## Edit Workflow

1. Read the task context and relevant docs.
2. Use `rg` to locate candidate files when the symbol or package is unknown.
3. Use `gopls` for symbol definitions, references, implementations, or call
   hierarchy before changing public or shared Go symbols.
4. Edit the smallest coherent set of files.
5. Run `task lsp` after Go edits.
6. Run focused tests selected for the changed behavior.
7. Run broader `task check` only when the workflow or risk level requires it.

## Review Workflow

1. Use `jj diff --stat` and targeted `jj diff` to identify changed Go files.
2. Use `gopls references` or `implementation` when a changed symbol has
   non-obvious callers or implementers.
3. Use `task lsp` to check changed Go files when review scope permits running
   commands.
4. Report diagnostics as code issues only when they reproduce through the repo
   task.

## Command Examples

```bash
gopls workspace_symbol Workflow
gopls workspace_symbol -matcher fuzzy workflow
gopls definition internal/config/types.go:645:6
gopls references -d internal/config/types.go:645:6
gopls implementation internal/config/types.go:645:6
gopls call_hierarchy internal/workflow/engine.go:42:6
task lsp
```
