# Bot instruction index

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

Start with [README.md](README.md) for install and operation. Use the links
below for product scope and engineering references.

- [Product](docs/product.md): users, scope, and non-goals.
- [Glossary](docs/dev/glossary.md): canonical domain terms and cross-layer
  mapping.
- [Architecture](docs/dev/architecture.md): the overview, with links to one
  document per subsystem in `docs/dev/`, such as [Plugins](docs/dev/plugins.md).
- [Development](docs/dev/development.md): local setup, tasks, tests, and debug.
- [Releasing](docs/dev/releasing.md): CI shards and release workflow.
- [Web UI](apps/web/AGENTS.md): frontend routes, state, and shadcn conventions.

Follow the [documentation rule](.agents/rules/documentation.md#architecture-docs)
for design ownership and updates.

## Skills

Repository skills live in `.agents/skills/`. Run `mise run skills:install` to
install the upstream skills. Read a skill when a rule links to it. If a skill
conflicts with a rule, the rule wins.
