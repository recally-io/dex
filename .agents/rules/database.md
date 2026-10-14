# Database

Read this before you change the schema, a migration, or a query.

dex is one local binary, so control uses SQLite (`modernc.org/sqlite`, no cgo)
instead of PostgreSQL. The agents' own data is in pi-durable storage in each
agent directory; control does not read it.

- `internal/db/migrations/` is the only schema source. Goose migrations are
  plain SQL with `-- +goose Up` and `-- +goose Down` sections. Create one with
  `goose -dir internal/db/migrations create <name> sql`.
- dex has no separate deploy step, so `db.Open` applies the migrations on
  start. Never edit a migration that a released build applied; add a new one.
- Write queries in `internal/db/queries/`. Run `mise run generate:db` and
  commit `internal/db/sqlc/`. `mise run check:db-drift` fails when the
  committed code differs from the generated code.
- Use `STRICT` tables.
