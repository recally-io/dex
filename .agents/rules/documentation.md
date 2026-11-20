# Documentation

Read this before you write or change a document or a diagram.

- `README.md`: what dex is, the quick start, and links.
- `docs/product.md`: users, problem, scope, non-goals.
- `docs/dev/architecture.md`: subsystems, sources of truth, flows, failure
  behavior. It is the single source of truth for the current design. It
  describes how the system works now, not its history. Update it in the same
  commit as a change to architecture, a contract, or a documented behavior.
- Do not add ADRs or other decision logs, and do not commit them. Put the
  reason for a decision in the commit message or the PR body.
- `.local/`: plans and reports; ignored by Git.

Write English in ASD-STE100 style: short sentences, active voice, one meaning
per word. Use `mise run <task>` in commands. Link to the source of truth
instead of copying it.

Figures: write the source in `assets/src/<name>.<ext>` and render it with
`documd assets/src/<name>.<ext> assets/<name>.png`. Read the `documd-visuals`
skill before you draw. Embed the PNG and link the source next to it. Look at
the PNG before you commit, and commit the source and the PNG together.
