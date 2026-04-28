# Agent Rules

## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run `bd prime` for workflow contex.

**Quick reference:**
- `bd ready` - Find unblocked work
- `bd create "Title" --type task --priority 2` - Create issue
- `bd close <id>` - Complete work
- `bd dolt push` - Push beads to remote

For full workflow details: `bd prime`

Important:
- This repo uses jj, not git.
- Use `jj status`, `jj diff`, and `jj describe`.
- Do not run git commands unless explicitly asked.
- Beads is git-free here. Use `BEADS_DIR=$PWD/../.beads`.
- Do not use `bd edit`; use `bd update` with flags.
- Before work: `bd ready --json`.
- Claim work: `bd update <id> --claim`.

