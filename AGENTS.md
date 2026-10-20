# Agent instruction index

Read the rules that match the task before acting. If a task spans several
areas, read each matching rule. Do not load all rule files by default.

- Every task, Git commits, issues and PRs: [workflow](.agents/rules/workflow.md).
- Code, dependencies, native execution and data: [implementation](.agents/rules/implementation.md).
- Database schema, migrations and queries: [database](.agents/rules/database.md).
- Writing or changing tests: [testing](.agents/rules/testing.md).
- Documents and diagrams: [documentation](.agents/rules/documentation.md).
- Selecting checks or completing work: [verification](.agents/rules/verification.md).

Start with [architecture](docs/dev/architecture.md) to learn how business services
and the native engine fit together.

Repository skills live in `.agents/skills/`. Run `mise run skills:install` to
install the upstream skills. Read a skill when a rule links to it. If a skill
conflicts with a rule, the rule wins.
