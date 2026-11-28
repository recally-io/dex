# Database

Read this before you change the schema, a migration, or a query.

The shared core uses SQLite (`modernc.org/sqlite`, no cgo). Follow
[data ownership](../../docs/dev/architecture.md#data-ownership). Keep stable
request IDs across the separate business and execution commits.
`sessions.execution_id` links a business Session to its native conversation.

- `internal/db/migrations/` is the core schema. Goose migrations are
  plain SQL with `-- +goose Up` and `-- +goose Down` sections. Create one with
  `goose -dir internal/db/migrations create <name> sql`.
- dex has no separate deploy step, so `db.Open` applies the migrations on
  start. Never edit a migration that a released build applied; add a new one.
- A release branch can ship a migration with a later version than migrations
  on main, so `db.Open` also applies lower versions that are missing. A
  migration must not depend on a migration that another branch adds.
- A plugin keeps its schema in `plugins/<name>/migrations/` with versions
  `00001`, `00002`, and so on. Name its tables `plugin_<name>_<table>`. A core
  table never uses the `plugin_` prefix and never refers to a plugin table. dex
  applies plugin migrations only for an enabled plugin. See
  [plugin migrations](../../docs/dev/plugins.md#plugin-migrations).
- Write core queries in `internal/db/queries/` and plugin queries in
  `plugins/<name>/queries/`. Run `mise run generate:db` and commit
  `internal/db/sqlc/` and `plugins/<name>/store/`. `mise run check:db-drift` fails when the
  committed code differs from the generated code.
- Use `STRICT` tables.
