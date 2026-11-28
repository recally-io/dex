# Documentation

Read this before you write or change a document or a diagram.

- `README.md`: what dex is, the quick start, and links.
- `docs/product.md`: users, problem, scope, non-goals.
- `docs/dev/architecture.md`: the overview: runtime, data owners, message
  flow, failure behavior, and an index of the subsystem documents.
- `docs/dev/<subsystem>.md`: the details of one subsystem, such as
  `plugins.md` or `tasks.md`. Add a row to the index for a new one.
- `apps/web/AGENTS.md`: frontend routes, state, and conventions.
- `docs/assets/src/<name>.<ext>`: architecture and flow figures.
- `.local/`: plans and reports; ignored by Git.

## Architecture docs

The architecture docs are `docs/dev/architecture.md`, the subsystem documents
that it links, `apps/web/AGENTS.md`, and the figures in `docs/assets/`. Together
they are the single source of truth for the current design. They describe how the system works now, not its history.

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

Figures: write the source in `docs/assets/src/<name>.<ext>` and render it with
`mise run documd docs/assets/src/<name>.<ext> docs/assets/<name>.png`. Read the
`documd-visuals` skill before you draw. Embed the PNG and link the source next
to it. Look at the PNG before you commit, and commit the source and the PNG
together.
