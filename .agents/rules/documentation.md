# Documentation

Read this before you write or change a document or a diagram.

- `README.md`: what dex is, the quick start, and links.
- `docs/product.md`: users, problem, scope, non-goals.
- `docs/dev/architecture.md`: subsystems, sources of truth, flows, failure
  behavior.
- `docs/dev/plugins.md`: the plugin boundary, lifecycle, and each plugin.
- `web/AGENTS.md`: frontend routes, state, and conventions.
- `assets/src/<name>.<ext>`: architecture and flow figures.
- `.local/`: plans and reports; ignored by Git.

## Architecture docs

The architecture docs are `docs/dev/architecture.md`, `docs/dev/plugins.md`,
`web/AGENTS.md`, and the figures in `assets/`. Together they are the single source of truth for the
current design. They describe how the system works now, not its history.

- When a change makes an architecture doc wrong, update that doc in the same
  commit. This includes a change to a subsystem, a contract, a flow, or a
  documented behavior.
- When a figure shows the changed part, update its source and render the PNG
  again.
- Do not add ADRs or other decision logs, and do not commit them. Put the
  reason for a decision in the commit message or the PR body.

## Style

Write English in ASD-STE100 style: short sentences, active voice, one meaning
per word. Use `mise run <task>` in commands. Link to the source of truth
instead of copying it.

Figures: write the source in `assets/src/<name>.<ext>` and render it with
`documd assets/src/<name>.<ext> assets/<name>.png`. Read the `documd-visuals`
skill before you draw. Embed the PNG and link the source next to it. Look at
the PNG before you commit, and commit the source and the PNG together.
