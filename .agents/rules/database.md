# Database

Read this before you change the schema, a migration, or a query.

dex is one local binary, so control uses SQLite (`modernc.org/sqlite`, no cgo)
instead of PostgreSQL. PiG execution data stays in each agent's
`state.sqlite`. Only `internal/engine` opens that storage. Business services
use typed engine methods; they do not query execution tables.

`control.sqlite` owns input intents, admission and outcome receipts, tasks,
settings, and channel reply delivery facts. `conversations.execution_id`
connects a business chat to its native execution conversation. Keep stable
request IDs across the two stores. Their commits are separate; reconciliation
must handle native admission before the receipt write and a receipt that
never reached native admission.

- `internal/db/migrations/` is the only schema source. Goose migrations are
  plain SQL with `-- +goose Up` and `-- +goose Down` sections. Create one with
  `goose -dir internal/db/migrations create <name> sql`.
- dex has no separate deploy step, so `db.Open` applies the migrations on
  start. Never edit a migration that a released build applied; add a new one.
- Write queries in `internal/db/queries/`. Run `mise run generate:db` and
  commit `internal/db/sqlc/`. `mise run check:db-drift` fails when the
  committed code differs from the generated code.
- Use `STRICT` tables.
