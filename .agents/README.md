# Agent Layout

This repo uses three layers for Codex CLI guidance:

- Root [`AGENTS.md`](../AGENTS.md): always-on repo contract, skill triggers, and a few global guardrails.
- Local `AGENTS.md` files: subtree-specific durable policy near docs and future owner boundaries.
- [`.agents/skills/`](./skills/): procedural workflows for scoping, docs syncing, beads issue creation, mock review, test-surface selection, jj workflow, and final verification.

Recommended Codex CLI pattern:

1. Start from the root `AGENTS.md`.
2. Read the local `AGENTS.md` nearest the files you are changing.
3. Run the matching skill before broad changes and again for verification/handoff.
4. Update permanent docs when the change affects durable behavior or repo policy.
