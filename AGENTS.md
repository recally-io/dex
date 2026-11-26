# Agent instruction index

Read the rules that match the task before acting. If a task spans several
areas, read each matching rule. Do not load all rule files by default.

## Rules

- Every task, Git commits, issues and PRs: [workflow](.agents/rules/workflow.md).
- Code, dependencies, native execution and data: [implementation](.agents/rules/implementation.md).
- Database schema, migrations and queries: [database](.agents/rules/database.md).
- Writing or changing tests: [testing](.agents/rules/testing.md).
- Documents and diagrams: [documentation](.agents/rules/documentation.md).
- Selecting checks or completing work: [verification](.agents/rules/verification.md).

## Reference docs

Keep user-facing docs in [README.md](README.md) and agent or engineering
references here.

- [Product](docs/product.md): users, scope, and non-goals.
- [Glossary](docs/dev/glossary.md): canonical domain terms and cross-layer
  mapping.
- [Architecture](docs/dev/architecture.md): the overview, with links to one
  document per subsystem in `docs/dev/`, such as [Plugins](docs/dev/plugins.md).
- [Tasks](docs/dev/tasks.md): Board, Task access, and external runs.
- [Development](docs/dev/development.md): local setup, tasks, tests, and debug.
- [Releasing](docs/dev/releasing.md): CI shards and release workflow.
- [Web UI](web/AGENTS.md): frontend routes, state, and shadcn conventions.

The architecture docs are the single source of truth for the current design:
`docs/dev/architecture.md`, the subsystem documents that it links,
`web/AGENTS.md`, and the figures in `docs/assets/`.
When a change makes any of them wrong, update it in the same commit. Do not
add architecture decision records (ADRs) or other decision logs, and do not
commit them. Put the reason for a decision in the commit message or the PR
body.

## Skills

Repository skills live in `.agents/skills/`. Run `mise run skills:install` to
install the upstream skills. Read a skill when a rule links to it. If a skill
conflicts with a rule, the rule wins.
